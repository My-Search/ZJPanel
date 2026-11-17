package sftp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// Entry 文件列表条目。
type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Perm    string    `json:"perm"`    // 八进制权限，如 "0644"
	Mode    string    `json:"mode"`    // 如 "drwxr-xr-x"
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir"`
}

// List 列出目录（目录在前，其余按名称排序）。
func List(c *sftp.Client, dir string) ([]Entry, error) {
	fis, err := c.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(fis))
	for _, fi := range fis {
		out = append(out, Entry{
			Name:    fi.Name(),
			Size:    fi.Size(),
			Perm:    fmt.Sprintf("%04o", uint32(fi.Mode().Perm())),
			Mode:    fi.Mode().String(),
			ModTime: fi.ModTime(),
			IsDir:   fi.IsDir(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Read 文本读取：上限 2MB（超出截断并标记）；含 NUL 字节判定为二进制文件不可编辑。
const (
	maxReadBytes = 2 << 20
	binaryProbe  = 8 << 10
)

type ReadResult struct {
	Binary    bool   `json:"binary"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

func Read(c *sftp.Client, p string) (*ReadResult, error) {
	f, err := c.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	res := &ReadResult{Size: st.Size()}
	buf := make([]byte, 0, maxReadBytes+1)
	chunk := make([]byte, 32<<10)
	for int64(len(buf)) < maxReadBytes {
		n, rerr := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if rerr != nil {
			break // EOF 或读错误：以已读内容为准
		}
	}
	// 二进制探测只看前 8KB；注意不能截断 buf 本身，否则文本内容也被削到 8KB
	probe := buf
	if len(probe) > binaryProbe {
		probe = probe[:binaryProbe]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		res.Binary = true
		return res, nil
	}
	res.Content = string(buf)
	res.Truncated = res.Size > int64(len(res.Content))
	return res, nil
}

// Write 写入文本文件（自动补齐父目录，整体覆盖）。
func Write(c *sftp.Client, p, content string) error {
	if dir := path.Dir(p); dir != "" && dir != "." {
		_ = c.MkdirAll(dir)
	}
	f, err := c.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, content)
	return err
}

func Mkdir(c *sftp.Client, p string) error { return c.MkdirAll(p) }

func Touch(c *sftp.Client, p string) error {
	f, err := c.Create(p)
	if err != nil {
		return err
	}
	return f.Close()
}

func Rename(c *sftp.Client, from, to string) error { return c.PosixRename(from, to) }

// Chmod 按八进制字符串设置权限（如 "0755"、"644"）。
func Chmod(c *sftp.Client, p, mode string) error {
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || v > 0o7777 {
		return fmt.Errorf("无效的权限: %s", mode)
	}
	return c.Chmod(p, os.FileMode(uint32(v)))
}

// DeleteAll 删除文件或目录（目录递归删除）。用 Lstat 判类型：
// 符号链接只删链接本身，绝不循进目标（防误删与循环）。
func DeleteAll(c *sftp.Client, p string) error {
	st, err := c.Lstat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return c.Remove(p)
	}
	fis, err := c.ReadDir(p)
	if err != nil {
		return err
	}
	for _, fi := range fis {
		if err := DeleteAll(c, path.Join(p, fi.Name())); err != nil {
			return err
		}
	}
	return c.RemoveDirectory(p)
}

// ---- 压缩 / 解压（zip 与 tar.gz 双格式，流式读写不落盘） ----

func Compress(c *sftp.Client, paths []string, format, dest string) error {
	if dir := path.Dir(dest); dir != "" && dir != "." {
		_ = c.MkdirAll(dir)
	}
	f, err := c.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	switch strings.ToLower(format) {
	case "zip":
		return compressZip(f, c, paths)
	case "targz":
		gz := gzip.NewWriter(f)
		defer gz.Close()
		return compressTar(gz, c, paths)
	default:
		return fmt.Errorf("不支持的压缩格式: %s", format)
	}
}

func compressZip(w io.Writer, c *sftp.Client, paths []string) error {
	zw := zip.NewWriter(w)
	for _, src := range paths {
		rel := path.Base(src)
		if err := addToZip(zw, c, src, rel); err != nil {
			zw.Close()
			return err
		}
	}
	return zw.Close()
}

func addToZip(zw *zip.Writer, c *sftp.Client, src, rel string) error {
	st, err := c.Lstat(src)
	if err != nil {
		return err
	}
	if st.IsDir() {
		fis, err := c.ReadDir(src)
		if err != nil {
			return err
		}
		if len(fis) == 0 { // 保留空目录
			_, err = zw.Create(rel + "/")
			return err
		}
		for _, fi := range fis {
			if err := addToZip(zw, c, path.Join(src, fi.Name()), path.Join(rel, fi.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	f, err := c.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := &zip.FileHeader{Name: rel, Modified: st.ModTime()}
	hdr.SetMode(st.Mode().Perm())
	zf, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(zf, f)
	return err
}

func compressTar(w io.Writer, c *sftp.Client, paths []string) error {
	tw := tar.NewWriter(w)
	for _, src := range paths {
		if err := addToTar(tw, c, src, path.Base(src)); err != nil {
			tw.Close()
			return err
		}
	}
	return tw.Close()
}

func addToTar(tw *tar.Writer, c *sftp.Client, src, rel string) error {
	st, err := c.Lstat(src)
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: rel, ModTime: st.ModTime(), Mode: int64(st.Mode().Perm())}
	if st.IsDir() {
		hdr.Typeflag = tar.TypeDir
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		fis, err := c.ReadDir(src)
		if err != nil {
			return err
		}
		for _, fi := range fis {
			if err := addToTar(tw, c, path.Join(src, fi.Name()), path.Join(rel, fi.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	hdr.Typeflag = tar.TypeReg
	hdr.Size = st.Size()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := c.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// Decompress 解压 zip / tar.gz 到 destDir（防 zip-slip：条目路径清洗后必须仍在 destDir 内）。
func Decompress(c *sftp.Client, src, destDir string) error {
	name := strings.ToLower(path.Base(src))
	switch {
	case strings.HasSuffix(name, ".zip"):
		return unzip(c, src, destDir)
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return untargz(c, src, destDir)
	default:
		return fmt.Errorf("不支持的解压格式: %s（支持 .zip / .tar.gz / .tgz）", name)
	}
}

func insideDir(dir, p string) bool {
	// 两侧均为 Clean 过的绝对路径，前缀匹配即可判定越界
	if dir == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func unzip(c *sftp.Client, src, destDir string) error {
	f, err := c.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}
	for _, zf := range zr.File {
		target := path.Join(destDir, zf.Name)
		if !insideDir(destDir, target) {
			return fmt.Errorf("zip 条目路径越界: %s", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := c.MkdirAll(target); err != nil {
				return err
			}
			continue
		}
		if dir := path.Dir(target); dir != destDir {
			if err := c.MkdirAll(dir); err != nil {
				return err
			}
		}
		if err := extractEntry(c, target, zf.ModTime(), zf.Mode(), func(w io.Writer) error {
			rc, e := zf.Open()
			if e != nil {
				return e
			}
			defer rc.Close()
			_, e = io.Copy(w, rc)
			return e
		}); err != nil {
			return err
		}
	}
	return nil
}

func untargz(c *sftp.Client, src, destDir string) error {
	f, err := c.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("打开 tar.gz 失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, rerr := tr.Next()
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return fmt.Errorf("读取 tar 失败: %w", rerr)
		}
		target := path.Join(destDir, hdr.Name)
		if !insideDir(destDir, target) {
			return fmt.Errorf("tar 条目路径越界: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := c.MkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if dir := path.Dir(target); dir != destDir {
				if err := c.MkdirAll(dir); err != nil {
					return err
				}
			}
			mode := os.FileMode(hdr.Mode)
			if err := extractEntry(c, target, hdr.ModTime, mode, func(w io.Writer) error {
				_, e := io.Copy(w, tr)
				return e
			}); err != nil {
				return err
			}
		}
	}
}

// extractEntry 建目标文件、流式写入并回填修改时间。
func extractEntry(c *sftp.Client, target string, mtime time.Time, mode os.FileMode, fill func(io.Writer) error) error {
	wf, err := c.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
	if err != nil {
		return err
	}
	if err := fill(wf); err != nil {
		wf.Close()
		return err
	}
	if err := wf.Close(); err != nil {
		return err
	}
	_ = c.Chtimes(target, mtime, mtime)
	if mode != 0 {
		_ = c.Chmod(target, mode.Perm())
	}
	return nil
}
