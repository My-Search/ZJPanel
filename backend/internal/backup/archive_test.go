package backup

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCopyEntry 以声明大小 size 把文件内容写成一个 tar 条目，返回 changed 标记与完整包字节流。
func runCopyEntry(t *testing.T, content []byte, size int64) (bool, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fh, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.Write(content); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	// 与 Build 的调用顺序一致：先写声明 size 的头，再定长写数据。
	if err := tw.WriteHeader(&tar.Header{Name: "f", Size: size}); err != nil {
		t.Fatalf("写头失败: %v", err)
	}
	changed, err := copyTarEntry(tw, fh, size)
	if err != nil {
		t.Fatalf("copyTarEntry 失败: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar 收尾失败（流结构已损坏）: %v", err)
	}
	return changed, buf.Bytes()
}

// readFirstEntry 读回 tar 包中第一个条目的内容，并校验流结构完整（大小一致、无残留条目）。
func readFirstEntry(t *testing.T, pkg []byte) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(pkg))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("读取 tar 条目失败: %v", err)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("读取条目内容失败: %v", err)
	}
	if hdr.Size != int64(len(body)) {
		t.Fatalf("条目大小与内容不符: hdr=%d body=%d", hdr.Size, len(body))
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("包内应无更多条目: %v", err)
	}
	return body
}

// TestCopyTarEntryGrowth 活文件在打包期间变大：截断为快照大小，tar 流保持完整
// （修复前 io.Copy 不设上限会触发 archive/tar: write too long 使整个备份失败）。
func TestCopyTarEntryGrowth(t *testing.T) {
	content := bytes.Repeat([]byte("A"), 25)
	changed, pkg := runCopyEntry(t, content, 10)
	if !changed {
		t.Fatal("文件变大应标记 changed")
	}
	body := readFirstEntry(t, pkg)
	if len(body) != 10 || string(body) != string(content[:10]) {
		t.Fatalf("应截断为快照大小 10: len=%d", len(body))
	}
}

// TestCopyTarEntryShrink 活文件在打包期间变小/被替换：补零填满声明大小，
// 后续条目与收尾不受影响。
func TestCopyTarEntryShrink(t *testing.T) {
	changed, pkg := runCopyEntry(t, []byte("12345"), 16)
	if !changed {
		t.Fatal("文件变小应标记 changed")
	}
	body := readFirstEntry(t, pkg)
	if len(body) != 16 {
		t.Fatalf("应补零到 16 字节: len=%d", len(body))
	}
	if string(body[:5]) != "12345" || !bytes.Equal(body[5:], make([]byte, 11)) {
		t.Fatal("补零段异常")
	}
}

// TestCopyTarEntryStable 大小未变化的普通文件：原样写入且不标记 changed。
func TestCopyTarEntryStable(t *testing.T) {
	changed, pkg := runCopyEntry(t, []byte("abcdefgh"), 8)
	if changed {
		t.Fatal("稳定文件不应标记 changed")
	}
	if body := readFirstEntry(t, pkg); string(body) != "abcdefgh" {
		t.Fatalf("内容异常: %q", body)
	}
}

// stubFree 注入固定的磁盘剩余水位（Restore 后由 t.Cleanup 还原）。
func stubFree(t *testing.T, v int64) {
	t.Helper()
	prev := diskFreeBytes
	diskFreeBytes = func(string) int64 { return v }
	t.Cleanup(func() { diskFreeBytes = prev })
}

// writeTestCerts 造一个可供 ItemCerts 打包的证书目录。
func writeTestCerts(t *testing.T, data string) {
	t.Helper()
	dir := filepath.Join(data, "certs", "a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), []byte("cert-body"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildAbortsOnLowDisk 打包中磁盘剩余跌破 500MB 水位：立即失败且不留半截包。
func TestBuildAbortsOnLowDisk(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)
	writeTestCerts(t, data)
	stubFree(t, minFreeBytes-1)

	_, err := s.Build([]string{ItemCerts}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "磁盘剩余空间不足") {
		t.Fatalf("低水位应报磁盘空间不足: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(data, "backups"))
	if len(entries) != 0 {
		t.Fatalf("失败后备份目录应无残包: %d 个文件", len(entries))
	}
}

// TestBuildReportsFreeSpace 打包日志展示磁盘剩余空间（起始行与进度行都带）。
func TestBuildReportsFreeSpace(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)
	writeTestCerts(t, data)

	var lines []string
	if _, err := s.Build([]string{ItemCerts}, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "磁盘剩余") {
			found = true
		}
	}
	if !found {
		t.Fatalf("打包日志缺少磁盘剩余展示: %v", lines)
	}
}

// TestDiskFreeImpl 平台实现：有效目录返回正值，不存在的路径返回 -1。
func TestDiskFreeImpl(t *testing.T) {
	if diskFreeImpl(t.TempDir()) <= 0 {
		t.Fatal("有效目录应返回正值剩余空间")
	}
	if diskFreeImpl(filepath.Join(t.TempDir(), "missing")) != -1 {
		t.Fatal("无效路径应返回 -1")
	}
}
