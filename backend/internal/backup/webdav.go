package backup

import (
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

func newWebDAV(cfg RemoteConfig) *webDAV {
	return &webDAV{
		cfg:  cfg,
		http: &http.Client{Timeout: 60 * time.Second},
	}
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
	req, err := http.NewRequest("PROPFIND", w.baseURL(""), nil)
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

func (w *webDAV) Upload(localPath, remoteName string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequest(http.MethodPut, w.baseURL(remoteName), f)
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("上传失败，状态 %d：%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (w *webDAV) Download(remoteName, localPath string) error {
	req, err := http.NewRequest(http.MethodGet, w.baseURL(remoteName), nil)
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
	_, err = io.Copy(out, resp.Body)
	return err
}

func (w *webDAV) Delete(remoteName string) error {
	req, err := http.NewRequest(http.MethodDelete, w.baseURL(remoteName), nil)
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
	req, err := http.NewRequest("PROPFIND", w.baseURL(""), nil)
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
	XMLName  xmlName    `xml:"multistatus"`
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
