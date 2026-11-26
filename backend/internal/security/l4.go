package security

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// l4Prev 上一次采样的累计计数，用于计算每秒带宽与队列溢出增量。
type l4Prev struct {
	t          time.Time
	rx, tx     uint64
	overflows  uint64
	drops      uint64
	overflowOK bool
}

// readL4 从 /proc 读内核层指标（Linux）。容器是 host 网络，不需要任何 cap 即可读；
// 非 Linux（本地开发 Windows）返回 Supported=false，前端隐藏该卡片。
//
// 为什么必须有这一层：SYN Flood / UDP 反射 / 连接数打满，nginx access log 一条都不记，
// 只看日志的安全面板会在站点已经被打挂时显示"一切正常"。
func readL4(prev *l4Prev) L4Stat {
	st := L4Stat{}
	if runtime.GOOS != "linux" {
		return st
	}
	now := time.Now()

	if rx, tx, ok := readNetDev(); ok {
		if !prev.t.IsZero() && now.After(prev.t) {
			dt := now.Sub(prev.t).Seconds()
			if dt > 0 && rx >= prev.rx && tx >= prev.tx {
				st.RxBps = float64(rx-prev.rx) / dt
				st.TxBps = float64(tx-prev.tx) / dt
			}
		}
		prev.rx, prev.tx = rx, tx
	}

	est, syn, tw, ok := readTCPStates()
	if ok {
		st.Conn, st.SynRecv, st.TimeWait = est, syn, tw
	}

	if ov, dr, ok := readSNMP(); ok {
		if prev.overflowOK && ov >= prev.overflows {
			st.Overflows = ov - prev.overflows
		}
		if prev.overflowOK && dr >= prev.drops {
			st.Drops = dr - prev.drops
		}
		prev.overflows, prev.drops, prev.overflowOK = ov, dr, true
	}

	prev.t = now
	st.Supported = ok || st.Conn > 0 || st.SynRecv > 0 || st.RxBps > 0
	return st
}

// readNetDev 汇总非 lo 网卡的收发字节（/proc/net/dev）。
func readNetDev() (rx, tx uint64, ok bool) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(ln[:i])
		if strings.HasPrefix(name, "lo") {
			continue
		}
		f := strings.Fields(ln[i+1:])
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
		ok = true
	}
	return
}

// readTCPStates 统计 ESTABLISHED / SYN_RECV / TIME_WAIT。
func readTCPStates() (est, syn, tw int, ok bool) {
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ok = true
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if len(f) < 4 {
				continue
			}
			switch f[3] {
			case "01":
				est++
			case "03":
				syn++
			case "06":
				tw++
			}
		}
	}
	return
}

// readSNMP 读 TcpExt 的 ListenOverflows / ListenDrops（accept 队列被打满的直接证据）。
func readSNMP() (overflows, drops uint64, ok bool) {
	b, err := os.ReadFile("/proc/net/snmp")
	if err != nil {
		return 0, 0, false
	}
	lines := strings.Split(string(b), "\n")
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "TcpExt:") || !strings.HasPrefix(lines[i+1], "TcpExt:") {
			continue
		}
		hdr := strings.Fields(lines[i])[1:]
		val := strings.Fields(lines[i+1])[1:]
		idx := map[string]int{}
		for j, h := range hdr {
			idx[h] = j
		}
		get := func(name string) uint64 {
			if j, yes := idx[name]; yes && j < len(val) {
				v, _ := strconv.ParseUint(val[j], 10, 64)
				return v
			}
			return 0
		}
		return get("ListenOverflows"), get("ListenDrops"), true
	}
	return 0, 0, false
}
