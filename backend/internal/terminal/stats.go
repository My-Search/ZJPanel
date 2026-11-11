package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/crypto/ssh"
)

// HostStats 终端底部状态栏数据：所连机器的核心资源占用。
// CPU/Mem/Disk 为百分比（0-100），NetIn/NetOut 为 B/s，Load1 为 1 分钟负载。
type HostStats struct {
	Time    time.Time `json:"time"`
	CPU     float64   `json:"cpu"`
	Mem     float64   `json:"mem"`
	MemTXT  string    `json:"memTxt"`
	Disk    float64   `json:"disk"`
	DiskTXT string    `json:"diskTxt"`
	NetIn   float64   `json:"netIn"`
	NetOut  float64   `json:"netOut"`
	Load1   float64   `json:"load1"`
}

const statsInterval = 5 * time.Second

// StatsCollector 周期采集一台机器的资源占用：本地走 gopsutil，SSH 复用连接执行只读命令。
// CPU 与网络速率为相邻两次采样的差分；采集失败静默保留上一次结果（终端场景宁缺毋吵）。
type StatsCollector struct {
	stop     chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	has     bool
	last    HostStats
	prevCPU cpuTimes
	prevNet netCounts
}

type cpuTimes struct{ busy, total float64 }

type netCounts struct {
	in, out uint64
	t       time.Time
}

// sshClient = *ssh.Client（远程采样直接复用会话已建立的 SSH 连接）。

// StartLocal 采集面板宿主机（本地终端会话）。
func (c *StatsCollector) StartLocal() { go c.loop(c.sampleLocal) }

// StartRemote 采集 SSH 远程机器（复用既有 SSH 连接，无需对端装 agent）。
func (c *StatsCollector) StartRemote(client *ssh.Client) {
	go c.loop(func() (HostStats, error) { return c.sampleRemote(client) })
}

// Stop 停止采集循环（幂等）。
func (c *StatsCollector) Stop() { c.stopOnce.Do(func() { close(c.stop) }) }

// Last 返回最近一次成功采样；从未成功时 ok=false（前端显示采集中）。
func (c *StatsCollector) Last() (HostStats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.has {
		return HostStats{}, false
	}
	return c.last, true
}

func (c *StatsCollector) loop(sample func() (HostStats, error)) {
	if st, err := sample(); err == nil {
		c.save(st)
	}
	tick := time.NewTicker(statsInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-tick.C:
			if st, err := sample(); err == nil {
				c.save(st)
			}
		}
	}
}

func (c *StatsCollector) save(st HostStats) {
	c.mu.Lock()
	c.has, c.last = true, st
	c.mu.Unlock()
}

// ---- 本机采样 ----

func (c *StatsCollector) sampleLocal() (HostStats, error) {
	st := HostStats{Time: time.Now()}
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		st.CPU = pcts[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		st.Mem = vm.UsedPercent
		st.MemTXT = humanBytes(vm.Used) + " / " + humanBytes(vm.Total)
	}
	d, txt := c.diskLocal()
	st.Disk, st.DiskTXT = d, txt
	if la, err := load.Avg(); err == nil {
		st.Load1 = la.Load1
	}
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		st.NetIn, st.NetOut = c.netRates(io[0].BytesRecv, io[0].BytesSent, st.Time)
	}
	return st, nil
}

// diskLocal 取根分区用量（/ 是系统盘最稳定的锚点；overlay 容器内即容器可写层所在盘）。
func (c *StatsCollector) diskLocal() (float64, string) {
	du, err := disk.Usage("/")
	if err != nil || du.Total == 0 {
		return 0, ""
	}
	return du.UsedPercent, humanBytes(du.Used) + " / " + humanBytes(du.Total)
}

// ---- SSH 远程采样（一次会话执行一条只读命令） ----

// remoteCmd 拼接只读采样命令：/proc 三文件 + 根分区 df（POSIX 输出）。
func remoteCmd() string {
	return "cat /proc/stat /proc/meminfo /proc/net/dev 2>/dev/null; echo ---; df -kP / 2>/dev/null"
}

func (c *StatsCollector) sampleRemote(client *ssh.Client) (HostStats, error) {
	out, err := runRemote(client, remoteCmd())
	if err != nil {
		return HostStats{}, err
	}
	st, err := c.parseRemote(out, time.Now())
	if err != nil {
		return HostStats{}, err
	}
	return st, nil
}

func runRemote(client *ssh.Client, cmd string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.Output(cmd)
}

// parseRemote 解析 cat /proc/stat /proc/meminfo /proc/net/dev + df -kP / 的合并输出。
func (c *StatsCollector) parseRemote(out []byte, now time.Time) (HostStats, error) {
	st := HostStats{Time: now}
	var memTotalKB, memAvailKB uint64
	var netIn, netOut uint64
	dfDone := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "---":
			dfDone = false // df 段开始（表头 + 数据行）
		case strings.HasPrefix(line, "cpu "):
			if ct, ok := parseProcStatCPU(line); ok {
				st.CPU = c.cpuPct(ct)
			}
		case strings.HasPrefix(line, "MemTotal:"):
			memTotalKB, _ = strconv.ParseUint(trimColon(line), 10, 64)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvailKB, _ = strconv.ParseUint(trimColon(line), 10, 64)
		case strings.Contains(line, ":") && !dfDone && !strings.HasPrefix(line, "Filesystem"):
			if in, out2, ok := parseProcNetDev(line); ok {
				netIn, netOut = netIn+in, netOut+out2 // 所有物理网卡累计（lo 已排除）
			}
		case dfDone && strings.HasPrefix(line, "/"):
			if f := strings.Fields(line); len(f) >= 5 {
				totalKB, _ := strconv.ParseUint(f[1], 10, 64)
				usedKB, _ := strconv.ParseUint(f[2], 10, 64)
				if totalKB > 0 {
					st.Disk = float64(usedKB) / float64(totalKB) * 100
					st.DiskTXT = humanBytes(usedKB * 1024) + " / " + humanBytes(totalKB * 1024)
				}
			}
		case strings.HasPrefix(line, "Filesystem"):
			dfDone = true
		}
	}
	if memTotalKB > 0 && memAvailKB > 0 {
		used := (memTotalKB - memAvailKB) * 1024
		st.Mem = float64(memTotalKB-memAvailKB) / float64(memTotalKB) * 100
		st.MemTXT = humanBytes(used) + " / " + humanBytes(memTotalKB*1024)
	}
	st.NetIn, st.NetOut = c.netRates(netIn, netOut, now)
	if st.CPU == 0 && st.Mem == 0 && st.Disk == 0 {
		return st, fmt.Errorf("远程采样输出无法解析")
	}
	return st, nil
}

// parseProcStatCPU 解析 "cpu  user nice system idle iowait irq softirq steal ..." 行。
func parseProcStatCPU(line string) (cpuTimes, bool) {
	f := strings.Fields(line)
	if len(f) < 5 {
		return cpuTimes{}, false
	}
	var total, idle float64
	for i, v := range f[1:] {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	return cpuTimes{busy: total - idle, total: total}, true
}

// parseProcNetDev 解析 "  eth0: bytes packets ..." 行，返回收发字节数（lo 排除）。
func parseProcNetDev(line string) (uint64, uint64, bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return 0, 0, false
	}
	iface := strings.TrimSpace(line[:idx])
	if iface == "" || iface == "lo" || iface == "face" {
		return 0, 0, false
	}
	f := strings.Fields(line[idx+1:])
	if len(f) < 9 {
		return 0, 0, false
	}
	in, err1 := strconv.ParseUint(f[0], 10, 64)
	out, err2 := strconv.ParseUint(f[8], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return in, out, true
}

func trimColon(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return strings.TrimSpace(line)
}

// ---- 差分计算（相邻两次采样） ----

func (c *StatsCollector) cpuPct(ct cpuTimes) float64 {
	c.mu.Lock()
	prev := c.prevCPU
	c.prevCPU = ct
	c.mu.Unlock()
	dt := ct.total - prev.total
	if prev.total == 0 || dt <= 0 {
		return 0 // 首个样本无基线
	}
	return clampPct((ct.busy - prev.busy) / dt * 100)
}

func (c *StatsCollector) netRates(in, out uint64, now time.Time) (float64, float64) {
	c.mu.Lock()
	prev := c.prevNet
	c.prevNet = netCounts{in: in, out: out, t: now}
	c.mu.Unlock()
	if prev.t.IsZero() || !now.After(prev.t) {
		return 0, 0
	}
	dt := now.Sub(prev.t).Seconds()
	return clampPct(float64(in-prev.in) / dt), clampPct(float64(out-prev.out) / dt)
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- Manager 接线 ----

// EnableStats 为会话启用底部状态栏采集：ssh=远程机器，local=面板宿主机。
// 采集器随会话关闭（Manager.remove）一并停止。
func (m *Manager) EnableStats(sess *Session) {
	if sess == nil || sess.isClosed() {
		return
	}
	c := &StatsCollector{}
	switch sess.Kind {
	case "ssh":
		p, ok := sess.pty.(*sshPty)
		if !ok || p == nil || p.client == nil {
			return
		}
		c.StartRemote(p.client)
	default:
		c.StartLocal()
	}
	sess.SetStats(c)
}
