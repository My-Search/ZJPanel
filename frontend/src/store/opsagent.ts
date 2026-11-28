import { reactive } from 'vue'
import { get, post, put, del } from '../api'

export interface OpsSession {
  id: number
  title: string
  source: string // manual | event:<topic> | inspect
  status: 'running' | 'waiting_confirm' | 'idle' | 'interrupted' | 'error'
  defenseMode: OpsMode
  hostId: number | null
  error: string
  createdAt: string
  updatedAt: string
  contextTokens?: number // 上下文用量估算（WS context 帧 / 会话详情回填）
  contextLimit?: number  // 模型上下文窗口配置
}

// 防御模式是会话级授权级别（工作台创建/切换；巡检任务各自指定；事件会话用全局默认）。
export type OpsMode = 'observe' | 'confirm' | 'auto'
export const OPS_MODE_OPTIONS: { value: OpsMode; label: string; hint: string }[] = [
  { value: 'observe', label: '仅观察', hint: '分析 + 通知 + 建议' },
  { value: 'confirm', label: '执行前确认', hint: '处置需人工放行' },
  { value: 'auto', label: '自动处置', hint: '直接执行（全程审计）' },
]
export const opsModeLabel = (m?: string) => OPS_MODE_OPTIONS.find(o => o.value === m)?.label || ''

export interface OpsMessage {
  id: number
  sessionId: number
  role: 'user' | 'assistant' | 'tool' | 'event' | 'summary'
  content: string
  reasoning?: string // 思考模型的推理过程（仅 assistant，前端折叠卡展示）
  toolName?: string
  toolArgs?: string
  meta?: string | null // JSON：assistant→{toolCalls:[…],streaming?}；tool→{callId,pending?}；summary→{toId,count}
  createdAt: string
}

export interface OpsEventDesc { topic: string; desc: string }

export interface OpsAgentSettings {
  llm: { baseURL: string; model: string; apiKeySet: boolean; ready: boolean; hint: string; contextTokens: number }
  agent: { defenseMode: string; eventEnabled: boolean; events: Record<string, boolean>; allowSSHExec: boolean }
  events: OpsEventDesc[]
}

export interface OpsMemory { id: number; scope: string; refId: string; key: string; content: string; updatedAt: string }

// 运维知识库条目：人工维护的操作规范/手册/约定（区别于智能体自动积累的记忆）。
// persistent=true 为常驻知识：每次会话自动注入系统提示词。
export interface OpsKnowledge { id: number; title: string; tags: string; content: string; persistent: boolean; updatedAt: string }

export interface InspectItem { key: string; title: string; hint: string }
export interface InspectTask { id: string; name: string; cron: string; items: string[]; hostId: number | null; defenseMode: OpsMode; enabled: boolean }
export interface InspectTaskView extends InspectTask {
  next: string // Go time.Time：零值 = "0001-01-01T00:00:00Z"（未排程）
}

/**
 * AI 运维智能体全局状态（模块级单例，语义等同 pinia store）。
 * 不用 defineStore 的原因：pinia 的 Store 交叉类型（UnwrapRef + PiniaCustomProperties）
 * 在模板表达式较多时会把 vue-tsc 的类型实例化推过深度上限，导致整个模板推断降级
 * 报出成片的假错误；这里用显式浅层接口 + reactive 对象，类型便宜且行为一致。
 *
 * 注意：方法必须直接读写 api 自身字段——不能再用「state 定义 + 展开进 reactive」的写法，
 * 展开是一次性浅拷贝，组件读到的 api.* 会与被修改的 state.* 脱钩，表现为接口全部成功但界面纹丝不动。
 */
export interface OpsAgentAPI {
  open: boolean
  sessions: OpsSession[]
  currentId: number | null
  messages: Record<number, OpsMessage[]>
  running: number // 运行中/待确认会话数（顶栏角标）
  settings: OpsAgentSettings | null
  memories: OpsMemory[]
  knowledge: OpsKnowledge[]
  inspects: InspectTaskView[]
  inspectCatalog: InspectItem[]
  fetchRunning(): Promise<void>
  fetchSessions(): Promise<void>
  fetchMessages(id: number, afterId?: number): Promise<OpsMessage[]>
  selectSession(id: number | null): void
  fetchSessionDetail(id: number): Promise<void>
  newSession(message: string, hostId?: number, defenseMode?: OpsMode): Promise<any>
  sendMessage(id: number, message: string): Promise<void>
  setSessionMode(id: number, mode: OpsMode): Promise<void>
  confirm(id: number, approve: boolean): Promise<void>
  stop(id: number): Promise<void>
  removeSession(id: number): Promise<void>
  loadSettings(): Promise<void>
  saveSettings(s: Record<string, any>): Promise<void>
  testLLM(): Promise<{ ok: boolean; error?: string }>
  loadMemories(query?: string): Promise<void>
  deleteMemory(id: number): Promise<void>
  loadKnowledge(query?: string): Promise<void>
  saveKnowledge(k: Partial<OpsKnowledge>): Promise<void>
  deleteKnowledge(id: number): Promise<void>
  loadInspects(): Promise<void>
  saveInspect(t: InspectTask): Promise<void>
  deleteInspect(id: string): Promise<void>
  runInspect(id: string): Promise<void>
  onWsFrame(frame: any): void
}

export const api: OpsAgentAPI = reactive({
  open: false,
  sessions: [] as OpsSession[],
  currentId: null as number | null,
  messages: {} as Record<number, OpsMessage[]>,
  running: 0,
  settings: null as OpsAgentSettings | null,
  memories: [] as OpsMemory[],
  knowledge: [] as OpsKnowledge[],
  inspects: [] as InspectTaskView[],
  inspectCatalog: [] as InspectItem[],

  async fetchRunning() {
    try { api.running = (await get<any>('/api/opsagent/running-count')).count ?? 0 } catch { /* ignore */ }
  },
  async fetchSessions() {
    try {
      api.sessions = await get<OpsSession[]>('/api/opsagent/sessions')
      api.running = api.sessions.filter(s => s.status === 'running' || s.status === 'waiting_confirm').length
    } catch { /* ignore */ }
  },
  async fetchMessages(id: number, afterId = 0) {
    const rows = await get<OpsMessage[]>(`/api/opsagent/sessions/${id}/messages?afterId=${afterId}`)
    const cur = api.messages[id] || []
    if (afterId > 0) {
      // WS 增量帧可能带来「已存在行的更新」（确认结果回写），按 id upsert
      const map = new Map(cur.map(m => [m.id, m]))
      for (const r of rows) map.set(r.id, r)
      api.messages[id] = [...map.values()].sort((a, b) => a.id - b.id)
    } else {
      api.messages[id] = rows
    }
    return rows
  },
  selectSession(id: number | null) {
    api.currentId = id
    if (id == null) return
    if (!api.messages[id]) void api.fetchMessages(id)
    void api.fetchSessionDetail(id) // 回填上下文用量等详情字段
  },
  async fetchSessionDetail(id: number) {
    try {
      const d = await get<any>(`/api/opsagent/sessions/${id}`)
      const s = api.sessions.find(x => x.id === id)
      if (s && d) {
        s.contextTokens = d.contextTokens
        s.contextLimit = d.contextLimit
      }
    } catch { /* ignore */ }
  },
  async newSession(message: string, hostId?: number, defenseMode?: OpsMode) {
    const r = await post<any>('/api/opsagent/sessions', { message, hostId: hostId || 0, defenseMode })
    await api.fetchSessions()
    api.selectSession(r.session.id)
    await api.fetchMessages(r.session.id)
    return r
  },
  async sendMessage(id: number, message: string) {
    await post(`/api/opsagent/sessions/${id}/messages`, { message })
    await api.fetchMessages(id)
  },
  async setSessionMode(id: number, mode: OpsMode) {
    await put(`/api/opsagent/sessions/${id}/mode`, { defenseMode: mode })
    const s = api.sessions.find(x => x.id === id)
    if (s) s.defenseMode = mode
  },
  async confirm(id: number, approve: boolean) {
    await post(`/api/opsagent/sessions/${id}/confirm`, { approve })
    await api.fetchMessages(id)
  },
  async stop(id: number) {
    await post(`/api/opsagent/sessions/${id}/stop`, {})
  },
  async removeSession(id: number) {
    await del(`/api/opsagent/sessions/${id}`)
    delete api.messages[id]
    if (api.currentId === id) api.currentId = null
    await api.fetchSessions()
  },
  async loadSettings() {
    api.settings = await get<OpsAgentSettings>('/api/opsagent/config')
  },
  async saveSettings(s: Record<string, any>) {
    await put('/api/opsagent/config', s)
    await api.loadSettings()
  },
  async testLLM(): Promise<{ ok: boolean; error?: string }> {
    return post('/api/opsagent/config/test', {})
  },
  async loadMemories(query = '') {
    api.memories = await get<OpsMemory[]>(`/api/opsagent/memories?query=${encodeURIComponent(query)}`)
  },
  async deleteMemory(id: number) {
    await del(`/api/opsagent/memories/${id}`)
    api.memories = api.memories.filter(m => m.id !== id)
  },
  async loadKnowledge(query = '') {
    api.knowledge = await get<OpsKnowledge[]>(`/api/opsagent/kb?query=${encodeURIComponent(query)}`)
  },
  async saveKnowledge(k: Partial<OpsKnowledge>) {
    await post('/api/opsagent/kb', k)
    await api.loadKnowledge()
  },
  async deleteKnowledge(id: number) {
    await del(`/api/opsagent/kb/${id}`)
    api.knowledge = api.knowledge.filter(k => k.id !== id)
  },
  async loadInspects() {
    const r = await get<any>('/api/opsagent/inspects')
    api.inspects = r.list || []
    api.inspectCatalog = r.catalog || []
  },
  async saveInspect(t: InspectTask) {
    await post('/api/opsagent/inspects', t)
    await api.loadInspects()
  },
  async deleteInspect(id: string) {
    await del(`/api/opsagent/inspects/${id}`)
    api.inspects = api.inspects.filter(t => t.id !== id)
  },
  async runInspect(id: string) {
    await post(`/api/opsagent/inspects/${id}/run`, {})
    await Promise.all([api.loadInspects(), api.fetchSessions()])
  },

  /**
   * WS 帧（后端主题 opsagent）：message / delta / status / session / context / mode。
   * message = 整行 upsert（流式定稿也走这里，整行替换）；delta = 流式增量追加。
   */
  onWsFrame(frame: any) {
    if (!frame || !frame.type) return
    if (frame.type === 'message' && frame.sessionId != null && frame.message) {
      const id: number = frame.sessionId
      const cur = api.messages[id] || []
      const map = new Map(cur.map(m => [m.id, m]))
      map.set(frame.message.id, frame.message)
      api.messages[id] = [...map.values()].sort((a, b) => a.id - b.id)
    } else if (frame.type === 'delta' && frame.sessionId != null && frame.messageId != null) {
      // 流式增量（path: content | reasoning）：按 messageId 追加；行缺失（如重连丢帧）时
      // 先补占位行再拉一次全量修复，之后的增量继续在本地追加
      const sid: number = frame.sessionId
      const cur = api.messages[sid] || []
      let row = cur.find(m => m.id === frame.messageId)
      if (!row) {
        row = {
          id: frame.messageId, sessionId: sid, role: 'assistant', content: '',
          meta: '{"streaming":true}', createdAt: new Date().toISOString(),
        }
        api.messages[sid] = [...cur, row].sort((a, b) => a.id - b.id)
        if (!missingFetch.has(sid)) {
          missingFetch.add(sid)
          void api.fetchMessages(sid).finally(() => missingFetch.delete(sid))
        }
      }
      if (frame.path === 'reasoning') row.reasoning = (row.reasoning || '') + (frame.append || '')
      else row.content += frame.append || ''
    } else if (frame.type === 'status' && frame.sessionId != null) {
      const s = api.sessions.find(x => x.id === frame.sessionId)
      if (s) { s.status = frame.status; s.error = frame.error || '' }
      void api.fetchRunning()
    } else if (frame.type === 'mode' && frame.sessionId != null) {
      // 会话级防御模式切换（工作台操作后回帧；其他标签页同步）
      const s = api.sessions.find(x => x.id === frame.sessionId)
      if (s && frame.defenseMode) s.defenseMode = frame.defenseMode
    } else if (frame.type === 'context' && frame.sessionId != null) {
      // 每轮 LLM 调用后的上下文用量（估算 tokens / 配置窗口）
      const s = api.sessions.find(x => x.id === frame.sessionId)
      if (s) { s.contextTokens = frame.tokens; s.contextLimit = frame.limit }
    } else if (frame.type === 'session' && frame.session) {
      if (!api.sessions.some(x => x.id === frame.session.id)) {
        api.sessions = [frame.session, ...api.sessions]
      }
      void api.fetchRunning()
    }
  },
})

export function useOpsAgent(): OpsAgentAPI {
  return api
}

// delta 帧发现行缺失时防重复全量修复的会话集合（fetch 进行中标记）
const missingFetch = new Set<number>()
