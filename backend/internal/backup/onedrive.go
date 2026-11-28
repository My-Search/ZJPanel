package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// onedrive 微软 OneDrive 通道（Microsoft Graph v1.0，委托权限 Files.ReadWrite）。
// 授权与令牌刷新见 oauth.go。
type onedrive struct {
	cfg      RemoteConfig
	client   *http.Client
	token    string
	folderOK bool
}

func newOneDrive(cfg RemoteConfig) *onedrive {
	return &onedrive{cfg: cfg, client: &http.Client{Timeout: 30 * time.Minute}}
}

const graphBase = "https://graph.microsoft.com/v1.0"

func (o *onedrive) ensureToken() error {
	if o.token != "" {
		return nil
	}
	tok, err := fetchAccessToken(o.cfg)
	if err != nil {
		return err
	}
	o.token = tok
	return nil
}

// call 统一 Graph 请求；401 刷新一次令牌后重试。
func (o *onedrive) call(method, u string, body []byte, contentType string, out any) (int, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := o.ensureToken(); err != nil {
			return 0, nil, err
		}
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, u, r)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+o.token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := o.client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			o.token = ""
			continue
		}
		if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
			_ = json.Unmarshal(raw, out)
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, raw, fmt.Errorf("OneDrive 返回状态 %d: %s", resp.StatusCode, snippet(raw))
		}
		return resp.StatusCode, raw, nil
	}
	return 0, nil, fmt.Errorf("OneDrive 授权失效，请重新授权")
}

// pathEscape 逐段转义路径（保留 / 分隔）。
func pathEscape(segments []string) string {
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		parts = append(parts, url.PathEscape(s))
	}
	return strings.Join(parts, "/")
}

type odItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
}

// ensureFolder 逐级创建（或复用）远程目录。
func (o *onedrive) ensureFolder() ([]string, error) {
	segs := dirSegments(o.cfg.RemoteDir, "ZJPanel")
	if o.folderOK {
		return segs, nil
	}
	for i := range segs {
		p := pathEscape(segs[:i+1])
		var item odItem
		status, _, err := o.call(http.MethodGet, graphBase+"/me/drive/root:/"+p, nil, "", &item)
		if err == nil {
			continue
		}
		if status != http.StatusNotFound {
			return nil, err
		}
		// 不存在 → 在父目录下创建
		var body []byte
		if i == 0 {
			body, _ = json.Marshal(map[string]any{"name": segs[0], "folder": map[string]any{},
				"@microsoft.graph.conflictBehavior": "fail"})
			if _, _, err := o.call(http.MethodPost, graphBase+"/me/drive/root/children", body, "application/json", nil); err != nil {
				return nil, fmt.Errorf("创建远程目录失败 %s: %w", segs[0], err)
			}
		} else {
			parent := pathEscape(segs[:i])
			body, _ = json.Marshal(map[string]any{"name": segs[i], "folder": map[string]any{},
				"@microsoft.graph.conflictBehavior": "fail"})
			if _, _, err := o.call(http.MethodPost, graphBase+"/me/drive/root:/"+parent+":/children", body, "application/json", nil); err != nil {
				return nil, fmt.Errorf("创建远程目录失败 %s: %w", segs[i], err)
			}
		}
	}
	o.folderOK = true
	return segs, nil
}

// findItem 在远程目录内按名称查找文件。
func (o *onedrive) findItem(name string) (*odItem, error) {
	segs, err := o.ensureFolder()
	if err != nil {
		return nil, err
	}
	// 直接按路径取元数据，命中率最高
	var item odItem
	status, _, gerr := o.call(http.MethodGet, graphBase+"/me/drive/root:/"+pathEscape(append(segs, name)), nil, "", &item)
	if gerr != nil {
		if status == http.StatusNotFound {
			return nil, fmt.Errorf("远程不存在该文件: %s", name)
		}
		return nil, gerr
	}
	return &item, nil
}

func (o *onedrive) Test() error {
	var drive struct {
		ID        string `json:"id"`
		DriveType string `json:"driveType"`
	}
	if _, _, err := o.call(http.MethodGet, graphBase+"/me/drive?$select=id,driveType", nil, "", &drive); err != nil {
		return err
	}
	_, err := o.ensureFolder()
	return err
}

// Upload 用上传会话分片写入（Graph 简单上传限制 250MB，分片可支持大包）。
// 分片大小必须是 320KiB 的整数倍。
func (o *onedrive) Upload(localPath, remoteName string, step func(string)) error {
	segs, err := o.ensureFolder()
	if err != nil {
		return err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	full := pathEscape(append(segs, remoteName))
	body, _ := json.Marshal(map[string]any{
		"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace", "name": remoteName},
	})
	var sess struct {
		UploadURL string `json:"uploadUrl"`
	}
	if _, _, err := o.call(http.MethodPost, graphBase+"/me/drive/root:/"+full+":/createUploadSession", body, "application/json", &sess); err != nil {
		return err
	}
	if sess.UploadURL == "" {
		return fmt.Errorf("创建上传会话失败：缺少 uploadUrl")
	}
	const chunkSize = 10 * 1024 * 1024 // 10MiB，320KiB 整数倍
	buf := make([]byte, chunkSize)
	totalChunks := (st.Size() + chunkSize - 1) / chunkSize
	for offset := int64(0); offset < st.Size(); offset += chunkSize {
		n, rerr := io.ReadFull(f, buf)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			return rerr
		}
		if n == 0 {
			break
		}
		emitStep(step, "OneDrive 上传分片 %d/%d（%s/%s）", offset/chunkSize+1, totalChunks, humanBytes(offset+int64(n)), humanBytes(st.Size()))
		req, err := http.NewRequest(http.MethodPut, sess.UploadURL, bytes.NewReader(buf[:n]))
		if err != nil {
			return err
		}
		req.ContentLength = int64(n)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(n)-1, st.Size()))
		resp, err := o.client.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		// 中间分片返回 202，最后一片返回 200/201
		if resp.StatusCode >= 300 {
			return fmt.Errorf("分片上传失败（状态 %d）: %s", resp.StatusCode, snippet(raw))
		}
	}
	return nil
}

func (o *onedrive) Download(remoteName, localPath string, step func(string)) error {
	item, err := o.findItem(remoteName)
	if err != nil {
		return err
	}
	if err := o.ensureToken(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, graphBase+"/me/drive/items/"+url.PathEscape(item.ID)+"/content", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	// 默认跟随 302 到临时下载地址
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("下载失败（状态 %d）: %s", resp.StatusCode, snippet(raw))
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := copyWithProgress(out, resp.Body, step, "OneDrive 下载")
	if err == nil {
		emitStep(step, "OneDrive 下载完成 %s", humanBytes(n))
	}
	return err
}

func (o *onedrive) Delete(remoteName string) error {
	item, err := o.findItem(remoteName)
	if err != nil {
		return err
	}
	_, _, err = o.call(http.MethodDelete, graphBase+"/me/drive/items/"+url.PathEscape(item.ID), nil, "", nil)
	return err
}

func (o *onedrive) List() ([]string, error) {
	segs, err := o.ensureFolder()
	if err != nil {
		return nil, err
	}
	var list struct {
		Value []odItem `json:"value"`
	}
	u := graphBase + "/me/drive/root:/" + pathEscape(segs) + ":/children?$select=id,name,size&$top=200"
	if _, _, err := o.call(http.MethodGet, u, nil, "", &list); err != nil {
		return nil, err
	}
	var names []string
	for _, it := range list.Value {
		if strings.HasSuffix(it.Name, ".tar.gz") {
			names = append(names, it.Name)
		}
	}
	return names, nil
}
