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
