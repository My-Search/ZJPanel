package backup

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// webDAV 通用 WebDAV 客户端（Basic Auth），兼容 Nextcloud / 群晖 / 阿里云盘 WebDAV 等。
type webDAV struct {
	cfg  RemoteConfig
	http *http.Client
}

// 客户端不设总超时：http.Client.Timeout 覆盖整个请求含上传 body，
// 大备份包单次 PUT 会被 60s 总超时掐断。改用每请求 context 限时。
const (
	webdavTransferTimeout = 30 * time.Minute // 上传/下载大包
	webdavMetaTimeout     = 60 * time.Second // PROPFIND/DELETE 等元数据操作
)

func newWebDAV(cfg RemoteConfig) *webDAV {
	return &webDAV{
		cfg:  cfg,
		http: &http.Client{},
	}
}

// withTimeout 给请求挂上独立的超时上下文。
func withTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (w *webDAV) baseURL(name string) string {
	base := strings.TrimRight(w.cfg.URL, "/")
	dir := strings.Trim(w.cfg.RemoteDir, "/")
	if dir != "" {
		base = base + "/" + dir
	}
	if name == "" {
		return base + "/"
	}
	return base + "/" + name
}

func (w *webDAV) auth(req *http.Request) {
	if w.cfg.Username != "" {
		req.SetBasicAuth(w.cfg.Username, w.cfg.Password)
	}
}

func (w *webDAV) Test() error {
	// PROPFIND 列目录：验证地址与凭据。
	ctx, cancel := withTimeout(webdavMetaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", w.baseURL(""), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	req.Header.Set("Depth", "0")
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("WebDAV 返回状态 %d", resp.StatusCode)
	}
	return nil
}

func (w *webDAV) Upload(localPath, remoteName string, step func(string)) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, cancel := withTimeout(webdavTransferTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, w.baseURL(remoteName), f)
	if err != nil {
		return err
	}
	w.auth(req)
	if st, err := f.Stat(); err == nil {
		emitStep(step, "WebDAV 整包上传 %s…", humanBytes(st.Size()))
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("上传失败，状态 %d：%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	emitStep(step, "WebDAV 上传完成")
	return nil
}

func (w *webDAV) Download(remoteName, localPath string, step func(string)) error {
	ctx, cancel := withTimeout(webdavTransferTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.baseURL(remoteName), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("下载失败，状态 %d", resp.StatusCode)
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	emitStep(step, "WebDAV 下载完成 %s", humanBytes(n))
	return nil
}

func (w *webDAV) Delete(remoteName string) error {
	ctx, cancel := withTimeout(webdavMetaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, w.baseURL(remoteName), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return fmt.Errorf("删除失败，状态 %d", resp.StatusCode)
	}
	return nil
}

func (w *webDAV) List() ([]string, error) {
	ctx, cancel := withTimeout(webdavMetaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", w.baseURL(""), nil)
	if err != nil {
		return nil, err
	}
	w.auth(req)
	req.Header.Set("Depth", "1")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("列举失败，状态 %d", resp.StatusCode)
	}
	var multistat propfindResp
	if err := xml.NewDecoder(resp.Body).Decode(&multistat); err != nil {
		return nil, err
	}
	var names []string
	for _, item := range multistat.Responses {
		if len(item.Hrefs) == 0 {
			continue
		}
		name := path.Base(strings.TrimSuffix(item.Hrefs[0], "/"))
		if name == "" || name == "." {
			continue
		}
		if strings.HasSuffix(name, ".tar.gz") {
			names = append(names, name)
		}
	}
	return names, nil
}

// PROPFIND 响应解析（仅取 href 与是否集合）。
type propfindResp struct {
	XMLName   xmlName        `xml:"multistatus"`
	Responses []propResponse `xml:"response"`
}

type xmlName struct{ Local string }

type propResponse struct {
	Hrefs    []string `xml:"href"`
	Propstat []struct {
		Prop struct {
			Resourcetype struct {
				Collection struct{} `xml:"collection"`
			} `xml:"resourcetype"`
		} `xml:"prop"`
	} `xml:"propstat"`
}
