package sitecheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/model"
)

// Service 后台站点探测（nginx-ui sitecheck 模式）：周期探测各站点，测量延迟与证书剩余天数，
// 结果缓存 + 离线事件发布（插件可订阅告警），并把最近一次结果落库供重启后展示。
//
// 应用级「停用」声明（ComposeProject.Disabled）：停用应用关联网站的探测照常跑，
// 但掉线不发通知、也不计入安全中心——人工关停的站没必要反复报故障。

const listenHost = "127.0.0.1" // 本机探测固定走回环：不依赖外部 DNS，也不把探测流量算到真实客户端

type Info struct {
	Domain       string    `json:"domain"`
	Status       string    `json:"status"` // online | offline | error
	Code         int       `json:"code"`
	LatencyMs    int64     `json:"latencyMs"`
	CertDaysLeft int       `json:"certDaysLeft"` // SSL 站点证书剩余天数
	CheckedAt    time.Time `json:"checkedAt"`
	Error        string    `json:"error,omitempty"`
}

type Service struct {
	db  *gorm.DB
	bus *bus.Bus

	mu      sync.RWMutex
	results map[string]*Info

	// probeMu 防止上一轮未结束（站点多/上游慢）时下一轮 ticker 叠加探测。
	probeMu sync.Mutex

	client *http.Client
	prober func(model.Site) *Info // 测试注入点；生产固定为 probe

	stop chan struct{}
	once sync.Once
}

func New(db *gorm.DB, b *bus.Bus) *Service {
	tlsConf := &tls.Config{InsecureSkipVerify: true} // 探测自签/过期证书也要拿到信息
	s := &Service{
		db: db, bus: b, results: map[string]*Info{},
		client: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig:   tlsConf,
				DisableKeepAlives: true,
			},
		},
		stop: make(chan struct{}),
	}
	s.prober = s.probe
	return s
}

// Start 启动周期探测：启动即全量探测一次，此后每个 interval 全量探测。
func (s *Service) Start(interval time.Duration) {
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	go func() {
		s.probeAll()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.probeAll()
			}
		}
	}()
}

func (s *Service) Close() { s.once.Do(func() { close(s.stop) }) }

func (s *Service) listSites() ([]model.Site, error) {
	var sites []model.Site
	if err := s.db.Find(&sites).Error; err != nil {
		slog.Warn("站点探测读取站点失败", "err", err)
		return nil, err
	}
	return sites, nil
}

// disabledProjects 停用应用的项目 ID 集合（每轮探测取一次，避免逐站点查库）。
func (s *Service) disabledProjects() map[uint]bool {
	out := map[uint]bool{}
	if s.db == nil {
		return out
	}
	var ids []uint
	if err := s.db.Model(&model.ComposeProject{}).Where("disabled = ?", true).Pluck("id", &ids).Error; err != nil {
		slog.Warn("站点探测读取停用应用失败", "err", err)
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func siteDisabled(site model.Site, disabled map[uint]bool) bool {
	return site.ComposeProjectID != nil && disabled[*site.ComposeProjectID]
}

// probeAll 全量并发探测一轮；结果串行落库（SQLite 单写，避免并发写锁）。
func (s *Service) probeAll() {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()

	sites, err := s.listSites()
	if err != nil || len(sites) == 0 {
		return
	}
	disabled := s.disabledProjects()

	infos := make([]*Info, len(sites))
	var wg sync.WaitGroup
	for i := range sites {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.prober != nil {
				infos[i] = s.prober(sites[i])
			}
		}(i)
	}
	wg.Wait()
	for i := range sites {
		s.record(sites[i], infos[i], siteDisabled(sites[i], disabled))
	}
}

// record 记录单个站点的探测结果：缓存 → 状态落库 → 状态变化事件。
func (s *Service) record(site model.Site, info *Info, ignored bool) {
	if info == nil {
		return
	}
	prev := s.Get(site.Domain)
	upd := map[string]any{
		"check_status":     info.Status,
		"check_code":       info.Code,
		"check_latency_ms": info.LatencyMs,
		"check_error":      info.Error,
		"checked_at":       info.CheckedAt,
	}

	s.mu.Lock()
	s.results[site.Domain] = info
	s.mu.Unlock()

	if s.db != nil {
		if err := s.db.Model(&model.Site{}).Where("id = ?", site.ID).Updates(upd).Error; err != nil {
			slog.Warn("站点探测结果落库失败", "domain", site.Domain, "err", err)
		}
	}

	if s.bus == nil || prev == nil {
		return
	}
	// 停用应用下的站点为人工关停：掉线不告警（探测结果仍照常展示，面板上不被当成故障）。
	if prev.Status == "online" && info.Status != "online" && !ignored {
		s.bus.Publish("sitecheck.down", map[string]any{"domain": site.Domain, "error": info.Error})
	}
	// 恢复事件仅供插件订阅（内置站内信不再发 up 通知，属噪音）。
	if prev.Status != "online" && info.Status == "online" {
		s.bus.Publish("sitecheck.up", map[string]any{"domain": site.Domain})
	}
}

// probe 本机探测：请求 127.0.0.1（nginx 监听口）并携带 Host/SNI，避免依赖外部 DNS。
func (s *Service) probe(site model.Site) *Info {
	info := &Info{Domain: site.Domain, CheckedAt: time.Now()}
	scheme, port := "http", 80
	if site.SSL {
		scheme, port = "https", 443
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(context.Background(), "GET", fmt.Sprintf("%s://%s:%d/", scheme, listenHost, port), nil)
	if err != nil {
		info.Status, info.Error = "error", err.Error()
		return info
	}
	req.Host = site.Domain
	req.Header.Set("User-Agent", "ZJPanel-SiteCheck/1.0")
	resp, err := s.client.Do(req)
	info.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		info.Status, info.Error = "offline", err.Error()
		return info
	}
	defer resp.Body.Close()
	info.Code = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		info.Status = "online"
	} else {
		info.Status = "error"
		info.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	// TLS 证书剩余天数
	if site.SSL && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		notAfter := resp.TLS.PeerCertificates[0].NotAfter
		info.CertDaysLeft = int(time.Until(notAfter).Hours() / 24)
	}
	return info
}

func (s *Service) Get(domain string) *Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.results[domain]
}

func (s *Service) All() map[string]*Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*Info, len(s.results))
	for k, v := range s.results {
		out[k] = v
	}
	return out
}
