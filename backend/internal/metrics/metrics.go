package metrics

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/host"

	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/ws"
)

// Service 服务器指标：gopsutil 快照 + 周期采样入库（默认 60s，保留 30 天）+ WS 推送。

type Snapshot struct {
	Time    time.Time `json:"time"`
	CPU     float64   `json:"cpu"`
	MemUsed uint64    `json:"memUsed"`
	MemTotal uint64   `json:"memTotal"`
	MemPct  float64   `json:"memPct"`
	SwapUsed uint64   `json:"swapUsed"`
	SwapTotal uint64  `json:"swapTotal"`
	Load1   float64   `json:"load1"`
	Load5   float64   `json:"load5"`
	Load15  float64   `json:"load15"`
	NetIn   uint64    `json:"netIn"`  // 累计
	NetOut  uint64    `json:"netOut"`
	NetRateIn  float64 `json:"netRateIn"`  // B/s
	NetRateOut float64 `json:"netRateOut"`
	Disks   []Disk    `json:"disks"`
	Uptime  uint64    `json:"uptime"`
	OS      string    `json:"os"`
	Hostname string   `json:"hostname"`
}

type Disk struct {
	Device  string  `json:"device"`
	Mount   string  `json:"mount"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	UsedPct float64 `json:"usedPct"`
}

type Service struct {
	db      *gorm.DB
	hub     *ws.Hub
	mu      sync.RWMutex
	last    *Snapshot
	lastNet struct {
		in, out uint64
		t       time.Time
	}
	stop chan struct{}
	once sync.Once
}

func New(db *gorm.DB, hub *ws.Hub) *Service {
	return &Service{db: db, hub: hub, stop: make(chan struct{})}
}

func (s *Service) Close() { s.once.Do(func() { close(s.stop) }) }

// Start 启动采样与 WS 推送循环。
func (s *Service) Start() {
	go func() {
		// 首次 cpu percent 需要间隔
		_, _ = cpu.Percent(300*time.Millisecond, false)
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				snap := s.Snapshot()
				s.mu.Lock()
				prev := s.last
				s.last = snap
				s.mu.Unlock()
				// 网络速率
				if prev != nil {
					dt := snap.Time.Sub(prev.Time).Seconds()
					if dt > 0 {
						snap.NetRateIn = float64(snap.NetIn-prev.NetIn) / dt
						snap.NetRateOut = float64(snap.NetOut-prev.NetOut) / dt
					}
				}
				if s.hub.Count("metrics") > 0 {
					b, _ := json.Marshal(snap)
					s.hub.Broadcast("metrics", b)
				}
			}
		}
	}()
	// 采样入库（60s）
	go func() {
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				snap := s.snapshotRaw()
				if snap == nil {
					continue
				}
				s.db.Create(&model.MetricSample{
					Time: snap.Time, CPU: snap.CPU,
					MemUsed: snap.MemUsed, MemTotal: snap.MemTotal,
					NetIn: snap.NetIn, NetOut: snap.NetOut, Load1: snap.Load1,
				})
				// 清理 30 天前
				cut := time.Now().AddDate(0, 0, -30)
				s.db.Where("time < ?", cut).Delete(&model.MetricSample{})
			}
		}
	}()
}

func (s *Service) snapshotRaw() *Snapshot {
	ctx := context2()
	snap := &Snapshot{Time: time.Now()}
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		snap.CPU = pct[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		snap.MemUsed, snap.MemTotal = vm.Used, vm.Total
		snap.MemPct = vm.UsedPercent
		snap.SwapUsed, snap.SwapTotal = vm.SwapTotal-vm.SwapFree, vm.SwapTotal
	}
	if la, err := load.AvgWithContext(ctx); err == nil {
		snap.Load1, snap.Load5, snap.Load15 = la.Load1, la.Load5, la.Load15
	}
	if ioStats, err := gnet.IOCountersWithContext(ctx, false); err == nil && len(ioStats) > 0 {
		snap.NetIn, snap.NetOut = ioStats[0].BytesRecv, ioStats[0].BytesSent
	}
	if hostInfo, err := host.InfoWithContext(ctx); err == nil {
		snap.Uptime = hostInfo.Uptime
		snap.OS = hostInfo.OS
		snap.Hostname = hostInfo.Hostname
	}
	return snap
}

func (s *Service) Snapshot() *Snapshot {
	snap := s.snapshotRaw()
	if snap == nil {
		return &Snapshot{Time: time.Now()}
	}
	// 磁盘占用（挂载点去重）
	seen := map[string]bool{}
	snap.Disks = []Disk{}
	if parts, err := disk.PartitionsWithContext(context2(), false); err == nil {
		for _, p := range parts {
			if seen[p.Mountpoint] {
				continue
			}
			seen[p.Mountpoint] = true
			if du, err := disk.UsageWithContext(context2(), p.Mountpoint); err == nil && du.Total > 0 {
				snap.Disks = append(snap.Disks, Disk{
					Device: p.Device, Mount: p.Mountpoint,
					Total: du.Total, Used: du.Used, UsedPct: du.UsedPercent,
				})
			}
		}
	}
	s.mu.RLock()
	prev := s.last
	s.mu.RUnlock()
	if prev != nil {
		dt := snap.Time.Sub(prev.Time).Seconds()
		if dt > 0 && prev.NetIn > 0 {
			snap.NetRateIn = float64(snap.NetIn-prev.NetIn) / dt
			snap.NetRateOut = float64(snap.NetOut-prev.NetOut) / dt
		}
	}
	return snap
}

// History 历史采样（hours 小时内）。
func (s *Service) History(hours int) []model.MetricSample {
	if hours <= 0 {
		hours = 24
	}
	cut := time.Now().Add(-time.Duration(hours) * time.Hour)
	var rows []model.MetricSample
	s.db.Where("time > ?", cut).Order("time").Limit(4000).Find(&rows)
	return rows
}

var _ = runtime.GOOS
var _ = slog.Info

func context2() context.Context { return context.Background() }
