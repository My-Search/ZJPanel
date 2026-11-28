package opsagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/model"
)

// EventDesc 事件目录：智能体可订阅的 eventBus 主题（UI 勾选清单与订阅共用）。
var EventCatalog = []EventDesc{
	{Topic: "sitecheck.down", Desc: "站点探测失败（宕机/超时/异常状态码）"},
	{Topic: "sitecheck.up", Desc: "站点探测恢复上线"},
	{Topic: "cert.failed", Desc: "证书签发/续期失败"},
	{Topic: "cert.expiring", Desc: "证书即将到期提醒"},
	{Topic: "cert.issued", Desc: "证书签发成功"},
	{Topic: "backup.done", Desc: "备份任务结束（成功或失败）"},
	{Topic: "plugin.enabled", Desc: "插件启用"},
	{Topic: "plugin.disabled", Desc: "插件停用"},
}

type EventDesc struct {
	Topic string `json:"topic"`
	Desc  string `json:"desc"`
}

// modeRules 三级防御模式在系统提示词中的规则说明。
var modeRules = map[string]string{
	"observe": "当前为「仅观察」模式：你只能查询、分析与发送通知（panel_notify），不能执行任何处置操作。" +
		"发现问题时给出具体建议方案（工具名+参数+影响），等待人工确认或切换模式。",
	"confirm": "当前为「执行前确认」模式：你调用的处置类操作会先展示给用户，确认后才会执行。" +
		"若操作被拒绝，不要重复原方案，改提替代建议。",
	"auto": "当前为「自动处置」模式：你可以直接执行处置操作，但必须先用只读工具确认现状与影响范围，" +
		"操作后在回复中清楚说明做了什么、为什么。",
}

// buildSystemPrompt 组装系统提示词：角色 + 面板上下文 + 防御模式规则 + 记忆注入 + 行为准则。
func buildSystemPrompt(sess *model.OpsAgentSession, hosts []model.SshHost, siteDomains []string, memories []model.OpsAgentMemory) string {
	var b strings.Builder
	b.WriteString("你是 ZJPanel（nginx 站点/证书/容器/主机一体化管理面板）内置的 AI 运维智能体，")
	b.WriteString("通过工具查询与操作面板：站点、nginx、容器、compose、证书、主机 Agent、安全中心、WAF、备份等。\n\n")

	b.WriteString("# 面板上下文\n")
	fmt.Fprintf(&b, "- 面板版本：%s\n", buildinfo.Version)
	fmt.Fprintf(&b, "- 当前时间：%s\n", time.Now().Format("2006-01-02 15:04:05"))
	if len(hosts) > 0 {
		b.WriteString("- 受管主机：\n")
		for _, h := range hosts {
			tag := "SSH"
			if h.Local {
				tag = "本机"
			} else if h.AgentStatus == "online" {
				tag = "Agent在线"
			}
			fmt.Fprintf(&b, "  - #%d %s（%s:%d, %s）\n", h.ID, h.Name, h.Host, h.Port, tag)
		}
	}
	if sess.HostID != nil {
		fmt.Fprintf(&b, "- 本会话绑定的主机：#%d（host_exec 等主机操作默认作用于它）\n", *sess.HostID)
	}
	if len(siteDomains) > 0 {
		fmt.Fprintf(&b, "- 站点（%d 个）：%s\n", len(siteDomains), strings.Join(siteDomains, ", "))
	}

	b.WriteString("\n# 防御模式\n")
	b.WriteString(modeRules[sess.DefenseMode])
	b.WriteString("\n")

	if len(memories) > 0 {
		b.WriteString("\n# 已有记忆（跨会话持久，可直接引用）\n")
		for _, m := range memories {
			if m.Key != "" {
				fmt.Fprintf(&b, "- [%s] %s\n", m.Key, m.Content)
			} else {
				fmt.Fprintf(&b, "- %s\n", m.Content)
			}
		}
	}

	b.WriteString(`
# 记忆使用
用 memory_save / memory_search / memory_forget 维护长期记忆（跨会话生效）：
- 值得记：主机特殊路径与架构、故障定位结论、用户明确交代的事项与偏好
- 不值得记：一次性的查询结果、随时会变的实时指标

# 行为准则
- 先查询后行动：处置前先用只读工具确认现状，避免凭猜测操作
- 工具结果是唯一事实来源，查不到就如实说，不要编造
- 处置类操作必须先说明影响范围（动什么、影响谁、怎么回退）
- 中文回复，先结论后细节，简洁直接`)
	return b.String()
}

// renderEvent 把 eventBus 载荷渲染成首条会话消息。
func renderEvent(topic string, payload map[string]any) string {
	pj, _ := json.MarshalIndent(payload, "", "  ")
	if len(pj) > 2000 {
		pj = append(pj[:2000], []byte("\n…（截断）")...)
	}
	var desc string
	for _, e := range EventCatalog {
		if e.Topic == topic {
			desc = e.Desc
		}
	}
	return fmt.Sprintf("【系统事件】%s（%s）\n时间：%s\n数据：\n%s\n\n请分析该事件：先说明发生了什么、影响范围，再给出（或按模式执行）处置建议。",
		topic, desc, time.Now().Format("2006-01-02 15:04:05"), string(pj))
}

// eventTitle 事件会话的短标题。
func eventTitle(topic string, payload map[string]any) string {
	for _, k := range []string{"domain", "name", "id", "taskId", "kind"} {
		if v, ok := payload[k]; ok {
			return fmt.Sprintf("%s: %v", topic, v)
		}
	}
	return topic
}

// eventKey 事件去重键：同一来源（如同一域名）短时间内重复触发只建一个会话。
func eventKey(payload map[string]any) string {
	for _, k := range []string{"domain", "name", "id"} {
		if v, ok := payload[k]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
