package opsagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	{Topic: "host.security", Desc: "主机防御事件（SSH 爆破/疑似爆破成功，主机 Agent 检测上报）"},
	{Topic: "security.attack", Desc: "安全中心攻击事件（web 爆破/CC/敏感路径扫描/面板登录爆破等）"},
}

// securityTopics 攻击类事件主题：会话默认防御模式取 AgentConfig.AttackMode（默认自动处置），
// 其余事件沿用 DefenseMode。AI 处置动作仍受三级防御门控与工具审计约束。
var securityTopics = map[string]bool{
	"host.security":   true,
	"security.attack": true,
}

// IsSecurityTopic 判断事件主题是否为攻击类。
func IsSecurityTopic(topic string) bool { return securityTopics[topic] }

type EventDesc struct {
	Topic string `json:"topic"`
	Desc  string `json:"desc"`
}

// modeRules 三级防御模式在系统提示词中的规则说明。
var modeRules = map[string]string{
	ModeObserve: "当前为「仅观察」模式：你只能查询、分析与发送通知（panel_notify），不能执行任何处置操作。" +
		"发现问题时给出具体建议方案（工具名+参数+影响），等待人工确认或切换模式。",
	ModeConfirm: "当前为「执行前确认」模式：你调用的处置类操作会先展示给用户，确认后才会执行。" +
		"若操作被拒绝，不要重复原方案，改提替代建议。",
	ModeAuto: "当前为「自动处置」模式：你可以直接执行处置操作，但必须先用只读工具确认现状与影响范围，" +
		"操作后在回复中清楚说明做了什么、为什么。",
}

// buildSystemPrompt 组装系统提示词：角色 + 面板上下文 + 防御模式规则 + 记忆注入 + 常驻知识 + 行为准则。
func buildSystemPrompt(sess *model.OpsAgentSession, hosts []model.SshHost, siteDomains []string, memories []model.OpsAgentMemory, kbs []model.OpsAgentKnowledge) string {
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
	if rule, ok := modeRules[sess.DefenseMode]; ok {
		b.WriteString(rule)
	} else {
		b.WriteString(modeRules[ModeObserve])
	}
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

	// 常驻知识：人工维护、每次会话全量携带；总量超限时只注入能放下的部分，
	// 其余靠 kb_search / kb_read 工具按需查询（kb 工具始终可用，不受此上限影响）。
	// 上限按字符（rune）计：知识内容以中文为主，len() 的字节口径会低估约 2/3 容量。
	if len(kbs) > 0 {
		b.WriteString("\n# 常驻知识（人工维护的运维知识库，每次会话自动携带，优先遵循）\n")
		total, capped := 0, false
		for _, k := range kbs {
			if total+utf8.RuneCountInString(k.Content) > kbPersistentMaxChars {
				capped = true
				break
			}
			total += utf8.RuneCountInString(k.Content)
			fmt.Fprintf(&b, "## %s\n%s\n\n", k.Title, k.Content)
		}
		if capped {
			b.WriteString("（常驻知识总量超出注入上限，以上仅为部分条目；更多内容可用 kb_search 检索、kb_read 查看全文）\n")
		}
	}

	b.WriteString(`
# 记忆使用
用 memory_save / memory_search / memory_forget 维护长期记忆（跨会话生效）：
- 值得记：主机特殊路径与架构、故障定位结论、用户明确交代的事项与偏好
- 不值得记：一次性的查询结果、随时会变的实时指标

# 知识库使用
运维知识库是人工维护的操作规范/手册（kb_search 检索条目，kb_read 按 id 读全文）。
任务涉及知识库覆盖的内容（如既定处置流程、变更规范、面板约定）时，先查知识库并遵循其约定。

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
	guide := ""
	if IsSecurityTopic(topic) {
		guide = `
处置提示：这是攻击事件。主机类可先 host_firewall_status 查看封禁列表、host_check 做账号/持久化基线排查；
确认攻击者 IP 未被封禁（或需要延长封禁时长）时可调用 host_ban_ip（面板 nginx 层对应 security_block_ip）；
处置后用 panel_notify 发一条摘要通知（type 填 security.alert）。注意保护名单内的地址不能封禁也不要尝试绕过。`
	}
	return fmt.Sprintf("【系统事件】%s（%s）\n时间：%s\n数据：\n%s\n\n请分析该事件：先说明发生了什么、影响范围，再给出（或按模式执行）处置建议。%s",
		topic, desc, time.Now().Format("2006-01-02 15:04:05"), string(pj), guide)
}

// eventTitle 事件会话的短标题。
func eventTitle(topic string, payload map[string]any) string {
	for _, k := range []string{"domain", "name", "id", "taskId", "kind"} {
		if v, ok := payload[k]; ok {
			if ip, ok2 := payload["ip"]; ok2 && k == "kind" {
				return fmt.Sprintf("%s: %v (%v)", topic, v, ip)
			}
			return fmt.Sprintf("%s: %v", topic, v)
		}
	}
	return topic
}

// renderInspect 把巡检任务渲染成首条会话指令。
func renderInspect(task InspectTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【定时巡检】%s\n", task.Name)
	if len(task.Items) > 0 {
		b.WriteString("请按以下巡检项逐项检查（括号内为建议工具）：\n")
		catalog := map[string]InspectItem{}
		for _, it := range InspectCatalog {
			catalog[it.Key] = it
		}
		for _, key := range task.Items {
			it, ok := catalog[key]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "- %s（%s）\n", it.Title, it.Hint)
		}
	}
	if custom := strings.TrimSpace(task.CustomPrompt); custom != "" {
		fmt.Fprintf(&b, "\n自定义巡检指令（额外巡检要求，与巡检项同样执行并给出结论）：\n%s\n", custom)
	}
	if task.HostID != nil && *task.HostID > 0 {
		fmt.Fprintf(&b, "\n本任务绑定主机 #%d：主机类检查项（防御基线/资源水位）只检查这台主机，其他巡检项照常。\n", *task.HostID)
	}
	b.WriteString(`
要求：
1. 每个巡检项给出明确结论：正常 / 异常 / 风险
2. 异常项列出证据（具体数值、事件、容器名、域名等）
3. 最后输出汇总：整体健康度评级 + 待处理事项（按优先级排序）
4. 全部完成后调用 panel_notify 发一条摘要通知（type 填 inspect.report，标题「巡检报告：`)
	b.WriteString(task.Name)
	b.WriteString(`」，正文为一句话结论）`)
	return b.String()
}

// eventKey 事件去重键：同一来源（如同一域名、同一攻击 IP）短时间内重复触发只建一个会话。
func eventKey(payload map[string]any) string {
	for _, k := range []string{"domain", "name", "id", "ip"} {
		if v, ok := payload[k]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
