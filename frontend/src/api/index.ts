import axios from 'axios'
import { message } from 'ant-design-vue'

export const http = axios.create({ baseURL: '', timeout: 30000, withCredentials: true })

http.interceptors.response.use(
  (resp) => resp,
  (err) => {
    const status = err.response?.status
    const msg = err.response?.data?.error || err.message
    if (status === 401 && !location.pathname.startsWith('/login')) {
      location.href = '/login'
    } else if (msg) {
      message.error(String(msg).slice(0, 200))
    }
    return Promise.reject(err)
  },
)

export const get = <T = any>(url: string, params?: any) => http.get<T>(url, { params }).then(r => r.data)
export const post = <T = any>(url: string, data?: any) => http.post<T>(url, data).then(r => r.data)
export const put = <T = any>(url: string, data?: any) => http.put<T>(url, data).then(r => r.data)
export const del = <T = any>(url: string) => http.delete<T>(url).then(r => r.data)
export const upload = (url: string, file: File, field = 'file') => {
  const fd = new FormData()
  fd.append(field, file)
  return http.post(url, fd, { headers: { 'Content-Type': 'multipart/form-data' } }).then(r => r.data)
}

// WebSocket 主题订阅（统一 /api/ws?topic=xxx，带自动重连）
export function wsTopic(topic: string, onData: (msg: any) => void, isBinary = false): () => void {
  let ws: WebSocket | null = null
  let closed = false
  let timer: any = null
  const connect = () => {
    if (closed) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    ws = new WebSocket(`${proto}://${location.host}/api/ws?topic=${encodeURIComponent(topic)}`)
    ws.onmessage = (ev) => {
      if (isBinary) onData(ev.data)
      else {
        try { onData(JSON.parse(ev.data)) } catch { /* ignore */ }
      }
    }
    ws.onclose = () => { if (!closed) timer = setTimeout(connect, 3000) }
    ws.onerror = () => ws?.close()
  }
  connect()
  return () => { closed = true; clearTimeout(timer); ws?.close() }
}

export function fmtBytes(n: number): string {
  if (n == null) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return n.toFixed(i === 0 ? 0 : 1) + units[i]
}

export function fmtTime(t: string | number | Date): string {
  if (!t) return '-'
  const d = new Date(t)
  return d.toLocaleString('zh-CN', { hour12: false })
}
