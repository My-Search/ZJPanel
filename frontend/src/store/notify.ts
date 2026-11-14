import { defineStore } from 'pinia'
import { ref } from 'vue'
import { get, post, put } from '../api'

export interface NotifyItem {
  id: number
  type: string
  level: 'info' | 'warn' | 'error'
  title: string
  body: string
  source: string
  createdAt: string
  readAt: string | null
}

export interface NotifySettings {
  smtpHost: string
  smtpPort: number
  smtpUser: string
  hasPass: boolean
  smtpFrom: string
  rcpt: string
  subs: Record<string, boolean>
  types: string[]
}

/** 统一通知：未读计数 / 站内信列表 / 邮件订阅与 SMTP 设置。 */
export const useNotify = defineStore('notify', () => {
  const unread = ref(0)
  const list = ref<NotifyItem[]>([])
  const settings = ref<NotifySettings | null>(null)

  async function fetchUnread() {
    try { unread.value = (await get<any>('/api/notify/unread')).unread ?? 0 } catch { /* ignore */ }
  }
  async function fetchList(limit = 20) {
    const r = await get<any>(`/api/notify/list?limit=${limit}`)
    list.value = r.list || []
    unread.value = r.unread ?? 0
  }
  async function markRead(ids: number[]) {
    const r = await post<any>('/api/notify/read', { ids })
    unread.value = r.unread ?? 0
    const set = new Set(ids)
    list.value = list.value.map(it => (set.has(it.id) ? { ...it, readAt: new Date().toISOString() } : it))
  }
  async function markAll() {
    const r = await post<any>('/api/notify/read', { all: true })
    unread.value = r.unread ?? 0
    const now = new Date().toISOString()
    list.value = list.value.map(it => ({ ...it, readAt: now }))
  }
  async function loadSettings() {
    settings.value = await get<NotifySettings>('/api/notify/settings')
  }
  async function saveSettings(s: Record<string, any>) {
    await put('/api/notify/settings', s)
  }
  async function testMail(): Promise<{ ok: boolean; error?: string }> {
    return post('/api/notify/test', {})
  }

  return { unread, list, settings, fetchUnread, fetchList, markRead, markAll, loadSettings, saveSettings, testMail }
})
