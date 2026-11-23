import axios from 'axios'
import { message } from 'ant-design-vue'

export const http = axios.create({ baseURL: '', timeout: 30000, withCredentials: true })

http.interceptors.response.use(
  (resp) => resp,
  (err) => {
    const status = err.response?.status
    const msg = err.response?.data?.error || err.message
    const silent = (err.config as any)?.silent === true
    if (status === 401 && !location.pathname.startsWith('/login')) {
      location.href = '/login'
    } else if (msg && !silent) {
      message.error(String(msg).slice(0, 200))
    }
    return Promise.reject(err)
  },
)

export const get = <T = any>(url: string, params?: any) => http.get<T>(url, { params }).then(r => r.data)
// 轮询/后台刷新用：失败不弹全局 toast，由调用方就地展示，避免同一个错误每 15s 弹一次
export const getQuiet = <T = any>(url: string, params?: any) =>
  http.get<T>(url, { params, silent: true } as any).then(r => r.data)
export const post = <T = any>(url: string, data?: any) => http.post<T>(url, data).then(r => r.data)
export const put = <T = any>(url: string, data?: any) => http.put<T>(url, data).then(r => r.data)
export const del = <T = any>(url: string) => http.delete<T>(url).then(r => r.data)
export const upload = (url: string, file: File, field = 'file') => {
  const fd = new FormData()
  fd.append(field, file)
  return http.post(url, fd, { headers: { 'Content-Type': 'multipart/form-data' } }).then(r => r.data)
}

// WebSocket 主题订阅（统一 /api/ws?topic=xxx，带自动重连）。
// 重连用指数退避（1s→2s→4s…上限 30s + 随机抖动，连上即重置）：
// 固定 3s 重试在后端宕机/网络中断时会形成重连风暴，白耗前后端 CPU。
export function wsTopic(topic: string, onData: (msg: any) => void, isBinary = false): () => void {
  let ws: WebSocket | null = null
  let closed = false
  let timer: any = null
  let attempt = 0
  const connect = () => {
    if (closed) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    ws = new WebSocket(`${proto}://${location.host}/api/ws?topic=${encodeURIComponent(topic)}`)
    ws.onopen = () => { attempt = 0 }
    ws.onmessage = (ev) => {
      if (isBinary) onData(ev.data)
      else {
        try { onData(JSON.parse(ev.data)) } catch { /* ignore */ }
      }
    }
    ws.onclose = () => {
      if (closed) return
      attempt++
      const delay = Math.min(30000, 1000 * 2 ** Math.min(attempt, 5)) + Math.random() * 500
      timer = setTimeout(connect, delay)
    }
    ws.onerror = () => ws?.close()
  }
  connect()
  return () => { closed = true; clearTimeout(timer); ws?.close() }
}

// SSE 事件流订阅（同源 EventSource 自动携带登录 Cookie）。
// 看门狗：超过 STALL_MS 没有任何 message 就主动重建连接。后端 summary 每 10s 一帧、
// 心跳 15s 一帧（注释帧不触发 message），35s 静默即视为流僵死——自愈代理超时断流、
// 连接被中间层静默丢弃、原生重连失效等一切「流死了但没人重建」的场景。
export function sseTopic(url: string, onData: (msg: any) => void): () => void {
  let es: EventSource | null = null
  let closed = false
  let watchdog: any = null
  const STALL_MS = 35000
  const arm = () => {
    clearTimeout(watchdog)
    watchdog = setTimeout(() => {
      if (closed) return
      es?.close()
      connect() // 僵死自愈：重建连接
    }, STALL_MS)
  }
  const connect = () => {
    if (closed) return
    es = new EventSource(url)
    es.onmessage = (ev) => {
      arm()
      try { onData(JSON.parse(ev.data)) } catch { /* ignore */ }
    }
    es.onerror = () => { if (closed) es?.close() } // 未手动关闭时交给原生自动重连，看门狗兜底
    arm()
  }
  connect()
  return () => { closed = true; clearTimeout(watchdog); es?.close() }
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
