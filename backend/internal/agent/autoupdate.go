package agent

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/model"
)

// 自动升级：Agent 每次回连（hello）时比对自身版本与面板内置 Agent 版本，
// 面板版本更新则立即下发 upgrade 指令。手动升级同样走这条通道，因此
// 最近 10 分钟已有升级指令时不再重复下发，避免失败场景每次重连都触发；
// 连续失败再按 10m→20m→40m 指数退避（封顶 1h），长期坏机不再刷屏。

const autoUpgradeCooldown = 10 * time.Minute

// autoUpgradeBackoff 连续失败时指数退避：10m→20m→40m→封顶 1h。
// 避免长期坏机（磁盘满/凭据失效）每 10 分钟重试一次并刷一条告警。
func autoUpgradeBackoff(fails int) time.Duration {
	d := autoUpgradeCooldown << fails
	if d <= 0 || d > time.Hour {
		d = time.Hour
	}
	return d
}

// markUpgradeFail / markUpgradeOK 维护连续失败计数，成功一次即清零。
func (s *Service) markUpgradeFail(hostID uint) {
	s.mu.Lock()
	s.upgradeFails[hostID]++
	s.mu.Unlock()
}

func (s *Service) markUpgradeOK(hostID uint) {
	s.mu.Lock()
	delete(s.upgradeFails, hostID)
	s.mu.Unlock()
}

func (s *Service) maybeAutoUpgrade(hostID uint, agentVer, osName, arch string) {
	if s.cfg == nil || !s.cfg.AgentAutoUpdate {
		return
	}
	panelVer := buildinfo.Version
	if !versionNewer(panelVer, agentVer) {
		return // 面板不是更新版本（含 dev 构建/降级场景）：不动
	}
	if osName != "" && osName != "linux" {
		return // 目前只分发 linux 静态二进制
	}
	a := normalizeArch(arch)
	if BinaryPath(s.cfg, a) == "" {
		slog.Debug("Agent 自动升级跳过：面板未内置该架构二进制", "host", hostID, "arch", a)
		return
	}

	s.mu.Lock()
	if s.upgrading[hostID] {
		s.mu.Unlock()
		return
	}
	s.upgrading[hostID] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.upgrading, hostID)
			s.mu.Unlock()
		}()

		// 冷却期内已有升级记录（手动或上次自动）就不再重复：升级失败时
		// Agent 会保持旧版本重连，没有冷却会形成「每次重连都下载一遍」。
		// 冷却窗口随连续失败次数指数退避；查询出错时保守跳过本轮，
		// 避免把失败当成「无记录」而重复下发。
		s.mu.Lock()
		cd := autoUpgradeBackoff(s.upgradeFails[hostID])
		s.mu.Unlock()
		var recent int64
		if err := s.db.Model(&model.AgentCommand{}).
			Where("host_id = ? AND kind = ? AND created_at > ?", hostID, "upgrade", time.Now().Add(-cd)).
			Count(&recent).Error; err != nil {
			slog.Warn("Agent 自动升级冷却查询失败，本轮跳过", "host", hostID, "err", err)
			return
		}
		if recent > 0 {
			return
		}

		bin := BinaryPath(s.cfg, a)
		if bin == "" {
			return
		}
		slog.Info("Agent 回连检测到新版本，自动升级", "host", hostID, "from", agentVer, "to", panelVer, "arch", a)
		res, err := s.Exec(hostID, "upgrade", map[string]any{
			"path":    "/api/agent/bin/" + a,
			"sha256":  FileSHA256(bin),
			"version": panelVer,
		}, 90*time.Second)
		if err != nil {
			s.markUpgradeFail(hostID)
			slog.Warn("Agent 自动升级失败", "host", hostID, "err", err)
			s.emitSystem(hostID, "Agent 自动升级", "warn", "升级指令失败："+err.Error())
			return
		}
		if !res.OK {
			s.markUpgradeFail(hostID)
			slog.Warn("Agent 自动升级被拒绝", "host", hostID, "output", res.Output)
			s.emitSystem(hostID, "Agent 自动升级", "warn", "升级未执行："+firstLine(res.Output))
			return
		}
		s.markUpgradeOK(hostID)
		s.emitSystem(hostID, "Agent 自动升级", "info", agentVer+" → "+panelVer+"，等待 Agent 重启回连")
	}()
}

// normalizeArch 把 Agent 上报的架构名映射到二进制文件名后缀。
func normalizeArch(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}

// versionNewer 判断 a 是否为比 b 更新的正式版本（a > b）。
// 只处理 x.y.z 数字段与 -pre 后缀：a 带预发布后缀时不自动升级（避免把线上
// Agent 升到 rc/dev 构建）；b 带后缀而 a 是正式版时视为更新。
func versionNewer(a, b string) bool {
	na, pa, oka := splitVersion(a)
	nb, pb, okb := splitVersion(b)
	if !oka || !okb || pa != "" {
		return false
	}
	for i := 0; i < len(na) || i < len(nb); i++ {
		x, y := 0, 0
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			return x > y
		}
	}
	// 数字段相同：对方是预发布版才算更新
	return pb != ""
}

// splitVersion 解析 v1.2.3-rc1：返回数字段、预发布后缀。
func splitVersion(v string) ([]int, string, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return nil, "", false
	}
	if i := strings.IndexByte(v, '+'); i >= 0 { // 丢弃 build metadata
		v = v[:i]
	}
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre, v = v[i+1:], v[:i]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, "", false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}
