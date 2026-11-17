package cert

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"

	"zjpanel/backend/internal/model"
)

// ---- 任务 JSON 结构 ----

// DeployTargetConfig 部署目标配置（存 CertTask.DeployTargets JSON）。
type DeployTargetConfig struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

func (s *Service) DecodeTargets(raw string) []DeployTargetConfig {
	var out []DeployTargetConfig
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// EncodeTargets 部署目标序列化。
func (s *Service) EncodeTargets(list []DeployTargetConfig) string {
	b, err := json.Marshal(list)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ---- 文件/证书解析辅助 ----

func pkgEnsureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

func osWriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// parseLeaf 解析 bundle 中第一个 PEM（leaf 证书）。
func parseLeaf(bundle []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(bundle)
	if block == nil {
		return nil, fmt.Errorf("证书 PEM 解析失败")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ---- 运行日志（certd 的 per-node logger 思路：缓冲 + WS 实时 + DB 落盘）----

type runLogger struct {
	svc *Service
	run *model.CertRun
	mu  *sync.Mutex
	buf []string
}

func (l *runLogger) line(level, msg string) {
	line := fmt.Sprintf("[%s][%s] %s", time.Now().Format("2006-01-02 15:04:05"), level, msg)
	l.mu.Lock()
	l.buf = append(l.buf, line)
	l.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "log", "runId": l.run.ID, "line": line})
	l.svc.hub.Broadcast("cert-run", b)
}

func (l *runLogger) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) == 0 {
		return
	}
	text := stringsJoin(l.buf, "\n")
	l.run.Log = l.run.Log + text + "\n"
	l.svc.db.Model(&model.CertRun{}).Where("id = ?", l.run.ID).Update("log", l.run.Log)
	l.buf = nil
}

func stringsJoin(list []string, sep string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
