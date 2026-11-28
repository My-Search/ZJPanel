import { reactive } from 'vue'
import { get, post, put, del } from '../api'

export interface OpsSession {
  id: number
  title: string
  source: string // manual | event:<topic>
  status: 'running' | 'waiting_confirm' | 'idle' | 'interrupted' | 'error'
  defenseMode: 'observe' | 'confirm' | 'auto'
  hostId: number | null
  error: string
  createdAt: string
  updatedAt: string
}

export interface OpsMessage {
  id: number
  sessionId: number
  role: 'user' | 'assistant' | 'tool' | 'event'
  content: string
  toolName?: string
  toolArgs?: string
  meta?: string | null // JSON：assistant→{toolCalls:[…]}；tool→{callId,pending?}
  createdAt: string
}

export interface OpsEventDesc { topic: string; desc: string }

export interface OpsAgentSettings {
  llm: { baseURL: string; model: string; apiKeySet: boolean; ready: boolean; hint: string }
  agent: { defenseMode: string; eventEnabled: boolean; events: Record<string, boolean>; allowSSHExec: boolean }
  events: OpsEventDesc[]
}

export interface OpsMemory { id: number; scope: string; refId: string; key: string; content: string; updatedAt: string }

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
  fetchRunning(): Promise<void>
  fetchSessions(): Promise<void>
  fetchMessages(id: number, afterId?: number): Promise<OpsMessage[]>
  selectSession(id: number | null): void
  newSession(message: string, hostId?: number): Promise<any>
  sendMessage(id: number, message: string): Promise<void>
  confirm(id: number, approve: boolean): Promise<void>
  stop(id: number): Promise<void>
  removeSession(id: number): Promise<void>
  loadSettings(): Promise<void>
  saveSettings(s: Record<string, any>): Promise<void>
  testLLM(): Promise<{ ok: boolean; error?: string }>
  loadMemories(query?: string): Promise<void>
  deleteMemory(id: number): Promise<void>
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
    if (id != null && !api.messages[id]) void api.fetchMessages(id)
  },
  async newSession(message: string, hostId?: number) {
    const r = await post<any>('/api/opsagent/sessions', { message, hostId: hostId || 0 })
    await api.fetchSessions()
    api.selectSession(r.session.id)
    await api.fetchMessages(r.session.id)
    return r
  },
  async sendMessage(id: number, message: string) {
    await post(`/api/opsagent/sessions/${id}/messages`, { message })
    await api.fetchMessages(id)
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

  /** WS 帧（后端主题 opsagent）：message / status / session。 */
  onWsFrame(frame: any) {
    if (!frame || !frame.type) return
    if (frame.type === 'message' && frame.sessionId != null && frame.message) {
      const id: number = frame.sessionId
      const cur = api.messages[id] || []
      const map = new Map(cur.map(m => [m.id, m]))
      map.set(frame.message.id, frame.message)
      api.messages[id] = [...map.values()].sort((a, b) => a.id - b.id)
    } else if (frame.type === 'status' && frame.sessionId != null) {
      const s = api.sessions.find(x => x.id === frame.sessionId)
      if (s) { s.status = frame.status; s.error = frame.error || '' }
      void api.fetchRunning()
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
