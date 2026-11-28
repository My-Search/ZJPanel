import { nextTick, onUnmounted, ref, watch } from 'vue'

// 会话时间线底部锚定状态机（移植自 ZCode v4 timelineScrollAnchor 的纯函数语义，去虚拟化部分）。
//
// 语义（following）：
// - 用户位于底部 → following=true，新内容（新消息 / 流式 delta 撑高）自动贴底；
// - 用户上滚离底 → following=false，流式增量不得拉回阅读位置，出现「回到底部」按钮；
// - 用户手动滚回底部（或点按钮）→ 恢复跟随。
//
// following 表达用户滚动权，不是瞬时几何快照：只有真实用户滚动输入可以改变它；
// 程序化贴底产生的 scroll 事件只更新几何账目，不夺走滚动权。

/** 离底判定容差：小于该距离视为「在底部」。覆盖亚像素滚动与最后一行 padding。 */
export const BOTTOM_ANCHOR_EPSILON_PX = 48
/** 未观察滚动的判定容差：小于该值的 scrollTop 回退视为亚像素抖动，不算用户上滚。 */
export const UNOBSERVED_SCROLL_EPSILON_PX = 2
/** 用户滚动意图捕获的时效：超过该时间的历史意图不再参与对账。 */
const INTENT_TTL_MS = 1200
/** 程序化贴底的 scroll 事件豁免窗口：贴底写入 + 布局稳定判定。 */
const PROGRAMMATIC_GUARD_MS = 250

export interface ScrollMetrics {
  scrollTop: number
  viewportHeight: number
  contentHeight: number
}

export type UserScrollIntent = 'none' | 'awayFromBottom' | 'towardBottom' | 'unknown'

export function distanceToBottom(m: ScrollMetrics): number {
  return Math.max(0, m.contentHeight - m.viewportHeight - m.scrollTop)
}

export function isAtBottom(m: ScrollMetrics, epsilonPx = BOTTOM_ANCHOR_EPSILON_PX): boolean {
  return distanceToBottom(m) <= epsilonPx
}

/** wheel 的 deltaY 与 scrollTop 同向：负值阅读更早内容，正值靠近底部。 */
export function wheelIntent(deltaY: number): UserScrollIntent {
  if (deltaY < 0) return 'awayFromBottom'
  if (deltaY > 0) return 'towardBottom'
  return 'none'
}

/** touch 手指位移与 scrollTop 反向：手指下移表示阅读更早内容。 */
export function touchIntent(prevY: number, nextY: number): UserScrollIntent {
  if (nextY > prevY) return 'awayFromBottom'
  if (nextY < prevY) return 'towardBottom'
  return 'none'
}

/** 键盘滚动意图；输入控件内的光标按键不属于时间线滚动。 */
export function keyboardIntent(key: string, shiftKey: boolean, editableTarget: boolean): UserScrollIntent {
  if (editableTarget) return 'none'
  if (key === 'ArrowUp' || key === 'PageUp' || key === 'Home') return 'awayFromBottom'
  if (key === 'ArrowDown' || key === 'PageDown' || key === 'End') return 'towardBottom'
  if (key === ' ') return shiftKey ? 'awayFromBottom' : 'towardBottom'
  return 'none'
}

/**
 * 内容变化 commit 贴底前，对账用户滚动意图。
 * scroll 事件在滚动后的下一帧才派发：用户上滚（wheel）之后、事件派发之前若恰好落进
 * 一次流式 delta 撑高的 commit，贴底动作会拿着过期的 following=true 把阅读位置拽回去。
 * 对账规则：明确向上 → 立即解除；无输入 → 原样保持；未知/向下 → 落点在底则恢复，
 * 明显未观察的上滚（scrollTop 回退超过容差）则解除。
 */
export function reconcileFollowingForContentAnchor(input: {
  following: boolean
  metrics: ScrollMetrics
  lastObservedScrollTop: number
  userScrollIntent?: UserScrollIntent
}): boolean {
  const intent = input.userScrollIntent ?? 'unknown'
  if (intent === 'awayFromBottom') return false
  if (intent === 'none') return input.following
  if (isAtBottom(input.metrics)) return true
  const unobservedUpscroll =
    input.metrics.scrollTop < input.lastObservedScrollTop - UNOBSERVED_SCROLL_EPSILON_PX
  if (unobservedUpscroll) return false
  return input.following
}

/** 内容变化后的动作：跟随中 → 贴底；已解除 → 保持阅读位置（绝不拉回）。 */
export function anchorActionAfterContentChange(following: boolean): 'stickToBottom' | 'hold' {
  return following ? 'stickToBottom' : 'hold'
}

/**
 * 会话时间线滚动 composable：绑定滚动容器并管理底部锚定。
 * 组件只需在消息/增量渲染变化时调用 onContentChanged()，切换会话时调用 resetToBottom()。
 */
export function useTimelineScroll() {
  const scrollRef = ref<HTMLElement | null>(null)
  const showBackToBottom = ref(false)

  let following = true
  let lastObservedScrollTop = 0
  let intent: UserScrollIntent = 'none'
  let intentAt = 0
  let programmaticUntil = 0
  let touchY: number | null = null

  const metrics = (): ScrollMetrics => {
    const el = scrollRef.value
    return {
      scrollTop: el?.scrollTop ?? 0,
      viewportHeight: el?.clientHeight ?? 0,
      contentHeight: el?.scrollHeight ?? 0,
    }
  }

  const freshIntent = (): UserScrollIntent =>
    Date.now() - intentAt <= INTENT_TTL_MS ? intent : 'none'

  const updateButton = () => {
    const m = metrics()
    showBackToBottom.value = !following && m.contentHeight > m.viewportHeight + 120
  }

  /** 程序化贴底：瞬时写入（不用 smooth，中间帧会被误判为用户滚动），随后豁免 scroll 事件。 */
  const stickToBottom = () => {
    const el = scrollRef.value
    if (!el) return
    programmaticUntil = Date.now() + PROGRAMMATIC_GUARD_MS
    el.scrollTop = el.scrollHeight
    lastObservedScrollTop = el.scrollTop
  }

  const onScroll = () => {
    const el = scrollRef.value
    if (!el) return
    const m = metrics()
    const isProgrammatic = Date.now() < programmaticUntil
    lastObservedScrollTop = el.scrollTop
    if (!isProgrammatic) {
      // 用户滚动：贴底前对账捕获到的意图（滚动条拖动/键盘等未捕获来源按落点裁决）
      following = reconcileFollowingForContentAnchor({
        following,
        metrics: m,
        lastObservedScrollTop,
        userScrollIntent: freshIntent(),
      })
    }
    updateButton()
  }

  const onWheel = (e: WheelEvent) => {
    intent = wheelIntent(e.deltaY)
    intentAt = Date.now()
    if (intent === 'awayFromBottom') {
      // 明确向上：立即解除跟随，不等下一帧 scroll 事件（防止同帧流式 commit 夺回滚动权）
      following = false
      updateButton()
    }
  }

  const onTouchStart = (e: TouchEvent) => {
    touchY = e.touches[0]?.clientY ?? null
  }
  const onTouchMove = (e: TouchEvent) => {
    const y = e.touches[0]?.clientY
    if (touchY != null && y != null) {
      intent = touchIntent(touchY, y)
      intentAt = Date.now()
      if (intent === 'awayFromBottom') {
        following = false
        updateButton()
      }
    }
    touchY = y ?? null
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const target = e.target as HTMLElement | null
    const editable = !!target && (target.isContentEditable || /^(input|textarea|select)$/i.test(target.tagName))
    const it = keyboardIntent(e.key, e.shiftKey, editable)
    if (it === 'none') return
    // 键盘滚动仅当焦点位于时间线容器内（或其子元素）时生效，避免抢全局按键
    const el = scrollRef.value
    if (!el || !(el === document.activeElement || el.contains(document.activeElement))) return
    intent = it
    intentAt = Date.now()
    if (it === 'awayFromBottom') {
      following = false
      updateButton()
    }
  }

  /** 内容变化（新消息/流式增量）后的贴底裁决：仅跟随时贴底，绝不拉回。 */
  const onContentChanged = () => {
    void nextTick(() => {
      following = reconcileFollowingForContentAnchor({
        following,
        metrics: metrics(),
        lastObservedScrollTop,
        userScrollIntent: freshIntent(),
      })
      if (anchorActionAfterContentChange(following) === 'stickToBottom') stickToBottom()
      updateButton()
    })
  }

  /** 手动回到底部（按钮/切换会话）：恢复跟随并贴底。 */
  const resetToBottom = () => {
    following = true
    intent = 'none'
    void nextTick(() => {
      stickToBottom()
      updateButton()
    })
  }

  const bind = (el: HTMLElement | null) => {
    if (!el) return
    el.addEventListener('scroll', onScroll, { passive: true })
    el.addEventListener('wheel', onWheel, { passive: true, capture: true })
    el.addEventListener('touchstart', onTouchStart, { passive: true })
    el.addEventListener('touchmove', onTouchMove, { passive: true })
    window.addEventListener('keydown', onKeyDown)
  }
  const unbind = (el: HTMLElement | null) => {
    if (!el) return
    el.removeEventListener('scroll', onScroll)
    el.removeEventListener('wheel', onWheel, true)
    el.removeEventListener('touchstart', onTouchStart)
    el.removeEventListener('touchmove', onTouchMove)
    window.removeEventListener('keydown', onKeyDown)
  }

  watch(scrollRef, (el, old) => {
    if (old) unbind(old)
    bind(el)
  })
  onUnmounted(() => unbind(scrollRef.value))

  /** 函数式模板 ref：模板里 `:ref="setScrollEl"` 绑定滚动容器 */
  const setScrollEl = (el: unknown) => {
    scrollRef.value = (el as HTMLElement | null) ?? null
  }

  // 临时调试探针（验证后移除）
  ;(window as any).__omScrollDebug = () => ({
    following, show: showBackToBottom.value, intent, intentAt, programmaticUntil, lastObservedScrollTop,
  })

  return { scrollRef, setScrollEl, showBackToBottom, onContentChanged, resetToBottom }
}
