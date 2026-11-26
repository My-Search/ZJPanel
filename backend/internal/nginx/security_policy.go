package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecurityIncludeRel 站点 conf 引用的安全策略片段（相对 NginxPrefix）。
// 该文件由安全中心渲染：统一封禁判定 + CC 限速，站点 conf 只负责 include。
const SecurityIncludeRel = "zjp-security/server.inc"

// SecurityIncludeLine 生成注入到 server 块的 include 行。
func SecurityIncludeLine(prefix string) string {
	return fmt.Sprintf("    include %s;", filepath.ToSlash(filepath.Join(prefix, SecurityIncludeRel)))
}

// EnsureSecurityInclude 幂等注入安全 include：每个 server 块的 server_name 行后各插一行。
// 一个站点 conf 可能有 80/443 两个 server 块，只插第一个会导致 HTTPS 完全不受策略约束，
// 所以这里按出现次数逐个插入，而不是只处理首处。
func EnsureSecurityInclude(content, prefix string) string {
	if strings.Contains(content, SecurityIncludeRel) {
		return content
	}
	line := SecurityIncludeLine(prefix) + "\n"
	var b strings.Builder
	rest := content
	for {
		idx := strings.Index(rest, "server_name")
		if idx < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[idx:], "\n")
		if end < 0 {
			b.WriteString(rest)
			b.WriteString("\n" + line)
			break
		}
		pos := idx + end + 1
		b.WriteString(rest[:pos])
		b.WriteString(line)
		rest = rest[pos:]
	}
	return b.String()
}

type fileBak struct {
	abs     string
	old     []byte
	existed bool
}

// SaveFilesAndReload 原子写入多个 nginx 配置文件（相对 NginxPrefix）→ nginx -t → reload；
// 任一步失败则恢复全部原文件，避免"只写了一半"把线上配置打坏。
func (s *Service) SaveFilesAndReload(files map[string]string) (string, error) {
	var baks []fileBak
	for rel, content := range files {
		abs, err := safeJoin(s.cfg.NginxPrefix, rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		old, rerr := os.ReadFile(abs)
		baks = append(baks, fileBak{abs: abs, old: old, existed: rerr == nil})
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			restoreBaks(baks)
			return "", err
		}
	}
	if !s.nginxAvailable() {
		return "nginx 未安装，仅写入（本地开发模式）", nil
	}
	out, err := s.TestConfig()
	if err != nil {
		restoreBaks(baks)
		return out, fmt.Errorf("nginx -t 未通过，已回滚: %s", errString(out))
	}
	out2, err2 := s.Reload()
	if err2 != nil {
		restoreBaks(baks)
		return out2, fmt.Errorf("reload 失败，已回滚: %s", errString(out2))
	}
	return out, nil
}

func restoreBaks(baks []fileBak) {
	for _, b := range baks {
		if b.existed {
			_ = os.WriteFile(b.abs, b.old, 0o644)
		} else {
			_ = os.Remove(b.abs)
		}
	}
}
