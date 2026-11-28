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

const state = reactive({
  open: false,
  sessions: [] as OpsSession[],
  currentId: null as number | null,
  messages: {} as Record<number, OpsMessage[]>,
  running: 0,
  settings: null as OpsAgentSettings | null,
  memories: [] as OpsMemory[],
})

async function fetchRunning() {
  try { state.running = (await get<any>('/api/opsagent/running-count')).count ?? 0 } catch { /* ignore */ }
}
async function fetchSessions() {
  try {
    state.sessions = await get<OpsSession[]>('/api/opsagent/sessions')
    state.running = state.sessions.filter(s => s.status === 'running' || s.status === 'waiting_confirm').length
  } catch { /* ignore */ }
}
async function fetchMessages(id: number, afterId = 0) {
  const rows = await get<OpsMessage[]>(`/api/opsagent/sessions/${id}/messages?afterId=${afterId}`)
  const cur = state.messages[id] || []
  if (afterId > 0) {
    // WS 增量帧可能带来「已存在行的更新」（确认结果回写），按 id upsert
    const map = new Map(cur.map(m => [m.id, m]))
    for (const r of rows) map.set(r.id, r)
    state.messages[id] = [...map.values()].sort((a, b) => a.id - b.id)
  } else {
    state.messages[id] = rows
  }
  return rows
}
function selectSession(id: number | null) {
  state.currentId = id
  if (id != null && !state.messages[id]) fetchMessages(id)
}
async function newSession(message: string, hostId?: number) {
  const r = await post<any>('/api/opsagent/sessions', { message, hostId: hostId || 0 })
  await fetchSessions()
  selectSession(r.session.id)
  await fetchMessages(r.session.id)
  return r
}
async function sendMessage(id: number, message: string) {
  await post(`/api/opsagent/sessions/${id}/messages`, { message })
  await fetchMessages(id)
}
async function confirm(id: number, approve: boolean) {
  await post(`/api/opsagent/sessions/${id}/confirm`, { approve })
  await fetchMessages(id)
}
async function stop(id: number) {
  await post(`/api/opsagent/sessions/${id}/stop`, {})
}
async function removeSession(id: number) {
  await del(`/api/opsagent/sessions/${id}`)
  delete state.messages[id]
  if (state.currentId === id) state.currentId = null
  await fetchSessions()
}
async function loadSettings() {
  state.settings = await get<OpsAgentSettings>('/api/opsagent/config')
}
async function saveSettings(s: Record<string, any>) {
  await put('/api/opsagent/config', s)
  await loadSettings()
}
async function testLLM(): Promise<{ ok: boolean; error?: string }> {
  return post('/api/opsagent/config/test', {})
}
async function loadMemories(query = '') {
  state.memories = await get<OpsMemory[]>(`/api/opsagent/memories?query=${encodeURIComponent(query)}`)
}
async function deleteMemory(id: number) {
  await del(`/api/opsagent/memories/${id}`)
  state.memories = state.memories.filter(m => m.id !== id)
}

/** WS 帧（后端主题 opsagent）：message / status / session。 */
function onWsFrame(frame: any) {
  if (!frame || !frame.type) return
  if (frame.type === 'message' && frame.sessionId != null && frame.message) {
    const id: number = frame.sessionId
    const cur = state.messages[id] || []
    const map = new Map(cur.map(m => [m.id, m]))
    map.set(frame.message.id, frame.message)
    state.messages[id] = [...map.values()].sort((a, b) => a.id - b.id)
  } else if (frame.type === 'status' && frame.sessionId != null) {
    const s = state.sessions.find(x => x.id === frame.sessionId)
    if (s) { s.status = frame.status; s.error = frame.error || '' }
    fetchRunning()
  } else if (frame.type === 'session' && frame.session) {
    if (!state.sessions.some(x => x.id === frame.session.id)) {
      state.sessions = [frame.session, ...state.sessions]
    }
    fetchRunning()
  }
}

const api: OpsAgentAPI = reactive({
  ...state,
  fetchRunning, fetchSessions, fetchMessages, selectSession,
  newSession, sendMessage, confirm, stop, removeSession,
  loadSettings, saveSettings, testLLM, loadMemories, deleteMemory, onWsFrame,
})

export function useOpsAgent(): OpsAgentAPI {
  return api
}
