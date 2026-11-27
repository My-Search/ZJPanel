<!--
  HostTerminal：主机页「终端」Tab 的内嵌终端。
  单会话（按当前主机创建），复用后端 /api/ws/terminal 协议；
  自带文件管理/上传入口与底部资源状态栏，避免为了传文件再跳回旧终端页。
  会话按主机持久化：sessionId + xterm 画面快照存 localStorage，浏览器刷新后
  attach 原会话并增量回放，shell 进程与滚屏内容都不丢；多标签仍由完整终端页承担。
-->
<template>
  <div class="host-term">
    <div class="ht-bar">
      <a-space size="small" wrap>
        <a-tag :color="stateColor" class="ht-state">{{ stateText }}</a-tag>
        <a-button size="small" @click="reconnect">重连</a-button>
        <a-button
          v-if="!isLocal" size="small" :type="fileOpen ? 'primary' : 'default'"
          @click="toggleFile"
        >
          <FolderOpenOutlined /> 文件管理
        </a-button>
        <a-button v-if="!isLocal" size="small" :disabled="!connected" @click="pickUpload">
          <UploadOutlined /> 上传文件
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
          <TermFilePanel ref="filePanel" :host-id="hostId" />
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, markRaw, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import { FolderOpenOutlined, UploadOutlined, ExportOutlined } from '@ant-design/icons-vue'
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
const filePanel = ref<any>(null)
const fileOpen = ref(false)
const stats = ref<any>(null)
const state = ref<'connecting' | 'online' | 'reconnecting' | 'closed' | 'idle'>('idle')
const connected = computed(() => state.value === 'online')

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

const stateText = computed(() => ({
  idle: '未连接', connecting: '连接中…', online: '已连接', reconnecting: '重连中…', closed: '已断开',
}[state.value]))
const stateColor = computed(() => state.value === 'online' ? 'green' : state.value === 'connecting' || state.value === 'reconnecting' ? 'orange' : 'default')

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

const reconnect = async () => {
  clearReconnect()
  closeWS()
  await deleteSession()
  forgetSession()
  consumed = 0
  skipReplay = 0
  term?.reset()
  stats.value = null
  connect(true)
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

const persistOnLeave = () => persistNow(true)
const onVisibility = () => { if (document.hidden) persistNow(true) }

const toggleFile = () => {
  fileOpen.value = !fileOpen.value
  nextTick(() => setTimeout(fitNow, 60))
}

const pickUpload = async () => {
  if (isLocal.value) { message.info('本机 Shell 不需要 SFTP 上传'); return }
  if (!fileOpen.value) {
    fileOpen.value = true
    await nextTick()
  }
  setTimeout(() => filePanel.value?.pickFiles?.(), 80)
}

const openFull = () => {
  if (props.local) router.push({ path: '/terminal', query: { local: '1' } })
  else if (hostId.value) router.push({ path: '/terminal', query: { host: String(hostId.value) } })
}

onMounted(async () => {
  await ensureTerm()
  // 刷新恢复：有本机保存的会话句柄就 attach，有快照先回填画面再按偏移增量续传
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
  }
  connect(false)
  window.addEventListener('resize', fitSoon)
  // 刷新/关闭/后台化前立即落快照（序列化同步完成，不依赖异步请求）
  window.addEventListener('beforeunload', persistOnLeave)
  window.addEventListener('pagehide', persistOnLeave)
  document.addEventListener('visibilitychange', onVisibility)
  // 持续输出时定期落快照，浏览器异常退出也能回放到最近 30 秒
  snapshotTimer = setInterval(() => { if (dirty) persistNow(true) }, 30000)
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
  ro?.disconnect()
  ro = null
  term?.dispose()
  term = null
  serialize = null
})

// 切换主机：显式换目标才销毁旧会话（每台主机最多保留一个内嵌会话）。
// 单纯离开主机页/刷新不销毁，由后端会话常驻承接恢复。
watch(hostId, async () => {
  await reset(true)
  term?.reset()
  if (termEl.value) { fitNow() }
  if (props.active) connect(true)
})

// 从其它 Tab 切回来：容器此前隐藏导致尺寸为 0，需要重新 fit 并聚焦；会话已断则补建
watch(() => props.active, async (v) => {
  if (!v) return
  await nextTick()
  if (!ws && (hostId.value || props.local)) connect(sessionId === '')
  setTimeout(() => { fitNow(); term?.focus() }, 80)
})

watch(() => ui.isDark, () => {
  if (term) term.options.theme = termTheme()
  fitSoon()
})
</script>

<style scoped>
.host-term { display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0; }
.ht-bar { flex: none; padding: 2px 0 8px; }
.ht-state { margin-inline-end: 0; }
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
