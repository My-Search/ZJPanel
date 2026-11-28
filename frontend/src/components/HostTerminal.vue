<!--
  HostTerminal：主机页「终端」Tab 的内嵌终端。
  单会话（按当前主机创建），复用后端 /api/ws/terminal 协议；
  工具栏只保留关闭/文件管理/多标签入口（上传走文件面板拖拽），断线与关闭的反馈
  由后端/前端直接写入终端画面，不再用状态标签占位。
  默认不自动连接：进入 Tab 先显示「未开启」占位，由用户显式打开/关闭，
  避免浏览主机时悄悄建立常驻 SSH 会话造成资源浪费。
  会话按主机持久化：sessionId + xterm 画面快照存 localStorage，浏览器刷新后
  attach 原会话并增量回放（仅恢复用户自己开过且未关闭的会话，点过关闭就保持关闭）；
  多标签仍由完整终端页承担。
-->
<template>
  <div class="host-term">
    <!-- 默认不开启：占位 + 显式打开，避免浏览主机时悄悄建立常驻会话 -->
    <div v-if="!opened" class="ht-idle">
      <div class="ht-idle-box">
        <CodeOutlined class="ht-idle-ic" />
        <div class="ht-idle-title">终端未开启</div>
        <div class="ht-idle-sub">按需开启终端会话；会话建立后会常驻后端，可随时在工具栏关闭以释放资源。</div>
        <a-button type="primary" @click="open">
          <CodeOutlined /> 打开终端
        </a-button>
      </div>
    </div>
    <template v-else>
      <div class="ht-bar">
        <a-space size="small" wrap>
          <a-popconfirm
            title="关闭将结束后端终端会话（Shell 进程与连接一并释放），未保存的工作会丢失。"
            ok-text="关闭" cancel-text="取消" @confirm="closeTerminal"
          >
            <a-button size="small" danger><PoweroffOutlined /> 关闭终端</a-button>
          </a-popconfirm>
          <a-button
            v-if="!isLocal" size="small" :type="fileOpen ? 'primary' : 'default'"
            @click="toggleFile"
          >
            <FolderOpenOutlined /> 文件管理
          </a-button>
          <a-button size="small" type="link" @click="openFull">
            多标签终端 <ExportOutlined />
          </a-button>
          <span class="dim">{{ isLocal ? '面板容器内 Shell' : '文件面板支持拖拽上传 / 在线编辑' }}</span>
        </a-space>
      </div>

      <div class="ht-body">
        <div class="ht-main">
          <div ref="termEl" class="ht-term" @click="focusTerm" />
          <StatusStrip :stats="stats" />
        </div>
        <template v-if="fileOpen && !isLocal">
          <div class="ht-side">
            <TermFilePanel :host-id="hostId" />
          </div>
        </template>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, markRaw, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import { FolderOpenOutlined, ExportOutlined, CodeOutlined, PoweroffOutlined } from '@ant-design/icons-vue'
import StatusStrip from './StatusStrip.vue'
import TermFilePanel from './TermFilePanel.vue'
import { useTermTheme } from '../utils/termTheme'
import type { Terminal as XtermTerminal } from '@xterm/xterm'

const props = defineProps<{
  hostId: number | null
  local?: boolean
  name?: string
  active?: boolean
}>()

const { ui, current: termTheme } = useTermTheme()
const router = useRouter()
const isLocal = computed(() => !!props.local)
const hostId = computed(() => props.hostId)

const termEl = ref<HTMLElement>()
const fileOpen = ref(false)
const stats = ref<any>(null)
const state = ref<'connecting' | 'online' | 'reconnecting' | 'closed' | 'idle'>('idle')
// 终端开关：false 时显示「未开启」占位，不加载 xterm 也不建 WS（默认不打开，显式开/关）
const opened = ref(false)

let term: XtermTerminal | null = null
let fitAddon: any = null
let serialize: any = null
let ws: WebSocket | null = null
let sessionId = ''
let consumed = 0
let skipReplay = 0
let boundKey = '' // 当前会话在 localStorage 中的键（按主机隔离）
let dirty = false
let pendingWrites = 0 // xterm 异步写队列未排空数：快照必须与已解析内容对齐
let ro: ResizeObserver | null = null
let statsTimer: any = null
let snapshotTimer: any = null
let reconnectTimer: any = null
let disposed = false

// ===== 刷新恢复：会话句柄 + xterm 画面快照 =====
// 会话句柄按主机存 localStorage（只保存 id 与快照偏移，不存任何凭据）；
// 画面快照沿用完整终端页的 zj.term.snap.<会话ID> 格式，两个页面可互相恢复同一会话。
const sessKey = () => `zj.hostterm.session.${props.local ? 'local' : hostId.value || 0}`
const snapKey = (id: string) => `zj.term.snap.${id}`
const readSaved = (key: string): { id: string; c: number } | null => {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return null
    const o = JSON.parse(raw)
    if (o && typeof o.id === 'string' && o.id) return { id: o.id, c: Number(o.c) || 0 }
  } catch { /* 损坏按无会话处理 */ }
  return null
}
const readSnapshot = (id: string): { c: number; s: string } | null => {
  try {
    const raw = localStorage.getItem(snapKey(id))
    if (!raw) return null
    const o = JSON.parse(raw)
    if (o && typeof o.c === 'number' && typeof o.s === 'string') {
      if (o.t && Date.now() - o.t > 86400_000) { // 快照超过 24 小时视为过期（后端会话大概率已不在）
        localStorage.removeItem(snapKey(id))
        return null
      }
      return o
    }
  } catch { /* 快照损坏走全量回放 */ }
  return null
}
const dropSnapshot = (id?: string | null) => {
  if (!id) return
  try { localStorage.removeItem(snapKey(id)) } catch { /* ignore */ }
}
// 句柄与快照必须同源：句柄里的 c 只在快照写入成功时推进；
// 没有快照时 c=0，刷新后走服务端全量回放，避免「空白画面 + 只收增量」。
const persistNow = (withSnapshot = true) => {
  if (!sessionId) return
  const key = boundKey || sessKey()
  if (withSnapshot && serialize) {
    // 【关键】xterm 的 write 是异步队列，serialize 只读已解析的缓冲区。
    // 队列未排空时拍快照会缺尾部字节，却把断点推到 consumed 造成永久丢失；
    // 此时保留上一次「快照与断点同源」的副本，刷新后由服务端从旧断点补回尾部。
    if (pendingWrites > 0) return
    try {
      const s = serialize.serialize()
      localStorage.setItem(snapKey(sessionId), JSON.stringify({ c: consumed, s, t: Date.now() }))
      localStorage.setItem(key, JSON.stringify({ id: sessionId, c: consumed, t: Date.now() }))
      dirty = false
      return
    } catch { /* 序列化/配额失败：退回无快照模式 */ }
  }
  dropSnapshot(sessionId)
  try { localStorage.setItem(key, JSON.stringify({ id: sessionId, c: 0, t: Date.now() })) } catch { /* ignore */ }
}
const forgetSession = () => {
  const key = boundKey || sessKey()
  const saved = readSaved(key)
  if (saved) dropSnapshot(saved.id)
  if (sessionId) dropSnapshot(sessionId)
  try { localStorage.removeItem(key) } catch { /* ignore */ }
  boundKey = ''
}

const focusTerm = () => term?.focus()

// 统一写入口：跟踪 xterm 写队列，快照只在队列排空后拍
const termWrite = (data: string | Uint8Array) => {
  if (!term) return
  pendingWrites++
  term.write(data as any, () => { pendingWrites-- })
}

const ensureTerm = async () => {
  if (term || !termEl.value) return
  const xterm = await import('@xterm/xterm')
  const addon = await import('@xterm/addon-fit')
  const addonSer = await import('@xterm/addon-serialize')
  const t = new xterm.Terminal({
    fontSize: 13, cursorBlink: true, scrollback: 5000, theme: termTheme(),
  })
  const fit = new addon.FitAddon()
  const ser = new addonSer.SerializeAddon()
  t.loadAddon(fit)
  t.loadAddon(ser)
  t.open(termEl.value)
  // xterm 实例是命令式对象，不进 Vue 响应式代理，避免输出时逐帧走 Proxy
  term = markRaw(t)
  fitAddon = markRaw(fit)
  serialize = markRaw(ser)
  fit.fit()
  t.onData(d => { if (ws && ws.readyState === 1) ws.send(new TextEncoder().encode(d)) })
  t.onResize(({ cols, rows }) => {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
  })
  if (typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => fitSoon())
    ro.observe(termEl.value)
  }
}

let fitRaf = 0
const fitNow = () => {
  if (!term || !fitAddon) return
  try { fitAddon.fit() } catch { /* 容器隐藏时尺寸为 0，忽略 */ }
}
const fitSoon = () => {
  if (fitRaf) return
  fitRaf = requestAnimationFrame(() => { fitRaf = 0; fitNow() })
}

const clearStatsTimer = () => { if (statsTimer) { clearInterval(statsTimer); statsTimer = null } }
const clearReconnect = () => { if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null } }

// 彻底释放 xterm：实例与 DOM 元素绑定（t.open），v-if 折叠后元素重建，
// 旧实例必须丢弃（而非 reset），否则重新打开后画面挂不回新元素（黑屏）
const disposeTerm = () => {
  ro?.disconnect()
  ro = null
  term?.dispose()
  term = null
  serialize = null
}

const closeWS = () => {
  if (ws) {
    const old = ws
    ws = null
    try { old.close() } catch { /* ignore */ }
  }
  clearStatsTimer()
  clearReconnect()
}

const deleteSession = async () => {
  const sid = sessionId
  sessionId = ''
  if (!sid) return
  try { await fetch(`/api/terminal/sessions/${sid}`, { method: 'DELETE' }) } catch { /* ignore */ }
}

const sendResize = () => {
  if (!term || !ws || ws.readyState !== 1) return
  const { cols, rows } = term
  if (cols > 0 && rows > 0) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
}

// fresh=true 表示强制新建会话（主机切换/重连）；否则优先 attach 断点续传
const connect = (fresh: boolean) => {
  if (disposed) return
  if (!term || (!hostId.value && !props.local)) { state.value = 'idle'; return }
  if (!boundKey) boundKey = sessKey()
  closeWS()
  state.value = 'connecting'
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const sock = new WebSocket(`${proto}://${location.host}/api/ws/terminal`)
  sock.binaryType = 'arraybuffer'
  ws = markRaw(sock)
  let attached = false
  sock.onopen = () => {
    if (ws !== sock) return
    if (fresh || !sessionId) {
      sock.send(JSON.stringify(props.local || !hostId.value
        ? { type: 'create', kind: 'local' }
        : { type: 'create', kind: 'ssh', hostId: hostId.value }))
    } else {
      attached = true
      sock.send(JSON.stringify({ type: 'attach', sessionId, since: consumed > 0 ? consumed : undefined }))
    }
    sendResize()
    state.value = 'online'
  }
  sock.onmessage = (ev) => {
    if (ws !== sock) return
    if (ev.data instanceof ArrayBuffer) {
      const n = ev.data.byteLength
      // 回放帧已由服务端 offset 覆盖：不能再计入 consumed，否则断点会翻倍
      if (skipReplay > 0) skipReplay = Math.max(0, skipReplay - n)
      else consumed += n
      dirty = true
      termWrite(new Uint8Array(ev.data))
      return
    }
    let msg: any
    try { msg = JSON.parse(ev.data) } catch { return }
    switch (msg.type) {
      case 'created':
        sessionId = msg.session?.id || ''
        boundKey = sessKey()
        state.value = 'online'
        consumed = msg.offset || 0
        skipReplay = msg.replayBytes || 0
        dirty = true
        // 先只记会话 ID（c=0）：即使浏览器异常退出没有快照，刷新也能全量回放原会话
        persistNow(false)
        break
      case 'attached':
        sessionId = msg.sessionId || sessionId
        boundKey = boundKey || sessKey()
        state.value = 'online'
        consumed = msg.offset || 0
        skipReplay = msg.replayBytes || 0
        if (msg.gap) {
          // 断点已滚出环形缓冲：清掉旧画面，由随后的全量回放重建
          term?.reset()
          dirty = true
        }
        break
      case 'error':
        if (attached) {
          // 旧会话已被后端清理（重启/过期）：丢掉失效句柄与快照，清屏后立刻新建
          forgetSession()
          attached = false
          sessionId = ''
          consumed = 0
          skipReplay = 0
          term?.reset()
          connect(true)
          return
        }
        state.value = 'closed'
        message.error(msg.message || '终端会话创建失败')
        break
      case 'closed':
        forgetSession()
        sessionId = ''
        state.value = 'closed'
        // 状态标签已移除，终结反馈直接写进画面（与完整终端页一致）
        termWrite('\r\n\x1b[31m[会话已关闭，重新打开终端可建立新会话]\x1b[0m\r\n')
        break
      case 'disconnected':
        state.value = 'reconnecting'
        break
      case 'reconnected':
        state.value = 'online'
        break
      case 'stats':
        stats.value = msg.stats || null
        break
    }
  }
  sock.onclose = () => {
    // 旧连接的 close 可能晚于新连接建立：不是当前 socket 就直接丢弃，避免重复重连
    if (ws !== sock) return
    ws = null
    clearStatsTimer()
    if (disposed) return
    // 隐藏 Tab 不后台重连：回到终端时由 active 监听补建，避免堆积无人使用的会话
    if (!props.active) { state.value = 'idle'; return }
    if (state.value === 'closed') return
    state.value = 'reconnecting'
    clearReconnect()
    reconnectTimer = setTimeout(() => { reconnectTimer = null; connect(false) }, 3000)
  }
  // 与完整终端页一致：建立连接后每 5s 拉一次资源采样
  const pull = () => { if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'stats' })) }
  setTimeout(pull, 800)
  statsTimer = setInterval(pull, 5000)
}

const reset = async (deleteOld: boolean) => {
  closeWS()
  if (deleteOld) {
    await deleteSession()
    forgetSession()
  }
  sessionId = ''
  consumed = 0
  skipReplay = 0
  stats.value = null
  state.value = 'idle'
}

// 打开终端：占位按钮与主机列表「打开终端」共用（列表侧经 defineExpose 调用）。
// 有本地句柄（自己开过且未关闭的会话）则快照回填 + attach 恢复，否则新建会话。
const open = async () => {
  if (disposed) return
  if (opened.value) {
    // 已开启：只补连接（若已断）并聚焦，不重复建会话
    if (!ws && (hostId.value || props.local)) connect(sessionId === '')
    setTimeout(() => { fitNow(); term?.focus() }, 80)
    return
  }
  opened.value = true
  await nextTick() // 占位 v-if 切走后 .ht-term 才挂载，ensureTerm 需要真实 DOM
  await ensureTerm()
  const key = sessKey()
  boundKey = key
  const saved = readSaved(key)
  if (saved) {
    sessionId = saved.id
    const snap = readSnapshot(saved.id)
    if (snap) {
      consumed = snap.c
      termWrite(snap.s)
    }
    connect(false)
  } else {
    connect(true)
  }
  setTimeout(() => { fitNow(); term?.focus() }, 80)
}

// 显式关闭终端：结束后端会话（PTY/SSH、stats 采集一并停止）并清掉本地句柄与快照，
// 回到「未开启」占位。这是唯一真正释放资源的入口（重连是换新会话，不是关闭）。
const closeTerminal = async () => {
  await reset(true)
  fileOpen.value = false
  opened.value = false
  disposeTerm()
}

const persistOnLeave = () => persistNow(true)
const onVisibility = () => { if (document.hidden) persistNow(true) }

const toggleFile = () => {
  fileOpen.value = !fileOpen.value
  nextTick(() => setTimeout(fitNow, 60))
}

const openFull = () => {
  if (props.local) router.push({ path: '/terminal', query: { local: '1' } })
  else if (hostId.value) router.push({ path: '/terminal', query: { host: String(hostId.value) } })
}

onMounted(() => {
  // 监听与定时器常驻：persistNow/snapshotTimer 在未开启（无 sessionId）时都是空操作
  window.addEventListener('resize', fitSoon)
  // 刷新/关闭/后台化前立即落快照（序列化同步完成，不依赖异步请求）
  window.addEventListener('beforeunload', persistOnLeave)
  window.addEventListener('pagehide', persistOnLeave)
  document.addEventListener('visibilitychange', onVisibility)
  // 持续输出时定期落快照，浏览器异常退出也能回放到最近 30 秒
  snapshotTimer = setInterval(() => { if (dirty) persistNow(true) }, 30000)
  // 默认不连接。仅当终端 Tab 已激活且存在自己开过、未关闭的会话时自动恢复；
  // Tab 未激活时交给 active 监听触发，避免在主机列表浏览时后台悄悄建连。
  boundKey = sessKey()
  if (props.active && readSaved(boundKey)) open()
})

onUnmounted(() => {
  disposed = true
  // 先落快照再断开：会话留在后端，刷新或回到主机页都能 attach 回同一个 shell
  persistNow(true)
  window.removeEventListener('resize', fitSoon)
  window.removeEventListener('beforeunload', persistOnLeave)
  window.removeEventListener('pagehide', persistOnLeave)
  document.removeEventListener('visibilitychange', onVisibility)
  clearInterval(snapshotTimer)
  closeWS()
  disposeTerm()
})

// 切换主机：旧主机会话「分离」而非销毁——快照落盘、断开 WS、句柄保留，回到该主机可恢复；
// xterm 实例随占位折叠一并释放（重新打开时重建）。真正的销毁只由显式的「关闭终端」/「重连」触发。
// 新主机有未关闭会话则恢复，否则回未开启占位。
watch(hostId, async () => {
  persistNow(true)
  closeWS()
  sessionId = ''
  consumed = 0
  skipReplay = 0
  stats.value = null
  state.value = 'idle'
  disposeTerm()
  boundKey = sessKey()
  if (!readSaved(boundKey)) {
    opened.value = false
    return
  }
  if (props.active) await open()
  else opened.value = false // 不在终端 Tab：先折叠占位，切回来时由 active 监听恢复
})

// 从其它 Tab 切回来：容器此前隐藏导致尺寸为 0，需要重新 fit 并聚焦；
// 未开启但有历史会话（自己开过且未关闭）则恢复；已开启但连接断了则补建
watch(() => props.active, async (v) => {
  if (!v) return
  await nextTick()
  if (!opened.value) {
    if (readSaved(sessKey())) await open()
    return
  }
  if (!ws && (hostId.value || props.local)) connect(sessionId === '')
  setTimeout(() => { fitNow(); term?.focus() }, 80)
})

watch(() => ui.isDark, () => {
  if (term) term.options.theme = termTheme()
  fitSoon()
})

// 主机列表「打开终端」按钮 = 切 Tab + 直接建连（表达明确打开意图），经模板 ref 调用
defineExpose({ open })
</script>

<style scoped>
.host-term { display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0; }
.ht-idle { flex: 1; min-height: 0; display: flex; }
.ht-idle-box { margin: auto; display: flex; flex-direction: column; align-items: center; gap: 10px; max-width: 440px; padding: 24px; text-align: center; }
.ht-idle-ic { font-size: 34px; color: var(--color-slate); opacity: .8; }
.ht-idle-title { font-size: 16px; font-weight: 600; color: var(--color-ink); }
.ht-idle-sub { font-size: 12.5px; line-height: 1.7; color: var(--color-slate); margin-bottom: 6px; }
.ht-bar { flex: none; padding: 2px 0 8px; }
.dim { color: var(--color-slate); font-size: 12px; }
.ht-body { flex: 1; min-height: 0; min-width: 0; display: flex; gap: 10px; }
.ht-main { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 0; container-type: inline-size; }
.ht-term { flex: 1; min-height: 0; border-radius: 8px; overflow: hidden; padding: 6px 8px; background: #0d1117; }
html[data-theme='light'] .ht-term { background: #fff; }
html[data-theme='light'] .ht-side { background: #fff; }
.ht-side { width: min(420px, 45%); flex: none; min-height: 0; min-width: 0; overflow: hidden; }

@media (max-width: 900px) {
  /* 窄屏文件面板改为覆盖层，终端不再被挤成一条缝 */
  .ht-body { position: relative; }
  .ht-side { position: absolute; inset: 0 0 0 auto; width: min(92%, 420px); z-index: 30; background: var(--color-bg, #141414); border-radius: 8px; box-shadow: -8px 0 24px rgba(0,0,0,.28); overflow: hidden; }
}
</style>
