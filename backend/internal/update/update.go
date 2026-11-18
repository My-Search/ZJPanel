package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/pkg"
)

var versionTagRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Service 版本检查与一键更新：
// 检查 = 拉取版本清单（GitHub releases/latest 或自定义 {version,url} JSON），semver 对比；
// 应用 = 按运行环境分两条路径（见 docker.go 的 detectContainer）：
//   - compose 容器：镜像式更新（docker.go）。把宿主机 compose 的 image 固化为具体版本 tag →
//     清理本地旧镜像（含 :latest 缓存）→ 用一次性容器延迟重建，由 compose up 拉取新镜像。
//     版本固化 + 旧缓存清理双管齐下，down/up 后不再回退；另外每次启动执行 SelfHeal 自愈，
//     即使是被二进制热更上来的环境，也会在启动后把 compose 固化、清掉旧镜像。
//   - 非容器/本地：二进制式更新。从 GitHub Release 下载当前平台二进制，校验后原子替换自身并重启，
//     dist 内嵌一并生效；重启方式见 restart_*.go（Linux syscall.Exec 原位替换，PID 不变）。

type Service struct {
	cfg       *pkg.Config
	container *containerUpdater // 非 nil = compose 容器环境，走镜像式更新

	mu sync.Mutex // 防并发重复更新
}

func New(cfg *pkg.Config) *Service { return &Service{cfg: cfg, container: detectContainer(cfg)} }

func CurrentVersion() string { return buildinfo.Version }

// SelfHeal 启动自愈：容器环境下把 compose 的 image 固化到当前二进制版本，并清理本地旧镜像。
//
// 这是"面板二进制热更 / 旧镜像升级上来后，down/up 又回退"的根治补丁：
// 老逻辑只在用户点"一键更新"时才固化 compose，而二进制热更把版本号推到最新后，
// 检查更新显示"已是最新"，用户再无机会触发固化。改为进程每次启动即自愈，
// 不依赖用户操作；本地直跑（非容器）或版本不可固化时安全跳过。
func (s *Service) SelfHeal() {
	if s.container == nil {
		return
	}
	// dev / 空版本不固化（本地开发构建不应写 compose）。
	v := CurrentVersion()
	if v == "" || strings.Contains(v, "dev") || strings.Contains(v, "v0.0.0") {
		return
	}
	tag := "v" + strings.TrimPrefix(v, "v")
	target := s.container.imageRepo + ":" + tag
	if _, err := s.container.pinComposeImage(target); err != nil {
		slog.Warn("面板更新：启动自愈固化 compose 失败", "target", target, "err", err)
		return
	}
	slog.Info("面板更新：启动自愈已固化镜像并清理本地旧缓存", "image", target)
}

// releaseInfo 版本清单解析结果。
type releaseInfo struct {
	Tag         string // GitHub tag（如 v0.1.8.1）；自定义清单为空
	Version     string // 语义化版本（去 v 前缀）
	DownloadURL string // 自定义清单提供的直链（GitHub 路径时为空，按 tag 拼接）
	SHA256      string // 当前平台二进制的期望 SHA-256（GitHub Release assets.digest 或自定义 sha256 字段）
	Notes       string
}

// fetchRelease 拉取并解析版本清单：GitHub releases/latest JSON（tag_name）或自定义 {version,downloadUrl}。
func (s *Service) fetchRelease() (*releaseInfo, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(s.cfg.UpdateURL)
	if err != nil {
		return nil, fmt.Errorf("版本清单拉取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("版本清单拉取失败: HTTP %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	info := &releaseInfo{}
	if tn, ok := raw["tag_name"].(string); ok && tn != "" {
		// GitHub releases/latest
		if !versionTagRE.MatchString(tn) || strings.Contains(tn, "..") {
			return nil, fmt.Errorf("版本清单 tag_name 非法: %q", tn)
		}
		info.Tag = tn
		info.Version = strings.TrimPrefix(tn, "v")
		info.Notes, _ = raw["html_url"].(string)
		info.SHA256 = extractAssetSHA256(raw["assets"], assetName())
	} else if v, ok := raw["version"].(string); ok && v != "" {
		if !versionTagRE.MatchString(v) || strings.Contains(v, "..") {
			return nil, fmt.Errorf("版本清单 version 非法: %q", v)
		}
		info.Version = strings.TrimPrefix(v, "v")
		info.DownloadURL, _ = raw["downloadUrl"].(string)
		info.SHA256, _ = raw["sha256"].(string)
		info.Notes, _ = raw["notes"].(string)
	} else {
		return nil, fmt.Errorf("版本清单格式无法识别")
	}
	return info, nil
}

// extractAssetSHA256 从 GitHub Release assets 列表中提取目标文件的 digest（格式 "sha256:<hex>"）。
func extractAssetSHA256(assetsAny any, targetName string) string {
	assets, ok := assetsAny.([]any)
	if !ok {
		return ""
	}
	for _, item := range assets {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name != targetName {
			continue
		}
		if d, _ := m["digest"].(string); strings.HasPrefix(strings.ToLower(d), "sha256:") {
			return strings.TrimSpace(d[7:])
		}
	}
	return ""
}

func (s *Service) Check() (map[string]any, error) {
	info, err := s.fetchRelease()
	if err != nil {
		return nil, err
	}
	mode := "binary"
	if s.container != nil {
		mode = "container"
	}
	return map[string]any{
		"current": CurrentVersion(), "latest": info.Version, "updateAvailable": semverLess(CurrentVersion(), info.Version),
		"url": info.DownloadURL, "notes": info.Notes, "mode": mode,
	}, nil
}

func semverLess(cur, latest string) bool {
	if cur == latest || latest == "" {
		return false
	}
	if strings.HasPrefix(cur, "0.0.0") || strings.Contains(cur, "dev") {
		return true // dev 版本永远提示可更新
	}
	c := splitSemver(cur)
	l := splitSemver(latest)
	for i := 0; i < 4; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

// splitSemver 解析版本号为最多 4 段整数（x.y.z.w，面板 tag 为 4 段；
// 此前 SplitN 3 段导致第 4 位补丁号被丢弃，0.1.8.0 与 0.1.8.1 判等、检查更新永远"已是最新"）。
// 缺失段按 0 处理（"1.8" 等价 "1.8.0.0"）。
func splitSemver(v string) [4]int {
	v = strings.TrimPrefix(v, "v")
	var out [4]int
	for i, part := range strings.Split(v, ".") {
		if i > 3 {
			break
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// Apply 一键更新：下载新二进制 → 校验 → 原子替换自身 → 延迟重启进程。
func (s *Service) Apply() (string, error) {
	if !s.mu.TryLock() {
		return "", fmt.Errorf("更新已在进行中，请勿重复点击")
	}
	defer s.mu.Unlock()

	info, err := s.fetchRelease()
	if err != nil {
		return "", err
	}

	// compose 容器环境：走镜像式更新（固化 compose tag → 清理本地旧镜像 → 重建时由 compose 拉取），
	// 版本固化 + 旧缓存清理，down/up 之后不再回退。
	if s.container != nil {
		tag := info.Tag
		if tag == "" {
			tag = "v" + info.Version // 自定义清单无 tag 时按版本号拼镜像 tag
		}
		return s.container.apply(tag)
	}

	url := info.DownloadURL
	var repo string
	if url == "" {
		if info.Tag == "" {
			return "", fmt.Errorf("版本清单未提供下载地址，无法自更新；请在宿主机执行 ./update.sh")
		}
		repo = githubRepo(s.cfg.UpdateURL)
		if repo == "" {
			return "", fmt.Errorf("无法从更新清单 URL 识别 GitHub 仓库（%s）", s.cfg.UpdateURL)
		}
		url = fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, info.Tag, assetName())
	}
	data, err := download(url)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	if err := validateBinary(data); err != nil {
		return "", err
	}
	expectedHash := strings.ToLower(strings.TrimSpace(info.SHA256))
	if expectedHash == "" && repo != "" && info.Tag != "" {
		sumsURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS", repo, info.Tag)
		if sumsData, serr := download(sumsURL); serr == nil {
			expectedHash = parseSHA256Sums(string(sumsData), assetName())
		}
	}
	if expectedHash != "" {
		gotHash := strings.ToLower(pkg.SHA256Hex(string(data)))
		if gotHash != expectedHash {
			return "", fmt.Errorf("二进制 SHA-256 校验失败（期望 %s，实际 %s），已拒绝更新以防篡改", expectedHash, gotHash)
		}
	}
	execPath, err := replaceSelf(data)
	if err != nil {
		return "", err
	}
	slog.Info("面板更新：新二进制已就位，准备重启", "version", info.Tag, "size", len(data))
	go restartAfter(execPath, 2*time.Second)
	return fmt.Sprintf("新版本 %s（%.1f MB）已下载并替换，面板正在重启，页面将在数秒后自动恢复。",
		info.Tag, float64(len(data))/(1<<20)), nil
}

// assetName 当前平台对应的 Release 资产名（release.yml 产物命名一致）。
func assetName() string {
	name := fmt.Sprintf("zjpanel-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// githubRepo 从更新清单 URL 提取 owner/repo：
// https://api.github.com/repos/OWNER/REPO/releases/latest 或 https://github.com/OWNER/REPO/...。
func githubRepo(updateURL string) string {
	for _, marker := range []string{"api.github.com/repos/", "github.com/"} {
		if i := strings.Index(updateURL, marker); i >= 0 {
			rest := updateURL[i+len(marker):]
			parts := strings.Split(strings.Split(rest, "?")[0], "/")
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return ""
}

// download 拉取二进制；配置了 ZJPANEL_GH_MIRROR（ghproxy 类前缀，如 https://ghfast.top）时镜像优先，直链兜底。
func download(url string) ([]byte, error) {
	urls := []string{url}
	if m := strings.TrimRight(os.Getenv("ZJPANEL_GH_MIRROR"), "/"); m != "" {
		urls = []string{m + "/" + url, url}
	}
	var errs []error
	for _, u := range urls {
		data, err := httpGet(u)
		if err == nil {
			return data, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
	}
	return nil, errors.Join(errs...)
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "zjpanel-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

// validateBinary 基础校验：足够大 + 平台可执行格式魔数，防止把 404 页面/损坏文件当二进制替换。
func validateBinary(data []byte) error {
	if len(data) < 4<<20 {
		return fmt.Errorf("下载内容过小（%d KB），不是有效的面板二进制", len(data)/1024)
	}
	if bytes.HasPrefix(data, []byte("\x7fELF")) || bytes.HasPrefix(data, []byte("MZ")) {
		return nil // ELF（linux/darwin）或 PE（windows）
	}
	return fmt.Errorf("下载内容不是有效的可执行文件（缺少 ELF/PE 头）")
}

// parseSHA256Sums 从 SHA256SUMS 内容中提取指定资产的 64 位 hex 校验和。
func parseSHA256Sums(sumsContent, targetName string) string {
	for _, line := range strings.Split(sumsContent, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 {
			name := strings.TrimPrefix(fields[1], "*")
			if filepath.Base(name) == targetName && len(fields[0]) == 64 {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

// replaceSelf 原子替换当前可执行文件（参考 octopus）：旧文件改名 .old → 新文件写入原路径，失败回滚。
// Windows 允许改名运行中的 exe（映射句柄仍有效）；返回替换后的可执行路径（供重启）。
func replaceSelf(data []byte) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	perm := os.FileMode(0o755)
	if info, err := os.Stat(execPath); err == nil {
		perm = info.Mode().Perm()
	}
	tmpPath := execPath + ".new"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return "", fmt.Errorf("写入新二进制失败: %w", err)
	}
	oldPath := execPath + ".old"
	_ = os.Remove(oldPath) // 清理上次更新残留（Windows 下运行中删不掉，忽略）
	if err := os.Rename(execPath, oldPath); err != nil {
		return "", fmt.Errorf("备份旧二进制失败: %w", err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		_ = os.Rename(oldPath, execPath) // 回滚，保持可运行状态
		return "", fmt.Errorf("替换二进制失败（已回滚）: %w", err)
	}
	return execPath, nil
}

// restartAfter 延迟重启：先让 HTTP 响应送达，再替换进程镜像。
func restartAfter(execPath string, d time.Duration) {
	time.Sleep(d)
	slog.Info("面板更新：正在重启进程 ...")
	restartSelf(execPath)
}
