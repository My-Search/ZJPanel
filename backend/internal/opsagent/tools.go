package opsagent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"zjpanel/backend/internal/agent"
	"zjpanel/backend/internal/llm"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/terminal"
)

// agentTool 统一的工具表示：MCP 注册表工具 + 内建工具（记忆 / SSH 执行）。
type agentTool struct {
	Name     string
	Desc     string
	Schema   map[string]any
	Mutating bool
	Builtin  bool // memory_* / host_exec
}

// observeBlocked observe 模式下对处置工具的拦截文案。
const observeBlocked = "（「仅观察」模式已阻止该处置操作。请分析现状并给出建议方案，不要尝试绕过。）"

// buildToolset 组装当前会话可用的工具表：
// MCP 注册表全量工具按防御模式过滤 + 内建工具（host_exec 需设置显式开启）。
func (s *Service) buildToolset(mode string, allowSSH bool) []agentTool {
	var out []agentTool
	for _, t := range s.reg.List() {
		if t.Mutating && mode == "observe" {
			continue
		}
		out = append(out, agentTool{Name: t.Name, Desc: t.Description, Schema: normalizeSchema(t.InputSchema), Mutating: t.Mutating})
	}
	out = append(out,
		agentTool{Name: "memory_save", Desc: "保存/更新一条长期记忆（跨会话生效）。适合记录主机特殊路径、故障结论、用户交代的事项",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"key":     map[string]any{"type": "string", "description": "记忆标识（简短，如 web-01.nginx.path）"},
				"content": map[string]any{"type": "string", "description": "记忆内容"},
				"scope":   map[string]any{"type": "string", "enum": []string{"global", "host"}, "description": "作用域：global 全局 / host 绑定某台主机，默认 global"},
				"hostId":  map[string]any{"type": "integer", "description": "scope=host 时的主机 ID"},
			}, "required": []string{"key", "content"}},
			Builtin: true},
		agentTool{Name: "memory_search", Desc: "检索长期记忆（关键词匹配 key 与内容，返回最相关/最新的若干条）",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "关键词，缺省=返回最近记忆"},
				"limit": map[string]any{"type": "integer", "description": "条数，默认 10"},
			}},
			Builtin: true},
		agentTool{Name: "memory_forget", Desc: "删除一条长期记忆",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"key": map[string]any{"type": "string", "description": "要删除的记忆标识"},
			}, "required": []string{"key"}},
			Builtin: true},
	)
	if allowSSH {
		out = append(out, agentTool{Name: "host_exec", Desc: "在指定主机上通过 SSH 执行一段 shell 命令（输出截断）。危险能力：仅在结构化工具无法满足时使用，遵守先查询后行动",
				Schema: map[string]any{"type": "object", "properties": map[string]any{
					"hostId":     map[string]any{"type": "integer", "description": "主机 ID"},
					"command":    map[string]any{"type": "string", "description": "要执行的 shell 命令（非交互）"},
					"timeoutSec": map[string]any{"type": "integer", "description": "超时秒数，默认 60，上限 180"},
				}, "required": []string{"hostId", "command"}},
				Mutating: true, Builtin: true})
	}
	return out
}

// normalizeSchema MCP InputSchema 缺 type/properties 时补齐为合法 JSON Schema。
func normalizeSchema(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	return out
}

// toLLMTools 转为 OpenAI function calling 定义。
func toLLMTools(tools []agentTool) []llm.Tool {
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		desc := t.Desc
		if t.Mutating {
			desc += "【处置类操作】"
		}
		out = append(out, llm.Tool{Name: t.Name, Description: desc, Schema: t.Schema})
	}
	return out
}

// dispatch 执行一次工具调用，返回作为 tool 消息回传给模型的结果文本。
// observe 模式对处置工具在此二次拦截（过滤之外的双保险）。
func (s *Service) dispatch(sess *model.OpsAgentSession, call llm.ToolCall) string {
	args := map[string]any{}
	if call.Arguments != "" {
		_ = json.Unmarshal([]byte(call.Arguments), &args)
	}
	switch call.Name {
	case "memory_save":
		return s.toolMemorySave(sess, args)
	case "memory_search":
		return s.toolMemorySearch(args)
	case "memory_forget":
		return s.toolMemoryForget(args)
	case "host_exec":
		return s.toolHostExec(args)
	}
	tool := s.reg.Get(call.Name)
	if tool == nil {
		return "工具不存在: " + call.Name
	}
	if tool.Mutating && sess.DefenseMode == "observe" {
		return observeBlocked
	}
	out, err := tool.Handler(args)
	if err != nil {
		out = strings.TrimSpace(out + "\n错误: " + err.Error())
	}
	return trunc(strings.TrimSpace(out), 4000)
}

// needsConfirm 该调用是否需要走「执行前确认」。
func needsConfirm(mode string, t agentTool) bool {
	return t.Mutating && mode == "confirm"
}

func lookupTool(s *Service, name string) *agentTool {
	for _, t := range s.buildToolsetUnfiltered() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

// buildToolsetUnfiltered 全量工具（含 observe 会过滤掉的处置工具），供确认门控查表。
func (s *Service) buildToolsetUnfiltered() []agentTool {
	var out []agentTool
	for _, t := range s.reg.List() {
		out = append(out, agentTool{Name: t.Name, Desc: t.Description, Schema: t.InputSchema, Mutating: t.Mutating})
	}
	cfg := s.AgentConfig()
	out = append(out,
		agentTool{Name: "memory_save", Builtin: true},
		agentTool{Name: "memory_search", Builtin: true},
		agentTool{Name: "memory_forget", Builtin: true},
	)
	if cfg.AllowSSHExec {
		out = append(out, agentTool{Name: "host_exec", Mutating: true, Builtin: true})
	}
	return out
}

// ---- 内建工具实现 ----

func (s *Service) toolMemorySave(sess *model.OpsAgentSession, args map[string]any) string {
	key, _ := args["key"].(string)
	content, _ := args["content"].(string)
	if strings.TrimSpace(key) == "" || strings.TrimSpace(content) == "" {
		return "错误: key 与 content 必填"
	}
	scope, _ := args["scope"].(string)
	if scope != "host" {
		scope = "global"
	}
	ref := ""
	if scope == "host" {
		if sess.HostID != nil {
			ref = strconv.FormatUint(uint64(*sess.HostID), 10)
		} else if id := numOf(args["hostId"]); id > 0 {
			ref = strconv.FormatUint(uint64(id), 10)
		} else {
			return "错误: scope=host 需要提供 hostId（或会话绑定主机）"
		}
	}
	var rec model.OpsAgentMemory
	err := s.db.Where("scope = ? AND ref_id = ? AND key = ?", scope, ref, key).First(&rec).Error
	if err != nil {
		rec = model.OpsAgentMemory{Scope: scope, RefID: ref, Key: key, Content: content}
		err = s.db.Create(&rec).Error
	} else {
		rec.Content = content
		err = s.db.Save(&rec).Error
	}
	if err != nil {
		return "错误: " + err.Error()
	}
	return "已保存记忆: [" + key + "] " + trunc(content, 200)
}

func (s *Service) toolMemorySearch(args map[string]any) string {
	q, _ := args["query"].(string)
	limit := int(numOf(args["limit"]))
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	q = strings.TrimSpace(q)
	tx := s.db.Model(&model.OpsAgentMemory{})
	if q != "" {
		like := "%" + q + "%"
		tx = tx.Where("key LIKE ? OR content LIKE ?", like, like)
	}
	var list []model.OpsAgentMemory
	if err := tx.Order("updated_at desc").Limit(limit).Find(&list).Error; err != nil {
		return "错误: " + err.Error()
	}
	if len(list) == 0 {
		return "（无匹配记忆）"
	}
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, fmt.Sprintf("#%d [%s] %s", m.ID, m.Key, m.Content))
	}
	return strings.Join(out, "\n")
}

func (s *Service) toolMemoryForget(args map[string]any) string {
	key, _ := args["key"].(string)
	if strings.TrimSpace(key) == "" {
		return "错误: key 必填"
	}
	res := s.db.Where("key = ?", key).Delete(&model.OpsAgentMemory{})
	if res.Error != nil {
		return "错误: " + res.Error.Error()
	}
	if res.RowsAffected == 0 {
		return "未找到该记忆: " + key
	}
	return fmt.Sprintf("已删除 %d 条记忆", res.RowsAffected)
}

// toolHostExec 通过面板 SSH 通道在主机上执行命令（config.AllowSSHExec 显式开启才注册）。
func (s *Service) toolHostExec(args map[string]any) string {
	hostID := uint(numOf(args["hostId"]))
	command, _ := args["command"].(string)
	if hostID == 0 || strings.TrimSpace(command) == "" {
		return "错误: hostId 与 command 必填"
	}
	timeout := int(numOf(args["timeoutSec"]))
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 180 {
		timeout = 180
	}
	var h model.SshHost
	if err := s.db.First(&h, hostID).Error; err != nil {
		return "错误: 主机不存在"
	}
	if h.Local {
		return "错误: 内置本机不支持 SSH 执行（本机操作请使用结构化工具）"
	}
	client, err := terminal.DialHostByID(s.db, s.box, hostID)
	if err != nil {
		return "错误: SSH 连接失败: " + err.Error()
	}
	defer client.Close()
	done := make(chan struct{})
	var out string
	var runErr error
	go func() {
		defer close(done)
		out, runErr = agent.RunScript(client, command)
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(timeout) * time.Second):
		return "错误: 执行超时（" + strconv.Itoa(timeout) + " 秒），命令可能仍在目标机运行，请检查"
	}
	if runErr != nil && strings.TrimSpace(out) == "" {
		return "错误: " + runErr.Error()
	}
	s.audit("opsagent.host_exec", fmt.Sprintf("host=%d cmd=%s", hostID, trunc(command, 200)))
	return trunc(strings.TrimSpace(out), 8000)
}

// audit 智能体动作留痕（无 HTTP 上下文，IP 记为 opsagent）。
func (s *Service) audit(action, detail string) {
	s.db.Create(&model.AuditLog{Time: time.Now(), Action: action, Detail: detail, IP: "opsagent"})
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…（截断）"
}

// numOf 兼容 LLM 可能给出的 float64 / json.Number 参数值。
func numOf(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}
