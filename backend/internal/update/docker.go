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
func detectContainer(cfg *pkg.Config) *containerUpdater {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil
	}
	if !dockerSocketReachable() {
		return nil
	}
	id := selfContainerID()
	if id == "" {
		return nil
	}
	out, err := runDocker(bin, inspectTimeout, "inspect", id, "--format", "{{json .}}")
	if err != nil {
		return nil
	}
	var info inspectInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return nil
	}
	labels := info.Config.Labels
	if labels["com.docker.compose.project.working_dir"] == "" && labels["com.docker.compose.project.config_files"] == "" {
		return nil // 非 compose 容器（手动 docker run），无可靠 compose 路径，不接管更新
	}
	repo := stripTag(info.Config.Image)
	if repo == "" {
		return nil
	}
	return &containerUpdater{
		bin:       bin,
		imageRepo: repo,
		fullImage: info.Config.Image,
		deployDir: strings.TrimSpace(cfg.DeployDir),
		labelFile: firstPath(labels["com.docker.compose.project.config_files"]),
		labelDir:  labels["com.docker.compose.project.working_dir"],
	}
}

// selfContainerID 从 /proc/self/cgroup 读取本容器 ID（hostname 在 host 网络模式下是宿主机名，不可用）。
func selfContainerID() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroup v1: 0::/system.slice/docker-<64hex>.scope 或 /docker/<64hex>
		// cgroup v2: 0::/system.slice/docker-<64hex>.scope
		if i := strings.Index(line, "docker-"); i >= 0 {
			id := line[i+len("docker-"):]
			if j := strings.IndexAny(id, ".\n/"); j > 0 && len(id[:j]) >= 12 {
				return id[:j]
			}
		}
		if i := strings.LastIndex(line, "/docker/"); i >= 0 {
			id := strings.TrimSpace(line[i+len("/docker/"):])
			if len(id) >= 12 {
				return id
			}
		}
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

// resolveComposeFile 按「显式配置 → compose 标签 → 工作目录」顺序找容器内可见的 compose 文件。
func (u *containerUpdater) resolveComposeFile() (string, error) {
	candidates := []string{}
	if u.deployDir != "" {
		candidates = append(candidates,
			filepath.Join(u.deployDir, "docker-compose.yml"),
			filepath.Join(u.deployDir, "compose.yml"))
	}
	if u.labelFile != "" {
		candidates = append(candidates, u.labelFile)
	}
	if u.labelDir != "" {
		candidates = append(candidates,
			filepath.Join(u.labelDir, "docker-compose.yml"),
			filepath.Join(u.labelDir, "compose.yml"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("无法在容器内定位宿主机 compose 文件（已尝试 %d 个路径）；请在部署目录设置 ZJPANEL_DEPLOY_DIR 并把该目录挂载进容器", len(candidates))
}

// apply 执行镜像式更新：改写 compose → 清理同名 tag 残留镜像 → 延迟重建。
// tag 固定为具体版本，down/up 不再回退。新镜像交由 compose up 时按需拉取（本地已清理则必拉）。
func (u *containerUpdater) apply(versionTag string) (string, error) {
	composeFile, err := u.resolveComposeFile()
	if err != nil {
		return "", err
	}
	newImage := u.imageRepo + ":" + versionTag
	if err := rewriteComposeImage(composeFile, u.imageRepo, newImage); err != nil {
		return "", err
	}
	// 清理本地同名 tag 镜像：若之前残留旧内容，确保 compose up 时重新拉取而非复用缓存。
	// 镜像不存在属正常情况（rmi 返回非零），忽略输出。
	_, _ = runDocker(u.bin, removeTimeout, "rmi", newImage)
	// 用一次性容器执行 compose up -d：当前容器会被重建停掉，由它自己的子进程执行会被连带 SIGTERM，
	// 导致新容器可能没起来；一次性容器独立于被重建的容器，daemon 侧完成重建后才退出。
	// 载体用当前镜像（已存在于本地），compose up 会以新 tag 拉取并启动。
	go recreateAfter(u.bin, composeFile, u.fullImage, 3*time.Second)
	return fmt.Sprintf("compose 已更新为 %s，正在重建容器并拉取新镜像，页面将在数十秒后自动恢复。", newImage), nil
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
