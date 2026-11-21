import { onActivated, onDeactivated, onUnmounted } from 'vue'

// 统一轮询封装：浏览器标签页隐藏（切后台）或 keep-alive 页面切走时暂停定时器，
// 恢复可见/切回时立即执行一次并重启。组件卸载自动清理。
// 目的：后台标签页不再持续向后端发轮询请求，后端也据此（订阅数/查询热度）降频。
//
// 注意：onActivated/onDeactivated 仅在组件被 keep-alive 包裹时触发，未包裹时自然退化为
// 只受 visibilitychange 控制，两种场景都可安全使用。
// enabled：额外的运行门控（如「父级 tab 是否激活」），每次 tick/start 前惰性求值，
// 不满足时定时器空转不发请求；恢复时机由调用方驱动（resume/stop）。
export function usePolling(fn: () => unknown, intervalMs: number, opts: { immediate?: boolean; enabled?: () => boolean } = {}) {
  const immediate = opts.immediate !== false
  const enabled = opts.enabled ?? (() => true)
  let timer: number | null = null
  let dead = false

  const tick = () => {
    if (!enabled()) return
    try {
      const r = fn()
      if (r instanceof Promise) r.catch(() => { /* 轮询失败静默，等下一轮 */ })
    } catch { /* 轮询失败静默 */ }
  }
  const stop = () => {
    if (timer !== null) {
      window.clearInterval(timer)
      timer = null
    }
  }
  const start = () => {
    if (dead || timer !== null || document.hidden || !enabled()) return
    timer = window.setInterval(tick, intervalMs)
  }
  // 从「暂停态」恢复：立即刷一次数据再重启（keep-alive 切回/标签页切回都需要新鲜数据）
  const resume = () => {
    if (dead) return
    tick()
    start()
  }

  const onVis = () => (document.hidden ? stop() : resume())
  document.addEventListener('visibilitychange', onVis)
  onActivated(resume)
  onDeactivated(stop)
  onUnmounted(() => {
    dead = true
    document.removeEventListener('visibilitychange', onVis)
    stop()
  })

  if (immediate) tick()
  start()
  return { stop, resume }
}

// 长连接（WS/SSE）生命周期管理：keep-alive 切走或标签页隐藏时断开，切回/恢复可见时重连。
// 断开后后端订阅数归零，推送侧采集/解析随之降频或停止——空闲节省的关键联动点。
export function useVisibleConn(start: () => (() => void) | void) {
  let stopFn: (() => void) | null = null
  let dead = false
  let active = true // keep-alive 激活状态（未被 keep-alive 包裹时保持 true）
  const open = () => {
    if (dead || stopFn) return
    stopFn = start() || null
  }
  const close = () => {
    stopFn?.()
    stopFn = null
  }
  const onVis = () => {
    if (document.hidden) close()
    else if (active) open()
  }
  document.addEventListener('visibilitychange', onVis)
  onActivated(() => { active = true; open() })
  onDeactivated(() => { active = false; close() })
  onUnmounted(() => {
    dead = true
    document.removeEventListener('visibilitychange', onVis)
    close()
  })
  if (!document.hidden) open()
  return { close, open }
}
