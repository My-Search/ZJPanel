package targets

import (
	"encoding/json"
	"strings"
	"testing"

	certdns "zjpanel/backend/internal/cert/dns"
)

// 部署目标的字段清单必须用 JSON 小写键 fields 暴露：前端按 .fields 渲染输入项，
// 键名写成 Fields 会让整块表单静默消失（只剩类型下拉）。
func TestTargetDefJSONContract(t *testing.T) {
	def := &TargetDef{
		Type:  "ssh_upload",
		Title: "上传到 SSH 主机",
		Fields: []certdns.FieldDef{
			{Key: "hostId", Title: "SSH 主机", Type: "select", OptionsSource: "hosts", Required: true},
			{Key: "certPath", Title: "证书远程路径", Type: "text", Default: "/etc/nginx/ssl/fullchain.pem"},
		},
		Owner: "builtin",
	}
	b, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	if !strings.Contains(raw, `"fields"`) {
		t.Fatalf("缺少 fields 键: %s", raw)
	}
	if strings.Contains(raw, `"Fields"`) {
		t.Fatalf("字段键名不应大写: %s", raw)
	}
	if !strings.Contains(raw, `"optionsSource":"hosts"`) {
		t.Fatalf("字段选项来源未正确序列化: %s", raw)
	}
}
