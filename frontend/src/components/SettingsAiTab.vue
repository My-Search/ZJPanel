<template>
  <!-- 设置页「运维Agent」下拉二级菜单选中后渲染的单页内容；
       page 由父组件（SettingsPage 的下拉菜单）决定，本组件不再自带子标签 -->
  <a-card v-if="page === 'llm'" size="small" title="模型配置（OpenAI 兼容）" class="ai-card ai-card-narrow">
    <a-form layout="vertical">
      <a-form-item label="API 地址 BaseURL">
        <a-input v-model:value="llm.baseURL" placeholder="https://api.deepseek.com" />
      </a-form-item>
      <a-form-item :label="llm.apiKeySet ? 'API Key（留空沿用）' : 'API Key'">
        <!-- autocomplete=new-password：抑制浏览器密码管理器自动回填（保存后重开应保持为空，留空即沿用） -->
        <a-input-password v-model:value="llmApiKey" autocomplete="new-password" :placeholder="llm.apiKeySet ? '••••••' : 'sk-…（本地模型如 Ollama 可留空）'" />
      </a-form-item>
      <a-form-item label="模型名">
        <a-input v-model:value="llm.model" placeholder="deepseek-chat / qwen-plus / glm-4-flash …" />
      </a-form-item>
      <a-form-item label="上下文窗口（tokens）">
        <a-input-number v-model:value="llm.contextTokens" :min="4096" :step="10000" style="width: 100%" />
        <p class="ai-hint">用于估算上下文占用与自动压缩时机（超过 75% 触发压缩；超限时强制压缩后重试）。默认 300000</p>
      </a-form-item>
      <a-space>
        <a-button :loading="testing" @click="doTest">测试连通</a-button>
        <a-button type="primary" :loading="savingLLM" @click="saveLLM">保存模型配置</a-button>
      </a-space>
      <p class="ai-hint">兼容 DeepSeek / 通义 / GLM / Kimi / OpenRouter / one-api / Ollama 等 OpenAI 协议服务；带版本路径请填完整（如 GLM 填 https://open.bigmodel.cn/api/paas/v4）。</p>
    </a-form>
  </a-card>

  <a-card v-else-if="page === 'agent'" size="small" title="事件与防御" class="ai-card ai-card-narrow">
    <a-alert
      type="info"
      show-icon
      message="事件触发：勾选的事件发生时，Agent 会自动创建会话分析并按「事件会话防御模式」行动。主动防御默认关闭，请按需开启。"
      style="margin-bottom: 14px"
    />
    <a-form layout="vertical">
      <a-form-item label="事件自动分析">
        <a-switch v-model:checked="agent.eventEnabled" />
        <span class="ai-switch-hint">开启后按下方勾选的事件自动创建会话</span>
      </a-form-item>
      <a-form-item label="监听事件">
        <div class="ai-ev-list">
          <a-checkbox
            v-for="e in settings?.events || []"
            :key="e.topic"
            :checked="!!agent.events[e.topic]"
            @change="(ev: any) => (agent.events[e.topic] = !!ev.target.checked)"
          >{{ e.desc }}<span class="ai-ev-topic">{{ e.topic }}</span></a-checkbox>
        </div>
      </a-form-item>
      <a-form-item label="事件会话防御模式">
        <a-radio-group v-model:value="agent.defenseMode">
          <a-radio value="observe">仅观察（分析+通知+建议）</a-radio>
          <a-radio value="confirm">执行前确认（处置需人工确认）</a-radio>
          <a-radio value="auto">自动处置（全程审计）</a-radio>
        </a-radio-group>
        <p class="ai-hint">防御模式是会话级设置，这里只决定事件自动创建的会话；手动会话在工作台创建时选择（空闲可切换），定时巡检任务在任务里各自指定。</p>
      </a-form-item>
      <a-form-item>
        <a-switch v-model:checked="agent.allowSSHExec" />
        <span class="ai-switch-hint">允许 Agent 通过 SSH 在主机上执行任意命令（高危总开关，默认关闭；仍受会话防御模式约束：仅观察不提供该工具，执行前确认需人工放行）</span>
      </a-form-item>
      <a-button type="primary" :loading="savingAgent" @click="saveAgent">保存 Agent 配置</a-button>
    </a-form>
  </a-card>

  <a-card v-else-if="page === 'kb'" size="small" title="运维知识库" class="ai-card">
    <a-alert
      type="info"
      show-icon
      message="人工维护的操作规范/手册/约定（区别于 Agent 自动积累的长期记忆）。Agent 会话中可用 kb_search 检索、kb_read 查看全文；勾选「常驻」的条目会在每次会话（含事件与巡检会话）自动注入系统提示词，常驻总量上限 2 万字符，超出部分 Agent 仍可通过工具查询。"
      style="margin-bottom: 12px"
    />
    <div class="ai-kb-bar">
      <a-input-search v-model:value="kbQuery" placeholder="搜索标题 / 标签 / 内容" size="small" style="max-width: 240px" allow-clear @search="loadKnowledge(kbQuery)" />
      <a-button size="small" type="primary" @click="openKB(null)"><PlusOutlined /> 新建知识</a-button>
    </div>
    <div class="ai-kb-list">
      <div v-for="k in knowledge" :key="k.id" class="ai-kb-row">
        <div class="ai-kb-main">
          <div class="ai-kb-top">
            <b class="ai-kb-title">{{ k.title }}</b>
            <a-tag v-if="k.persistent" color="gold" style="margin-right: 0">常驻</a-tag>
            <a-tag v-for="t in kbTagList(k.tags)" :key="t" style="margin-right: 0">{{ t }}</a-tag>
          </div>
          <div class="ai-kb-content">{{ k.content }}</div>
          <div class="ai-kb-time">更新：{{ fmtTime(k.updatedAt) }}</div>
        </div>
        <a-space size="4">
          <a-button size="small" @click="openKB(k)">编辑</a-button>
          <a-popconfirm title="删除该知识条目？" @confirm="removeKnowledge(k.id)">
            <a-button size="small" danger>删除</a-button>
          </a-popconfirm>
        </a-space>
      </div>
      <div v-if="!knowledge.length" class="ai-mem-empty">暂无知识条目。可添加操作规范、故障处理手册、面板使用约定等，供 Agent 会话中查阅</div>
    </div>
  </a-card>

  <a-card v-else-if="page === 'memory'" size="small" title="长期记忆" class="ai-card">
    <div class="ai-mem-bar">
      <a-input-search v-model:value="memQuery" placeholder="搜索记忆" size="small" @search="loadMemories(memQuery)" />
    </div>
    <div class="ai-mem-list">
      <div v-for="m in memories" :key="m.id" class="ai-mem-row">
        <div class="ai-mem-main">
          <div class="ai-mem-key">{{ m.key }} <a-tag v-if="m.scope === 'host'" style="margin-right: 0">主机#{{ m.refId }}</a-tag></div>
          <div class="ai-mem-content">{{ m.content }}</div>
        </div>
        <a-popconfirm title="删除这条记忆？" @confirm="deleteMemory(m.id)">
          <a class="ai-mem-del"><DeleteOutlined /></a>
        </a-popconfirm>
      </div>
      <div v-if="!memories.length" class="ai-mem-empty">暂无记忆（Agent 会话中会自动积累）</div>
    </div>
  </a-card>

  <a-card v-else-if="page === 'inspect'" size="small" title="定时巡检" class="ai-card">
    <a-alert
      type="info"
      show-icon
      message="到点自动创建巡检会话：Agent 按勾选的巡检项自查并输出报告（含摘要通知）。巡检走智能体能力，需先配置好模型；每个任务可单独指定防御模式。"
      style="margin-bottom: 12px"
    />
    <div class="ai-inspect-bar">
      <a-button size="small" type="primary" @click="openInspect(null)"><PlusOutlined /> 新建巡检任务</a-button>
    </div>
    <div class="ai-inspect-list">
      <div v-for="t in inspects" :key="t.id" class="ai-inspect-row">
        <div class="ai-inspect-main">
          <div class="ai-inspect-top">
            <a-switch size="small" :checked="t.enabled" @change="(v: any) => toggleInspect(t, !!v)" />
            <b class="ai-inspect-name">{{ t.name }}</b>
            <code class="ai-inspect-cron">{{ t.cron }}</code>
          </div>
          <div class="ai-inspect-meta">
            <a-tag :color="modeColor(t.defenseMode)" style="margin-right: 4px">{{ opsModeLabel(t.defenseMode) || '默认模式' }}</a-tag>
            <a-tag v-for="k in t.items" :key="k" style="margin-right: 4px">{{ itemTitle(k) }}</a-tag>
            <span class="ai-inspect-next">下次：{{ fmtNext(t.next) }}</span>
          </div>
        </div>
        <a-space size="4">
          <a-tooltip title="立即执行一次">
            <a-button size="small" :loading="runningId === t.id" @click="runNow(t)"><ThunderboltOutlined /></a-button>
          </a-tooltip>
          <a-button size="small" @click="openInspect(t)">编辑</a-button>
          <a-popconfirm title="删除该巡检任务？" @confirm="removeInspect(t.id)">
            <a-button size="small" danger>删除</a-button>
          </a-popconfirm>
        </a-space>
      </div>
      <div v-if="!inspects.length" class="ai-mem-empty">暂无巡检任务</div>
    </div>
  </a-card>

  <!-- 知识条目编辑弹窗 -->
  <a-modal v-model:open="kbOpen" :title="kbForm.id ? '编辑知识条目' : '新建知识条目'" :width="640" @ok="saveKB">
    <a-form layout="vertical">
      <a-form-item label="标题" required>
        <a-input v-model:value="kbForm.title" placeholder="如：nginx 变更操作规范" />
      </a-form-item>
      <a-form-item label="标签（逗号分隔，便于 Agent 检索）">
        <a-input v-model:value="kbForm.tags" placeholder="nginx, 证书" />
      </a-form-item>
      <a-form-item>
        <a-switch v-model:checked="kbForm.persistent" />
        <span class="ai-switch-hint">常驻知识：每次会话自动注入；请控制常驻条目总量，避免占用过多模型上下文</span>
      </a-form-item>
      <a-form-item label="内容" required>
        <a-textarea v-model:value="kbForm.content" :rows="14" placeholder="支持 Markdown。如操作步骤、故障处理流程、面板使用约定等" />
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 巡检任务编辑弹窗 -->
  <a-modal v-model:open="inspectOpen" :title="inspectForm.id ? '编辑巡检任务' : '新建巡检任务'" :width="580" @ok="saveInspect">
    <a-form layout="vertical">
      <a-form-item label="任务名称" required>
        <a-input v-model:value="inspectForm.name" placeholder="如：每日安全巡检" />
      </a-form-item>
      <a-form-item label="执行频率（crontab）" required>
        <a-input v-model:value="inspectForm.cron" placeholder="0 9 * * *" />
        <p class="ai-hint">5 段标准 crontab。示例：0 9 * * *（每天 9 点）· 0 */6 * * *（每 6 小时）· 30 8 * * 1-5（工作日 8:30）</p>
      </a-form-item>
      <a-form-item label="巡检项">
        <div class="ai-ev-list">
          <a-checkbox
            v-for="it in inspectCatalog"
            :key="it.key"
            :checked="inspectForm.items.includes(it.key)"
            @change="(ev: any) => toggleItem(it.key, !!ev.target.checked)"
          >{{ it.title }}<span class="ai-ev-topic">{{ it.hint }}</span></a-checkbox>
        </div>
      </a-form-item>
      <a-form-item label="自定义巡检指令（可选）">
        <a-textarea v-model:value="inspectForm.customPrompt" :rows="4" placeholder="如：检查 nginx 日志中 5xx 状态码占比，超过 10% 标记风险" />
        <p class="ai-hint">作为额外巡检要求写进 Agent 指令，可与固定巡检项搭配；只填此项也能创建纯自定义巡检任务</p>
      </a-form-item>
      <a-form-item label="防御模式">
        <a-select v-model:value="inspectForm.defenseMode" style="width: 100%">
          <a-select-option v-for="m in OPS_MODE_OPTIONS" :key="m.value" :value="m.value">
            {{ m.label }}（{{ m.hint }}）
          </a-select-option>
        </a-select>
        <p class="ai-hint">该任务创建的巡检会话使用的授权级别，与其他会话相互独立</p>
      </a-form-item>
      <a-form-item label="绑定主机（可选）">
        <a-select v-model:value="inspectForm.hostId" allow-clear placeholder="全部主机" style="width: 100%">
          <a-select-option v-for="h in hosts" :key="h.id" :value="h.id">{{ h.name }}</a-select-option>
        </a-select>
        <p class="ai-hint">绑定后，主机类巡检项（防御基线/资源水位）只巡检这台主机</p>
      </a-form-item>
      <a-form-item label="启用">
        <a-switch v-model:checked="inspectForm.enabled" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { onMounted, ref, toRefs } from 'vue'
import { message } from 'ant-design-vue'
import { DeleteOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons-vue'
import { get, fmtTime } from '../api'
import { useOpsAgent, OPS_MODE_OPTIONS, opsModeLabel, type InspectTask, type OpsKnowledge } from '../store/opsagent'

// page：当前渲染的子页（llm | agent | kb | memory | inspect），由 SettingsPage 的下拉二级菜单切换
defineProps<{ page: string }>()

const api = useOpsAgent()
// 模板一律消费 toRefs 拆出的顶层 ref（自动解包的类型检查路径最稳），
// store 对象只用于脚本内调方法，避免模板上下文类型退化
const { memories, knowledge, settings, inspects, inspectCatalog } = toRefs(api)
const loadMemories = (q?: string) => api.loadMemories(q)
const deleteMemory = (id: number) => api.deleteMemory(id)
const loadKnowledge = (q?: string) => api.loadKnowledge(q)
const removeKnowledge = (id: number) => api.deleteKnowledge(id)

const llm = ref<{ baseURL: string; model: string; apiKeySet: boolean; contextTokens: number }>({ baseURL: '', model: '', apiKeySet: false, contextTokens: 300000 })
const llmApiKey = ref('')
const agent = ref<{ defenseMode: string; eventEnabled: boolean; events: Record<string, boolean>; allowSSHExec: boolean }>({
  defenseMode: 'observe', eventEnabled: false, events: {}, allowSSHExec: false,
})
const savingLLM = ref(false)
const savingAgent = ref(false)
const testing = ref(false)
const memQuery = ref('')

// ---- 知识库 ----
const kbQuery = ref('')
const kbOpen = ref(false)
const kbForm = ref<{ id: number; title: string; tags: string; content: string; persistent: boolean }>({ id: 0, title: '', tags: '', content: '', persistent: false })
const kbTagList = (tags: string) => (tags || '').split(/[,，]/).map(t => t.trim()).filter(Boolean)
const openKB = (k: OpsKnowledge | null) => {
  kbForm.value = k
    ? { id: k.id, title: k.title, tags: k.tags, content: k.content, persistent: !!k.persistent }
    : { id: 0, title: '', tags: '', content: '', persistent: false }
  kbOpen.value = true
}
const saveKB = async () => {
  const f = kbForm.value
  if (!f.title.trim()) { message.warning('标题必填'); return }
  if (!f.content.trim()) { message.warning('内容必填'); return }
  try {
    await api.saveKnowledge({ ...f })
    kbOpen.value = false
    message.success('知识条目已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  }
}

// ---- 定时巡检 ----
const hosts = ref<{ id: number; name: string }[]>([])
const inspectOpen = ref(false)
const runningId = ref('')
const inspectForm = ref<InspectTask>({ id: '', name: '', cron: '', items: [], customPrompt: '', hostId: null, defenseMode: 'observe', enabled: true })

const itemTitle = (key: string) => inspectCatalog.value.find(i => i.key === key)?.title || key
const modeColor = (mode: string) => (({ observe: 'blue', confirm: 'orange', auto: 'red' }) as Record<string, string>)[mode] || 'default'
const fmtNext = (next: string) => {
  const d = new Date(next)
  if (!next || isNaN(d.getTime()) || d.getFullYear() < 2000) return '—'
  return `${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
const openInspect = (t: InspectTask | null) => {
  inspectForm.value = t
    ? { id: t.id, name: t.name, cron: t.cron, items: [...t.items], customPrompt: t.customPrompt || '', hostId: t.hostId, defenseMode: t.defenseMode || 'observe', enabled: t.enabled }
    : { id: '', name: '', cron: '', items: ['security'], customPrompt: '', hostId: null, defenseMode: 'observe', enabled: true }
  inspectOpen.value = true
}
const toggleItem = (key: string, on: boolean) => {
  const set = new Set(inspectForm.value.items)
  on ? set.add(key) : set.delete(key)
  inspectForm.value.items = [...set]
}
const saveInspect = async () => {
  const f = inspectForm.value
  if (!f.name.trim()) { message.warning('任务名称必填'); return }
  if (!f.cron.trim()) { message.warning('cron 表达式必填'); return }
  if (!f.items.length && !f.customPrompt.trim()) { message.warning('至少勾选一个巡检项或填写自定义巡检指令'); return }
  try {
    await api.saveInspect({ ...f })
    inspectOpen.value = false
    message.success('巡检任务已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  }
}
const toggleInspect = async (t: InspectTask, enabled: boolean) => {
  try { await api.saveInspect({ ...t, enabled }) } catch (e: any) {
    message.error(e?.response?.data?.error || '操作失败')
  }
}
const runNow = async (t: InspectTask) => {
  runningId.value = t.id
  try {
    await api.runInspect(t.id)
    message.success('巡检已启动，可在顶栏「运维Agent」查看会话进度')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '执行失败')
  } finally { runningId.value = '' }
}
const removeInspect = async (id: string) => {
  try { await api.deleteInspect(id) } catch (e: any) {
    message.error(e?.response?.data?.error || '删除失败')
  }
}
const loadHosts = async () => {
  if (hosts.value.length) return
  try {
    const list = await get<any[]>('/api/hosts')
    hosts.value = (list || []).map(h => ({ id: h.id, name: h.local ? h.name + '（本机）' : h.name }))
  } catch { /* ignore */ }
}

const load = async () => {
  try {
    await api.loadSettings()
    if (!settings.value) return
    llm.value = { baseURL: settings.value.llm.baseURL, model: settings.value.llm.model, apiKeySet: settings.value.llm.apiKeySet, contextTokens: settings.value.llm.contextTokens || 300000 }
    agent.value = {
      defenseMode: settings.value.agent.defenseMode || 'observe',
      eventEnabled: !!settings.value.agent.eventEnabled,
      events: { ...(settings.value.agent.events || {}) },
      allowSSHExec: !!settings.value.agent.allowSSHExec,
    }
    api.loadMemories()
    api.loadKnowledge()
    api.loadInspects()
    loadHosts()
  } catch { /* ignore */ }
}
const saveLLM = async () => {
  if (!llm.value.baseURL.trim() || !llm.value.model.trim()) {
    message.warning('BaseURL 与模型名必填'); return
  }
  savingLLM.value = true
  try {
    await api.saveSettings({ llm: { baseURL: llm.value.baseURL, apiKey: llmApiKey.value, model: llm.value.model, contextTokens: llm.value.contextTokens || 300000 } })
    llmApiKey.value = ''
    message.success('模型配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { savingLLM.value = false }
}
const saveAgent = async () => {
  savingAgent.value = true
  try {
    await api.saveSettings({ agent: agent.value })
    message.success('Agent 配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { savingAgent.value = false }
}
const doTest = async () => {
  testing.value = true
  try {
    const r = await api.testLLM()
    r.ok ? message.success('模型连接正常') : message.error(r.error || '连接失败')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '连接失败')
  } finally { testing.value = false }
}

onMounted(load)
</script>

<style scoped>
.ai-card { margin-bottom: 16px; }
.ai-card-narrow { max-width: 760px; }
.ai-hint { font-size: 12px; color: var(--color-slate); margin: 10px 0 0; }
.ai-switch-hint { font-size: 12px; color: var(--color-slate); margin-left: 8px; }
.ai-ev-list { display: flex; flex-direction: column; gap: 6px; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 8px 12px; max-height: 220px; overflow: auto; }
.ai-ev-topic { font-size: 11px; color: var(--color-slate); font-family: ui-monospace, Consolas, monospace; margin-left: 6px; }
.ai-mem-bar { margin-bottom: 8px; }
.ai-mem-list { max-height: 260px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 2px 12px; }
.ai-mem-row { display: flex; align-items: flex-start; gap: 8px; padding: 7px 0; border-bottom: 1px solid rgba(128,128,128,.12); }
.ai-mem-row:last-child { border-bottom: none; }
.ai-mem-main { flex: 1; min-width: 0; }
.ai-mem-key { font-size: 12.5px; font-weight: 600; font-family: ui-monospace, Consolas, monospace; }
.ai-mem-content { font-size: 12px; color: var(--color-slate); word-break: break-all; margin-top: 2px; }
.ai-mem-del { font-size: 12px; color: var(--color-slate); }
.ai-mem-del:hover { color: #ef4444; }
.ai-mem-empty { text-align: center; color: var(--color-slate); font-size: 12px; padding: 20px 0; }
/* 知识库 */
.ai-kb-bar { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-bottom: 8px; }
.ai-kb-list { max-height: 480px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 2px 12px; }
.ai-kb-row { display: flex; align-items: flex-start; gap: 8px; padding: 9px 0; border-bottom: 1px solid rgba(128,128,128,.12); }
.ai-kb-row:last-child { border-bottom: none; }
.ai-kb-main { flex: 1; min-width: 0; }
.ai-kb-top { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.ai-kb-title { font-size: 13px; color: var(--color-ink); }
.ai-kb-content { font-size: 12px; color: var(--color-slate); margin-top: 3px; white-space: pre-wrap; word-break: break-all; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.ai-kb-time { font-size: 11px; color: var(--color-slate); margin-top: 3px; }
/* 定时巡检 */
.ai-inspect-bar { margin-bottom: 8px; }
.ai-inspect-list { max-height: 300px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 2px 12px; }
.ai-inspect-row { display: flex; align-items: center; gap: 10px; padding: 9px 0; border-bottom: 1px solid rgba(128,128,128,.12); }
.ai-inspect-row:last-child { border-bottom: none; }
.ai-inspect-main { flex: 1; min-width: 0; }
.ai-inspect-top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.ai-inspect-name { font-size: 13px; color: var(--color-ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ai-inspect-cron { font-size: 12px; font-family: ui-monospace, Consolas, monospace; color: #1677ff; background: rgba(22,119,255,.08); border-radius: 5px; padding: 1px 7px; flex-shrink: 0; }
.ai-inspect-meta { display: flex; align-items: center; gap: 6px; margin-top: 5px; flex-wrap: wrap; }
.ai-inspect-next { font-size: 11px; color: var(--color-slate); }
</style>
