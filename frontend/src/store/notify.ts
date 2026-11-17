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
  smtpFromAlias: string
  rcpt: string
  subs: Record<string, boolean>
  types: string[]
}

/** 统一通知：未读计数 / 站内信列表 / 邮件订阅与 SMTP 设置。 */
export const useNotify = defineStore('notify', () => {
  const unread = ref(0)
  // 未读/已读分开缓存：默认只拉未读，切到已读 tab 才请求，避免无谓加载已读数据
  const unreadList = ref<NotifyItem[]>([])
  const readList = ref<NotifyItem[]>([])
  const settings = ref<NotifySettings | null>(null)

  async function fetchUnread() {
    try { unread.value = (await get<any>('/api/notify/unread')).unread ?? 0 } catch { /* ignore */ }
  }
  // scope: unread 仅未读 / read 仅已读
  async function fetchList(scope: 'unread' | 'read', limit = 30) {
    const r = await get<any>(`/api/notify/list?scope=${scope}&limit=${limit}`)
    const items: NotifyItem[] = r.list || []
    if (scope === 'unread') unreadList.value = items
    else readList.value = items
    unread.value = r.unread ?? 0
  }
  async function markRead(ids: number[]) {
    const r = await post<any>('/api/notify/read', { ids })
    unread.value = r.unread ?? 0
    // 从当前缓存移除：未读列表移除即可（已读缓存下次切 tab 会重新拉取）
    const set = new Set(ids)
    unreadList.value = unreadList.value.filter(it => !set.has(it.id))
  }
  async function markAll() {
    const r = await post<any>('/api/notify/read', { all: true })
    unread.value = r.unread ?? 0
    unreadList.value = []
    readList.value = [] // 全部已读后缓存失效，下次切 tab 重新拉
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

  return { unread, unreadList, readList, settings, fetchUnread, fetchList, markRead, markAll, loadSettings, saveSettings, testMail }
})
