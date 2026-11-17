package update

// 容器部署下的「镜像式」一键更新：
// 与二进制式更新（原地替换 /app/zjpanel，容器重建即回退）不同，本路径把新版本固化进镜像：
// 改写宿主机 compose 的 image → 清理本地同名 tag 残留镜像 → 重建容器时由 compose 自动拉取新镜像。
// down/up 之后仍是新版本；不主动 pull，拉取失败时 compose 保留旧容器，不会造成服务中断。
//
// 自身定位：compose 启动的容器带有 com.docker.compose.* 标签，
// 从 /proc/self/cgroup 取本容器 ID（host 网络下 hostname 是宿主机名，不可用）inspect 自身即可拿到
// 镜像名、compose 路径与工作目录。路径可见性依赖单容器部署的 /opt:/opt 挂载（容器内路径 = 宿主机路径）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"zjpanel/backend/internal/pkg"
)

const (
	removeTimeout   = 30 * time.Second // 清理残留镜像
	recreateTimeout = 8 * time.Minute  // 一次性容器：compose up 可能含拉取镜像，留足时间
	inspectTimeout  = 8 * time.Second
)

// containerUpdater 容器更新器；nil 表示当前不是可自更新的 compose 容器（走二进制路径）。
type containerUpdater struct {
	bin         string // docker 可执行文件路径
	imageRepo   string // 当前镜像仓库名（不含 tag）
	fullImage   string // 当前容器镜像完整 ref（带 tag），用于起一次性 compose 容器
	composeFile string // compose 文件在容器内可见的路径
	deployDir   string // ZJPANEL_DEPLOY_DIR（可选，优先用于定位 compose）
	labelFile   string // com.docker.compose.project.config_files
	labelDir    string // com.docker.compose.project.working_dir
}

type inspectInfo struct {
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// detectContainer 探测 compose 容器环境。任一步不满足即返回 nil，安全回退到二进制更新。
//
// 关键：不把容器检测绑死在"能取到自身容器 ID + inspect 成功"上。cgroup v2 隔离下
// /proc/self/cgroup 往往是 "0::/"，取不到 ID，曾导致镜像式逻辑从未激活、compose 长期停留 :latest。
// 这里 inspect 仅用于补充 compose 标签路径；拿不到时仍可靠 ZJPANEL_DEPLOY_DIR 或默认部署路径定位。
func detectContainer(cfg *pkg.Config) *containerUpdater {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil
	}
	if !dockerSocketReachable() {
		return nil
	}
	// 防误判：宿主机直跑（装了 docker + 恰好有 compose 文件）不应走镜像式更新。
	if !runningInContainer() {
		return nil
	}

	// 尽力从自身容器标签补 compose 路径（cgroup 取不到 ID 时跳过，不影响主流程）。
	var labelFile, labelDir string
	if id := selfContainerID(); id != "" {
		if out, err := runDocker(bin, inspectTimeout, "inspect", id, "--format", "{{json .}}"); err == nil {
			var info inspectInfo
			if json.Unmarshal(bytes.TrimSpace(out), &info) == nil {
				labelFile = firstPath(info.Config.Labels["com.docker.compose.project.config_files"])
				labelDir = info.Config.Labels["com.docker.compose.project.working_dir"]
			}
		}
	}

	u := &containerUpdater{
		bin:       bin,
		deployDir: strings.TrimSpace(cfg.DeployDir),
		labelFile: labelFile,
		labelDir:  labelDir,
	}
	composeFile, err := u.resolveComposeFile()
	if err != nil {
		return nil // 定位不到 compose，判定非 compose 部署，回退二进制更新
	}
	u.composeFile = composeFile
	u.fullImage = imageRefFromCompose(composeFile)
	u.imageRepo = stripTag(u.fullImage)
	if u.imageRepo == "" {
		return nil
	}
	return u
}

// 容器 ID 为 64 位小写十六进制，正则提取可兼容 cgroup v1/v2 各种挂载格式
// （docker-<id>.scope、/docker/<id>、containerd/cri 等）。
var containerIDRE = regexp.MustCompile(`[0-9a-f]{64}`)

// selfContainerID 从 cgroup 读取本容器 ID（hostname 在 host 网络模式下是宿主机名，不可用）。
// 依次尝试 /proc/self/cgroup 与 /proc/1/cgroup，兼容 cgroup v1/v2。
func selfContainerID() string {
	for _, p := range []string{"/proc/self/cgroup", "/proc/1/cgroup"} {
		if data, err := os.ReadFile(p); err == nil {
			if id := parseContainerID(string(data)); id != "" {
				return id
			}
		}
	}
	return ""
}

// parseContainerID 从 cgroup 内容中提取 64 位容器 ID；提取不到返回空。
func parseContainerID(cgroup string) string {
	return containerIDRE.FindString(cgroup)
}

// runningInContainer 判断是否确实运行在容器内。
// Docker 容器根目录必有 /.dockerenv；cgroup 提取到容器 ID 也可作为补充信号。
// 两者都无时按非容器处理，避免宿主机直跑（装了 docker + 有 compose 文件）被误判、
// 走镜像式更新而失败。
func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return selfContainerID() != ""
}

// imageRefFromCompose 读取 compose 中第一处 image 配置（含 tag），作为当前镜像 ref。
func imageRefFromCompose(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	if loc := imageLineRE.FindSubmatchIndex(data); loc != nil {
		return string(data[loc[4]:loc[5]])
	}
	return ""
}

// dockerSocketReachable 检查 docker daemon socket 是否存在（tcp 远程 DOCKER_HOST 跳过检查）。
func dockerSocketReachable() bool {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(host, "unix://") {
		return true
	}
	_, err := os.Stat(strings.TrimPrefix(host, "unix://"))
	return err == nil
}

func firstPath(list string) string {
	// config_files 可能是空格或冒号分隔的多路径，取第一个非空项。
	for _, p := range strings.FieldsFunc(list, func(r rune) bool { return r == ':' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			return p
		}
	}
	return ""
}

// stripTag 去掉镜像 tag；兼容带端口的私有仓库（host:5000/repo 中的冒号不能当 tag）。
func stripTag(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}

// resolveComposeFile 找容器内可见的宿主机 compose 文件。
// 顺序：显式配置 → compose 标签 → 默认部署路径。cgroup 拿不到标签时，后两者兜底。
func (u *containerUpdater) resolveComposeFile() (string, error) {
	names := []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	candidates := []string{}
	if u.deployDir != "" {
		for _, n := range names {
			candidates = append(candidates, filepath.Join(u.deployDir, n))
		}
	}
	if u.labelFile != "" {
		candidates = append(candidates, u.labelFile)
	}
	if u.labelDir != "" {
		for _, n := range names {
			candidates = append(candidates, filepath.Join(u.labelDir, n))
		}
	}
	// 默认部署路径兜底（compose 标签取不到时的最后手段；/opt:/opt 挂载下容器内可见）。
	for _, n := range names {
		candidates = append(candidates, filepath.Join("/opt/zjpanel", n))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("无法在容器内定位宿主机 compose 文件（已尝试 %d 个路径）；请在部署目录设置 ZJPANEL_DEPLOY_DIR 并把该目录挂载进容器", len(candidates))
}

// apply 执行镜像式更新：固化 compose 到新版本 → 清理本地旧镜像 → 延迟重建。
// tag 固定为具体版本，down/up 不再回退。新镜像交由 compose up 时按需拉取（本地已清理则必拉）。
func (u *containerUpdater) apply(versionTag string) (string, error) {
	newImage := u.imageRepo + ":" + versionTag
	// 清理前先解析当前镜像 ID 作为一次性容器载体：rmi -f 只 untag，容器仍在引用该镜像，
	// 用 ID 启动载体不受 tag 被清理的影响（见 carrierImageID）。
	carrier := u.carrierImageID()
	if carrier == "" {
		carrier = u.fullImage // 兜底：inspect 失败时用原 ref（docker run 会按需拉取）
	}
	composeFile, err := u.pinComposeImage(newImage)
	if err != nil {
		return "", err
	}
	// 用一次性容器执行 compose up -d：当前容器会被重建停掉，由它自己的子进程执行会被连带 SIGTERM，
	// 导致新容器可能没起来；一次性容器独立于被重建的容器，daemon 侧完成重建后才退出。
	go recreateAfter(u.bin, composeFile, carrier, 3*time.Second)
	return fmt.Sprintf("compose 已固定为 %s，已清理本地旧镜像，正在重建容器并拉取新镜像，页面将在数十秒后自动恢复。", newImage), nil
}

// pinComposeImage 把 compose 的 image 固化为 targetImage（留 .bak），并清理本地同名旧镜像缓存。
// 这是"down/up 不回退"的关键：compose 固定到具体版本 tag + 本地旧镜像被清理，up 时必拉新镜像。
func (u *containerUpdater) pinComposeImage(targetImage string) (string, error) {
	composeFile, err := u.resolveComposeFile()
	if err != nil {
		return "", err
	}
	if err := rewriteComposeImage(composeFile, u.imageRepo, targetImage); err != nil {
		return "", err
	}
	u.pruneStaleImages(targetImage)
	return composeFile, nil
}

// pruneStaleImages 清理本地同名旧镜像（latest 缓存 + 当前引用的旧 tag），让 compose up 必重新拉取。
// 用 rmi -f 仅解除 tag，不影响正在运行的容器；保留 keep（目标新镜像）不动。
func (u *containerUpdater) pruneStaleImages(keep string) {
	stale := map[string]bool{u.imageRepo + ":latest": true} // 核心：清掉 latest 缓存，解决 down/up 回退
	if u.fullImage != "" {
		stale[u.fullImage] = true // 当前 compose 引用的旧 tag
	}
	for img := range stale {
		if img == keep || img == "" {
			continue
		}
		out, err := runDocker(u.bin, removeTimeout, "rmi", "-f", img)
		if err != nil {
			slog.Debug("面板更新：清理旧镜像跳过", "image", img, "detail", tail(out, 200))
		} else {
			slog.Info("面板更新：已清理本地旧镜像", "image", img)
		}
	}
}

// carrierImageID 返回当前镜像的 ID（sha256:...），用于一次性容器载体。
// 用 ID 而非 tag：清理旧 tag 后 ID 仍被运行中的容器引用、可安全 docker run。
func (u *containerUpdater) carrierImageID() string {
	if u.fullImage == "" {
		return ""
	}
	out, err := runDocker(u.bin, inspectTimeout, "image", "inspect", u.fullImage, "--format", "{{.Id}}")
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(out))
	if strings.HasPrefix(id, "sha256:") {
		return id
	}
	return ""
}

var imageLineRE = regexp.MustCompile(`(?m)^([ \t]*image:[ \t]*)([^\s#]+)`)

// rewriteComposeImage 把 compose 中本面板的 image 行改成 newImage，保留文件权限并留 .bak。
// 按 repo 精确匹配镜像行，避免多 service 的 compose 改错行。
func rewriteComposeImage(file, repo, newImage string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("读取 compose 文件失败: %w", err)
	}
	var hit []int
	for _, loc := range imageLineRE.FindAllSubmatchIndex(data, -1) {
		if strings.HasPrefix(string(data[loc[4]:loc[5]]), repo) {
			hit = loc
			break
		}
	}
	if hit == nil {
		return fmt.Errorf("compose 中未找到镜像 %s 对应的 image 配置，拒绝改写", repo)
	}
	var buf bytes.Buffer
	buf.Write(data[:hit[4]])
	buf.WriteString(newImage)
	buf.Write(data[hit[5]:])

	// 已是目标镜像时不写文件、不刷新 .bak，保证启动自愈可反复执行且幂等。
	if bytes.Equal(buf.Bytes(), data) {
		return nil
	}

	if err := os.WriteFile(file+".bak", data, 0o600); err != nil {
		return fmt.Errorf("备份 compose 文件失败: %w", err)
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(file); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(file, buf.Bytes(), mode); err != nil {
		_ = os.WriteFile(file, data, mode) // 回滚
		return fmt.Errorf("写入 compose 文件失败（已回滚）: %w", err)
	}
	return nil
}

// recreateAfter 延迟重建：先让 HTTP 响应送达，再用一次性容器执行 compose up -d。
// 一次性容器挂载 docker.sock 与 compose 所在目录，独立于将被重建的面板容器，
// 避免面板容器被停止时连带 SIGTERM 自己的 compose 子进程导致新容器没起来。
// carrierImage 为当前镜像（本地已存在，含 docker CLI），compose up 会按新 tag 拉取并启动。
func recreateAfter(bin, composeFile, carrierImage string, d time.Duration) {
	time.Sleep(d)
	projectDir := filepath.Dir(composeFile)
	slog.Info("面板更新：正在通过一次性容器重建 ...", "compose", composeFile, "carrier", carrierImage)
	args := []string{
		"run", "--rm",
		"--entrypoint", "docker",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", projectDir + ":" + projectDir,
		"-w", projectDir,
		carrierImage,
		"compose", "-f", composeFile, "up", "-d", "--remove-orphans",
	}
	out, err := runDocker(bin, recreateTimeout, args...)
	if err != nil {
		slog.Error("面板更新：重建容器失败", "err", err, "output", tail(out, 600))
		return
	}
	slog.Info("面板更新：容器已重建", "output", tail(out, 600))
}

func runDocker(bin string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ() // 继承 DOCKER_HOST 等
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func tail(out []byte, n int) string {
	s := strings.TrimSpace(string(out))
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
