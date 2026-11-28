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

// gdrive Google Drive 通道（Drive v3 REST，drive.file 作用域：只能访问本应用创建的文件）。
// 授权与令牌刷新见 oauth.go。
type gdrive struct {
	cfg      RemoteConfig
	client   *http.Client
	token    string
	folderID string
}

func newGDrive(cfg RemoteConfig) *gdrive {
	return &gdrive{cfg: cfg, client: &http.Client{Timeout: 30 * time.Minute}}
}

// token 首次调用时用刷新令牌换访问令牌。
func (g *gdrive) ensureToken() error {
	if g.token != "" {
		return nil
	}
	tok, err := fetchAccessToken(g.cfg)
	if err != nil {
		return err
	}
	g.token = tok
	return nil
}

// call 统一请求：带 Bearer；401 时刷新一次令牌后重试。
func (g *gdrive) call(method, u string, body []byte, contentType string, out any) (int, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := g.ensureToken(); err != nil {
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
		req.Header.Set("Authorization", "Bearer "+g.token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := g.client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			g.token = ""
			continue
		}
		if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
			_ = json.Unmarshal(raw, out)
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, raw, fmt.Errorf("Google Drive 返回状态 %d: %s", resp.StatusCode, snippet(raw))
		}
		return resp.StatusCode, raw, nil
	}
	return 0, nil, fmt.Errorf("Google Drive 授权失效，请重新授权")
}

// escapeQuery 转义 Drive 查询语法中的单引号。
func escapeQuery(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}

type gdFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size string `json:"size"`
}

// ensureFolder 逐级创建（或复用）远程目录，返回末级目录 ID。
func (g *gdrive) ensureFolder() (string, error) {
	if g.folderID != "" {
		return g.folderID, nil
	}
	parent := "root"
	for _, seg := range dirSegments(g.cfg.RemoteDir, "ZJPanel") {
		q := fmt.Sprintf("name = '%s' and mimeType = 'application/vnd.google-apps.folder' and '%s' in parents and trashed = false",
			escapeQuery(seg), parent)
		u := "https://www.googleapis.com/drive/v3/files?pageSize=10&fields=" + url.QueryEscape("files(id,name)") + "&q=" + url.QueryEscape(q)
		var list struct {
			Files []gdFile `json:"files"`
		}
		if _, _, err := g.call(http.MethodGet, u, nil, "", &list); err != nil {
			return "", err
		}
		if len(list.Files) > 0 {
			parent = list.Files[0].ID
			continue
		}
		meta, _ := json.Marshal(map[string]any{
			"name":     seg,
			"mimeType": "application/vnd.google-apps.folder",
			"parents":  []string{parent},
		})
		var created gdFile
		u = "https://www.googleapis.com/drive/v3/files?fields=id"
		if _, _, err := g.call(http.MethodPost, u, meta, "application/json; charset=UTF-8", &created); err != nil {
			return "", err
		}
		if created.ID == "" {
			return "", fmt.Errorf("创建远程目录失败: %s", seg)
		}
		parent = created.ID
	}
	g.folderID = parent
	return parent, nil
}

// findFile 在远程目录内按名称查找备份包。
func (g *gdrive) findFile(name string) (*gdFile, error) {
	fid, err := g.ensureFolder()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("name = '%s' and '%s' in parents and trashed = false", escapeQuery(name), fid)
	u := "https://www.googleapis.com/drive/v3/files?pageSize=10&fields=" + url.QueryEscape("files(id,name,size)") + "&q=" + url.QueryEscape(q)
	var list struct {
		Files []gdFile `json:"files"`
	}
	if _, _, err := g.call(http.MethodGet, u, nil, "", &list); err != nil {
		return nil, err
	}
	for _, f := range list.Files {
		if f.Name == name {
			return &f, nil
		}
	}
	return nil, fmt.Errorf("远程不存在该文件: %s", name)
}

func (g *gdrive) Test() error {
	// drive.file 作用域下 about 的 user 字段可能不返回，改用最小的 files 查询验证令牌。
	var list struct {
		Files []gdFile `json:"files"`
	}
	u := "https://www.googleapis.com/drive/v3/files?pageSize=1&fields=" + url.QueryEscape("files(id,name)")
	if _, _, err := g.call(http.MethodGet, u, nil, "", &list); err != nil {
		return err
	}
	// 顺带验证能否创建目录（drive.file 作用域下自建目录可写）
	if _, err := g.ensureFolder(); err != nil {
		return err
	}
	return nil
}

func (g *gdrive) Upload(localPath, remoteName string, step func(string)) error {
	fid, err := g.ensureFolder()
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
	// 先建可续传会话，再一次性 PUT 整个内容（Drive 支持单次写入任意大小）。
	meta, _ := json.Marshal(map[string]any{"name": remoteName, "parents": []string{fid}})
	for attempt := 0; attempt < 2; attempt++ {
		if err := g.ensureToken(); err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodPost,
			"https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id", bytes.NewReader(meta))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+g.token)
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		req.Header.Set("X-Upload-Content-Type", "application/gzip")
		req.Header.Set("X-Upload-Content-Length", fmt.Sprintf("%d", st.Size()))
		resp, err := g.client.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			g.token = ""
			continue
		}
		if resp.StatusCode >= 300 {
			return fmt.Errorf("创建上传会话失败（状态 %d）: %s", resp.StatusCode, snippet(raw))
		}
		location := resp.Header.Get("Location")
		if location == "" {
			return fmt.Errorf("上传会话缺少 Location")
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		// 分片续传：单请求整包在超大备份上会被限流/中断，按 16MiB 切片（256KiB 整数倍）。
		const chunkSize = 16 * 1024 * 1024
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
			emitStep(step, "Drive 上传分片 %d/%d（%s/%s）", offset/chunkSize+1, totalChunks, humanBytes(offset+int64(n)), humanBytes(st.Size()))
			put, err := http.NewRequest(http.MethodPut, location, bytes.NewReader(buf[:n]))
			if err != nil {
				return err
			}
			put.ContentLength = int64(n)
			put.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(n)-1, st.Size()))
			put.Header.Set("Content-Type", "application/gzip")
			up, err := g.client.Do(put)
			if err != nil {
				return err
			}
			upRaw, _ := io.ReadAll(io.LimitReader(up.Body, 1<<16))
			up.Body.Close()
			// 308：还有后续分片；200/201：最后一片写入完成。
			if up.StatusCode == 308 {
				continue
			}
			if up.StatusCode >= 300 {
				return fmt.Errorf("上传失败（状态 %d）: %s", up.StatusCode, snippet(upRaw))
			}
			if offset+int64(n) >= st.Size() {
				return nil
			}
		}
		return nil
	}
	return fmt.Errorf("上传失败：授权失效")
}

func (g *gdrive) Download(remoteName, localPath string, step func(string)) error {
	f, err := g.findFile(remoteName)
	if err != nil {
		return err
	}
	if err := g.ensureToken(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(f.ID)+"?alt=media", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	resp, err := g.client.Do(req)
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
	n, err := copyWithProgress(out, resp.Body, step, "Drive 下载")
	if err == nil {
		emitStep(step, "Drive 下载完成 %s", humanBytes(n))
	}
	return err
}

// copyWithProgress 每 64MiB 上报一次下载进度（step 可为 nil）。
func copyWithProgress(w io.Writer, r io.Reader, step func(string), label string) (int64, error) {
	if step == nil {
		return io.Copy(w, r)
	}
	var done int64
	const reportEvery = int64(64 << 20)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if done/reportEvery > (done-int64(n))/reportEvery {
				emitStep(step, "%s 已拉取 %s…", label, humanBytes(done))
			}
		}
		if rerr == io.EOF {
			return done, nil
		}
		if rerr != nil {
			return done, rerr
		}
	}
}

func (g *gdrive) Delete(remoteName string) error {
	f, err := g.findFile(remoteName)
	if err != nil {
		return err
	}
	_, _, err = g.call(http.MethodDelete, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(f.ID), nil, "", nil)
	return err
}

func (g *gdrive) List() ([]string, error) {
	fid, err := g.ensureFolder()
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("'%s' in parents and trashed = false and name contains 'zjpanel-backup'", fid)
	u := "https://www.googleapis.com/drive/v3/files?pageSize=200&fields=" + url.QueryEscape("files(id,name,size)") + "&q=" + url.QueryEscape(q)
	var list struct {
		Files []gdFile `json:"files"`
	}
	if _, _, err := g.call(http.MethodGet, u, nil, "", &list); err != nil {
		return nil, err
	}
	var names []string
	for _, f := range list.Files {
		if strings.HasSuffix(f.Name, ".tar.gz") {
			names = append(names, f.Name)
		}
	}
	return names, nil
}

// snippet 截断错误响应体，避免把整页 HTML 回给前端。
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
