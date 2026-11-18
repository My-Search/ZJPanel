package plugin

import (
	"archive/zip"
	"bytes"
	"testing"
)

func buildZip(entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestUnzipPluginRejectsZipSlip(t *testing.T) {
	root := t.TempDir()
	// 1. 以 / 开头且含 .. 的路径穿越（回归：此前 !strings.HasPrefix(name, "/") 放过了该情况）
	badZip1 := buildZip(map[string]string{
		"/a/../../evil.txt": "pwn",
		"plugin.yaml":       "id: demo\nname: Demo\nversion: 0.1.0\n",
	})
	if _, _, err := unzipPlugin(badZip1, root); err == nil {
		t.Fatal("含 /../../ 的 zip 应被拒绝")
	}

	// 2. 相对路径含 .. 的路径穿越
	badZip2 := buildZip(map[string]string{
		"demo/../../evil.txt": "pwn",
		"demo/plugin.yaml":    "id: demo\nname: Demo\nversion: 0.1.0\n",
	})
	if _, _, err := unzipPlugin(badZip2, root); err == nil {
		t.Fatal("含 ../ 的 zip 应被拒绝")
	}

	// 3. 合法 zip 应正常解压
	goodZip := buildZip(map[string]string{
		"demo/plugin.yaml": "id: demo\nname: Demo\nversion: 0.1.0\n",
		"demo/main.go":     "package main\n",
	})
	if _, id, err := unzipPlugin(goodZip, root); err != nil || id != "demo" {
		t.Fatalf("合法插件 zip 应通过: id=%q err=%v", id, err)
	}
}

func TestValidID(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../etc", "a/b", "a\\b", "-bad"} {
		if ValidID(bad) {
			t.Errorf("非法插件 ID 应被拒绝: %q", bad)
		}
	}
	if !ValidID("notify-webhook") || !ValidID("mysqladmin") {
		t.Error("合法插件 ID 应通过")
	}
}
