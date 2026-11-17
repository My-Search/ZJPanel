package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripTag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ghcr.io/my-search/zjpanel:latest", "ghcr.io/my-search/zjpanel"},
		{"ghcr.io/my-search/zjpanel:v1.0.1", "ghcr.io/my-search/zjpanel"},
		{"ghcr.io/my-search/zjpanel", "ghcr.io/my-search/zjpanel"},
		{"registry.example.com:5000/team/zjpanel:v2", "registry.example.com:5000/team/zjpanel"},
	}
	for _, c := range cases {
		if got := stripTag(c.in); got != c.want {
			t.Errorf("stripTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/opt/zjpanel/docker-compose.yml", "/opt/zjpanel/docker-compose.yml"},
		{"/opt/zjpanel/docker-compose.yml /other/compose.yml", "/opt/zjpanel/docker-compose.yml"},
		{" /opt/zjpanel/docker-compose.yml:/other ", "/opt/zjpanel/docker-compose.yml"},
		{"", ""},
	}
	for _, c := range cases {
		if got := firstPath(c.in); got != c.want {
			t.Errorf("firstPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseContainerID(t *testing.T) {
	id := strings.Repeat("ab", 32) // 64 位小写十六进制，动态构造避免手写长度出错
	cases := []struct{ name, cgroup string }{
		{"cgroup v2 scope", "0::/system.slice/docker-" + id + ".scope\n"},
		{"cgroup v1 docker path", "11:name=systemd:/docker/" + id + "\n"},
		{"cgroup v2 root (no id)", "0::/\n"}, // 取不到应返回空，不应崩
		{"empty", ""},
	}
	for _, c := range cases {
		got := parseContainerID(c.cgroup)
		want := ""
		if c.name != "cgroup v2 root (no id)" && c.name != "empty" {
			want = id
		}
		if got != want {
			t.Errorf("parseContainerID(%s) = %q, want %q", c.name, got, want)
		}
	}
}

func TestImageRefFromCompose(t *testing.T) {
	content := `services:
  zjpanel:
    image: ghcr.io/my-search/zjpanel:latest
    container_name: zjpanel
  other:
    image: nginx:alpine
`
	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := imageRefFromCompose(file); got != "ghcr.io/my-search/zjpanel:latest" {
		t.Errorf("imageRefFromCompose = %q, want latest ref", got)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if got := imageRefFromCompose(file); got != "" {
		t.Errorf("imageRefFromCompose(不存在) = %q, want empty", got)
	}
}

func TestRewriteComposeImage(t *testing.T) {
	const repo = "ghcr.io/my-search/zjpanel"
	original := `services:
  zjpanel:
    image: ghcr.io/my-search/zjpanel:latest
    container_name: zjpanel
    restart: unless-stopped
  other:
    image: nginx:alpine
`

	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	newImage := repo + ":v1.2.3"
	if err := rewriteComposeImage(file, repo, newImage); err != nil {
		t.Fatalf("rewriteComposeImage: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "image: "+newImage) {
		t.Errorf("新镜像未写入: %s", s)
	}
	if !strings.Contains(s, "image: nginx:alpine") {
		t.Errorf("其他 service 的镜像被误改: %s", s)
	}
	// 原内容已备份
	bak, err := os.ReadFile(file + ".bak")
	if err != nil {
		t.Fatalf("未生成 .bak: %v", err)
	}
	if string(bak) != original {
		t.Error(".bak 内容与原文件不一致")
	}
}

func TestRewriteComposeImageRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	content := "services:\n  zjpanel:\n    image: other/image:latest\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteComposeImage(file, "ghcr.io/my-search/zjpanel", "ghcr.io/my-search/zjpanel:v1"); err == nil {
		t.Error("镜像不匹配应拒绝改写")
	}
	got, _ := os.ReadFile(file)
	if string(got) != content {
		t.Error("拒绝改写时文件不应变化")
	}
}
