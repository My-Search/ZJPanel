package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// GitHub 插件安装：把仓库 / Release 的 zip 下载下来交给 InstallZip。
// 只允许 GitHub 域名，避免面板被当作 SSRF 跳板访问内网。

const maxGitHubZip = 32 << 20 // 与上传 zip 的上限一致

var repoShorthand = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// githubZipHost 判断是否 GitHub 的下载域名（含 Release 资源重定向域名）。
func githubZipHost(host string) bool {
	host = strings.ToLower(host)
	switch host {
	case "github.com", "www.github.com", "codeload.github.com", "raw.githubusercontent.com", "api.github.com":
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com")
}

// NormalizeGitHubURL 把用户输入规范成可直接下载的 zip 地址。
// 支持：owner/repo、https://github.com/owner/repo、仓库归档 zip、Release zip 直链。
// 仓库根目录形式统一转成 /archive/HEAD.zip（HEAD 即默认分支）。
func NormalizeGitHubURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("请填写 GitHub 仓库地址")
	}
	if !strings.Contains(s, "://") && repoShorthand.MatchString(s) {
		s = "https://github.com/" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("GitHub 地址格式不正确")
	}
	if u.Scheme != "https" {
		return "", errors.New("仅支持 https 地址")
	}
	host := strings.ToLower(u.Hostname())
	if !githubZipHost(host) {
		return "", errors.New("仅支持 GitHub 域名（github.com / codeload.github.com 等）")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("地址不要带查询参数或锚点")
	}
	if host == "github.com" || host == "www.github.com" {
		path := strings.Trim(u.Path, "/")
		parts := strings.Split(path, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			repo := strings.TrimSuffix(parts[1], ".git")
			return fmt.Sprintf("https://github.com/%s/%s/archive/HEAD.zip", parts[0], repo), nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".zip") {
			// Release 资产 / archive 归档直链
			return "https://github.com/" + path, nil
		}
		return "", errors.New("请填仓库地址（owner/repo）或 zip 直链；插件文件需在仓库根目录")
	}
	if !strings.HasSuffix(strings.ToLower(u.Path), ".zip") {
		// codeload 的归档地址通常是 /owner/repo/zip/<ref>，没有 .zip 后缀
		if !(host == "codeload.github.com" && strings.Contains(u.Path, "/zip/")) {
			return "", errors.New("该地址不是 zip 文件")
		}
	}
	return u.String(), nil
}

// DownloadGitHubZip 下载并校验 zip（限制 32MB、仅跟随 GitHub 域名重定向）。
func DownloadGitHubZip(raw string) ([]byte, string, error) {
	target, err := NormalizeGitHubURL(raw)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			if !githubZipHost(req.URL.Hostname()) {
				return fmt.Errorf("拒绝重定向到非 GitHub 域名: %s", req.URL.Host)
			}
			return nil
		},
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ZJPanel-PluginInstaller")
	req.Header.Set("Accept", "application/zip, application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("下载失败：HTTP %d（私有仓库或仓库不存在，请改用 zip 上传）", resp.StatusCode)
	}
	if resp.ContentLength > maxGitHubZip {
		return nil, "", fmt.Errorf("zip 超过 32MB 上限（实际 %dMB）", resp.ContentLength>>20)
	}
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, io.LimitReader(resp.Body, maxGitHubZip+1))
	if err != nil {
		return nil, "", fmt.Errorf("下载中断: %w", err)
	}
	if n > maxGitHubZip {
		return nil, "", errors.New("zip 超过 32MB 上限")
	}
	data := buf.Bytes()
	// GitHub 对不存在/私有仓库会返回 200 的 HTML 登录页，先看 zip 魔数再进解析。
	if len(data) < 4 || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return nil, "", errors.New("下载内容不是 zip（仓库需公开，私有仓库请改用 zip 上传）")
	}
	return data, target, nil
}
