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
// 结果缓存 + WS 推送 + 离线事件发布（插件可订阅告警）。

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
	db      *gorm.DB
	bus     *bus.Bus
	mu      sync.RWMutex
	results map[string]*Info
	client  *http.Client
	stop    chan struct{}
	once    sync.Once
}

func New(db *gorm.DB, b *bus.Bus) *Service {
	tlsConf := &tls.Config{InsecureSkipVerify: true} // 探测自签/过期证书也要拿到信息
	return &Service{
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
}

// Start 启动周期探测循环。
func (s *Service) Start(interval time.Duration) {
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.probeAll()
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

func (s *Service) probeAll() {
	var sites []model.Site
	if err := s.db.Find(&sites).Error; err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, site := range sites {
		wg.Add(1)
		go func(site model.Site) {
			defer wg.Done()
			info := s.probe(site)
			prev := s.Get(site.Domain)
			s.mu.Lock()
			s.results[site.Domain] = info
			s.mu.Unlock()
			// 状态变化事件（供插件告警）
			if prev != nil && prev.Status == "online" && info.Status != "online" {
				s.bus.Publish("sitecheck.down", map[string]any{"domain": site.Domain, "error": info.Error})
			}
			if prev != nil && prev.Status != "online" && info.Status == "online" {
				s.bus.Publish("sitecheck.up", map[string]any{"domain": site.Domain})
			}
		}(site)
	}
	wg.Wait()
}

// probe 本机探测：请求 127.0.0.1（nginx 监听口）并携带 Host/SNI，避免依赖外部 DNS。
func (s *Service) probe(site model.Site) *Info {
	info := &Info{Domain: site.Domain, CheckedAt: time.Now()}
	scheme, port := "http", 80
	if site.SSL {
		scheme, port = "https", 443
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(context.Background(), "GET", fmt.Sprintf("%s://127.0.0.1:%d/", scheme, port), nil)
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

var _ = slog.Info // 保持引用
