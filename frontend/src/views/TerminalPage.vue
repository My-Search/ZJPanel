<template>
  <a-card size="small" class="terminal-card" :body-style="{ padding: '12px' }">
    <a-space v-if="tabs.length" style="margin-bottom: 10px" wrap>
      <a-dropdown>
        <a-button type="primary">新建 SSH 会话 <DownOutlined /></a-button>
        <template #overlay>
          <a-menu>
            <a-menu-item v-for="h in hosts" :key="h.id" @click="newSsh(h)">
              {{ h.name }}（{{ h.username }}@{{ h.host }}:{{ h.port }}）
            </a-menu-item>
            <a-menu-item v-if="!hosts.length" disabled>暂无 SSH 主机，请先添加</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
      <a-button @click="newLocal">本地终端</a-button>
      <a-button :type="fileOpen ? 'primary' : 'default'" @click="toggleFilePanel" title="远程文件管理（跟随当前 SSH 标签）">
        <FolderOpenOutlined /> 文件管理
      </a-button>
      <a-button :type="qcOpen ? 'primary' : 'default'" @click="qcOpen = !qcOpen" title="常用命令一键执行">
        <ThunderboltOutlined /> 常用命令
      </a-button>
      <a-button @click="showHostMgr = true">管理 SSH 主机</a-button>
      <span class="dim">会话常驻后端 · 断线自动重连 · 刷新页面自动恢复</span>
    </a-space>

    <!-- 常用命令条 -->
    <TermQuickCmds v-if="qcOpen && tabs.length" class="qc-strip" :disabled="!activeSshTab" @exec="sendQuickCmd" />

    <!-- 工作台主体：左终端 + 右文件面板 -->
    <div class="term-body">
      <div class="term-main">
        <!-- 无会话时不渲染空标签栏，改为占满终端区的引导空态 -->
        <a-tabs v-if="tabs.length" v-model:activeKey="activeTab" type="editable-card" hide-add size="small" class="term-tabs" @edit="closeTab">
          <a-tab-pane v-for="t in tabs" :key="t.id" :tab="t.label + (t.down ? ' ⚠' : '')" class="term-pane">
            <div :ref="el => setTermBox(t.id, el)" class="term-box" :style="{ background: boxBg() }"></div>
            <StatusStrip :stats="t.stats" />
          </a-tab-pane>
        </a-tabs>
        <TermEmpty v-else :hosts="hosts" @connect="newSsh" @local="newLocal" @manage="showHostMgr = true" />
      </div>
      <template v-if="fileOpen && tabs.length">
        <div class="fm-divider" @pointerdown="startDividerDrag" title="拖拽调整宽度"></div>
        <div class="fm-side" :style="fmSideStyle">
          <TermFilePanel :host-id="activeHostId" />
        </div>
      </template>
    </div>

    <!-- SSH 主机管理 -->
    <a-modal v-model:open="showHostMgr" title="SSH 主机管理" :footer="null" :width="860">
      <a-button type="primary" style="margin-bottom: 12px" @click="openHostEditor()">添加主机</a-button>
      <a-table :data-source="hosts" :columns="hostCols" size="small" row-key="id" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'action'">
            <a-space>
              <a-button size="small" @click="openHostEditor(record)">编辑</a-button>
              <a-button size="small" :loading="testingId === record.id" @click="testHost(record)">测试</a-button>
              <a-popconfirm title="删除该主机？" @confirm="delHost(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-modal>

    <a-modal v-model:open="hostOpen" :title="editHost.id ? '编辑主机' : '添加主机'" @ok="saveHost">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="editHost.name" placeholder="生产服务器" /></a-form-item>
        <a-row :gutter="8">
          <a-col :span="12"><a-form-item label="主机"><a-input v-model:value="editHost.host" placeholder="1.2.3.4" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="端口"><a-input-number v-model:value="editHost.port" :min="1" :max="65535" style="width: 100%" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="8">
          <a-col :span="12"><a-form-item label="用户名"><a-input v-model:value="editHost.username" placeholder="root" /></a-form-item></a-col>
          <a-col :span="12">
            <a-form-item label="认证方式">
              <a-select v-model:value="editHost.authType" :options="[{ label: '密码', value: 'password' }, { label: '私钥', value: 'key' }]" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item v-if="editHost.authType === 'password'" label="密码（编辑时留空则不修改）">
          <a-input-password v-model:value="editHost.password" />
        </a-form-item>
        <template v-else>
          <a-form-item label="私钥（编辑时留空则不修改）"><a-textarea v-model:value="editHost.privateKey" :rows="5" /></a-form-item>
          <a-form-item label="私钥口令（可选）"><a-input-password v-model:value="editHost.passphrase" /></a-form-item>
        </template>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { markRaw, onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del } from '../api'
import { DownOutlined, FolderOpenOutlined, ThunderboltOutlined } from '@ant-design/icons-vue'
import { useTermTheme } from '../utils/termTheme'
import StatusStrip from '../components/StatusStrip.vue'
import TermFilePanel from '../components/TermFilePanel.vue'
import TermQuickCmds from '../components/TermQuickCmds.vue'
import TermEmpty from '../components/TermEmpty.vue'
import type { Terminal as XtermTerminal } from '@xterm/xterm'

const { ui, current: termTheme, boxBg } = useTermTheme()

interface TermStats {
  cpu: number; mem: number; memTxt: string
  disk: number; diskTxt: string
  netIn: number; netOut: number; load1: number
}

interface Tab {
  id: string; label: string; sessionId: string | null; hostId: number | null
  kind: 'ssh' | 'local'   // 会话类型：恢复失败自动重建时决定 create 参数
  ws: WebSocket | null; term: XtermTerminal | null; fit: { fit: () => void } | null
  created: boolean; stats: TermStats | null; statsTimer: any
  everReady: boolean // 已完成过一次 created/attached（之后 WS 重连不再写回放，避免重复内容）
  down: boolean      // 后端会话 SSH 断线、自动重连中（标签显示 ⚠）
  noRe: boolean      // 会话已终结（手动关闭/退出/不存在），不再自动重连
  restoring: boolean // 刷新页面恢复中：attach 失败（后端重启/会话过期）时自动重建会话而不是留死标签
  closing: boolean   // 标签正在销毁，禁止一切异步回调
  reconnTimer: any
  // 刷新恢复：快照 + 增量续传
  consumed: number      // 已消费的 PTY 输出流偏移（字节）：attach 的 since 断点
  skipReplay: number    // attach 回放帧剩余字节数（不计入 consumed，服务端 offset 已覆盖）
  pendingWrites: number // xterm 异步写队列未排空数；为 0 时才能拍快照
  lastWriteAt: number   // 最近一次 xterm 写入时间：快照只在输出停顿后拍，避免持续序列化卡 UI
  snapTimer: any        // 滚动对齐定时器：滚轮/拖拽停止后把视口吸附到整行
  serialize: any | null    // SerializeAddon 实例
  marker: string | null // 全量回放排空后补写的提示文本；无缝续传为 null（不插标记破坏 TUI 光标状态）
  ro: ResizeObserver | null // 监听终端容器尺寸（状态栏换行/文件面板/窗口变化都会改变它）
  roRaf: number
}

const hosts = ref<any[]>([])
const tabs = ref<Tab[]>([])
const activeTab = ref('')
const showHostMgr = ref(false)
const hostOpen = ref(false)
const editHost = ref<any>({})
const testingId = ref(0)
const termBoxes = new Map<string, HTMLElement>()

const hostCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 140 },
  { title: '地址', key: 'addr', width: 220, customRender: (o: any) => `${o.record.username}@${o.record.host}:${o.record.port}` },
  { title: '认证', dataIndex: 'authType', key: 'authType', width: 80 },
  { title: '操作', key: 'action', width: 200 },
]

const setTermBox = (id: string, el: any) => { if (el) termBoxes.set(id, el as HTMLElement) }

const loadHosts = async () => { hosts.value = await get('/api/ssh/hosts') }

// ===== 刷新恢复：快照 + 增量续传 =====
// localStorage 按会话 ID 存 xterm 缓冲快照（含光标位置/颜色/时间戳）与已消费偏移：
// 刷新/关闭标签后重新打开都能恢复画面与断开前一致，TUI 应用无感继续。
// 相比 sessionStorage：关闭 tab 后打开新 tab 也能利用快照恢复，不再降级全量回放。
const snapKey = (id: string) => `zj.term.snap.${id}`
const readSnapshot = (id: string): { c: number; s: string } | null => {
  try {
    const raw = localStorage.getItem(snapKey(id))
    if (!raw) return null
    const o = JSON.parse(raw)
    if (o && typeof o.c === 'number' && typeof o.s === 'string') {
      // 快照超过 24 小时视为过期（后端会话大概率已不在）
      if (o.t && Date.now() - o.t > 86400_000) {
        localStorage.removeItem(snapKey(id))
        return null
      }
      return o
    }
  } catch { /* 快照损坏按无快照处理，走回退路径 */ }
  return null
}
const dropSnapshot = (id?: string | null) => {
  if (!id) return
  try { localStorage.removeItem(snapKey(id)) } catch {}
}
// 启动时清理孤立快照：后端会话列表里没有的快照一律删除，防止 localStorage 堆积
const cleanOrphanSnapshots = (activeSids: string[]) => {
  try {
    const prefix = 'zj.term.snap.'
    const active = new Set(activeSids)
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i)
      if (k && k.startsWith(prefix) && !active.has(k.slice(prefix.length))) {
        localStorage.removeItem(k)
      }
    }
  } catch {}
}

// 统一写入口：跟踪 xterm 异步写排空——快照必须等队列排空，否则会缺尾部字节
const tWrite = (t: Tab, data: string | Uint8Array) => {
  if (!t.term) return
  t.lastWriteAt = Date.now()
  t.pendingWrites++
  t.term.write(data as any, () => {
    t.pendingWrites--
    scheduleSnapshot(t)
  })
}

// 立即拍快照（同步）：供 beforeunload 等需要即时保存的场景
const takeSnapshotNow = (t: Tab) => {
  if (!t.serialize || !t.sessionId || t.closing || t.noRe) return
  try {
    localStorage.setItem(snapKey(t.sessionId), JSON.stringify({
      c: t.consumed, s: t.serialize.serialize(), t: Date.now(),
    }))
  } catch { /* 配额满等：跳过 */ }
}

// 空闲快照：输出停顿 3s 后才序列化，序列化本身再放到浏览器空闲时段。
// 旧版每 800ms 就 serialize 5000 行 scrollback + 同步写 localStorage，
// 持续输出/连续回车时会把主线程占满，表现为输入和滚动都卡。
const SNAPSHOT_IDLE_MS = 3000
const scheduleSnapshot = (t: Tab) => {
  if (t.snapTimer || t.closing || t.noRe) return
  t.snapTimer = setTimeout(() => {
    t.snapTimer = null
    if (t.closing || t.noRe) return
    // 仍在输出或刚停顿不足 3s：1s 后再探，不做序列化
    if (t.pendingWrites > 0 || Date.now() - t.lastWriteAt < SNAPSHOT_IDLE_MS) {
      scheduleSnapshot(t)
      return
    }
    const run = () => {
      if (t.closing || t.noRe) return
      if (t.pendingWrites > 0 || Date.now() - t.lastWriteAt < SNAPSHOT_IDLE_MS) {
        scheduleSnapshot(t)
        return
      }
      takeSnapshotNow(t)
    }
    const ric = (window as any).requestIdleCallback
    if (typeof ric === 'function') ric(run, { timeout: 1500 })
    else setTimeout(run, 0)
  }, 1000)
}

// 滚动停止后把视口吸附到整行：xterm 的滚动位置允许小数像素，
// 停在两行之间时首行会被顶部裁掉一半（表现为「终端上面被盖住」）。
// 只处理滚轮/拖拽/翻页这类用户滚动，输出自动滚到底不受影响。
const snapScroll = (t: Tab) => {
  if (t.snapTimer) clearTimeout(t.snapTimer)
  t.snapTimer = setTimeout(() => {
    t.snapTimer = null
    if (t.closing || !t.term) return
    const b = t.term.buffer.active
    if (b.viewportY >= b.baseY) t.term.scrollToBottom()
    else t.term.scrollToLine(b.viewportY)
  }, 140)
}

// fit 后按行对齐视口：容器高度变化（状态栏换行、文件面板、窗口缩放）时，
// 旧版只调 fit()，xterm 的滚动位置可能停在半行，顶部首行被切。
const fitTerm = (t: Tab) => {
  if (!t.term || !t.fit) return
  const b = t.term.buffer.active
  const y = b.viewportY
  const atBottom = y >= b.baseY
  t.fit.fit()
  if (atBottom) t.term.scrollToBottom()
  else t.term.scrollToLine(y)
}

// 首次为标签创建 xterm 实例；WS 断开重连时复用同一实例（不清屏、不重复挂事件）
const ensureTerm = async (t: Tab) => {
  if (t.term) return
  await nextTick()
  const xterm = await import('@xterm/xterm')
  const fitAddon = await import('@xterm/addon-fit')
  const serializeAddon = await import('@xterm/addon-serialize')
  const term = new xterm.Terminal({
    fontSize: 13, cursorBlink: true, scrollback: 5000,
    theme: termTheme(),
  })
  const fit = new fitAddon.FitAddon()
  term.loadAddon(fit)
  const box = termBoxes.get(t.id)
  if (!box) return
  term.open(box)
  // xterm/FitAddon/SerializeAddon 是纯命令式实例，不能进 Vue 深度响应式代理：
  // 否则每帧 write/send 都要穿过 Proxy，输出量大时明显拖慢。
  t.term = markRaw(term)
  t.fit = markRaw(fit)
  t.serialize = markRaw(new serializeAddon.SerializeAddon())
  term.loadAddon(t.serialize)
  fitTerm(t)
  setTimeout(() => fitTerm(t), 150)

  // 用户滚动结束后吸附整行，避免首行停在半行被顶部裁切
  term.element?.addEventListener('wheel', () => snapScroll(t), { passive: true, capture: true })
  term.element?.addEventListener('pointerdown', (ev) => {
    if ((ev.target as HTMLElement).closest('.scrollbar')) snapScroll(t)
  }, { passive: true, capture: true })
  term.onKey(({ domEvent }) => {
    if (domEvent.key === 'PageUp' || domEvent.key === 'PageDown' || domEvent.key === 'Home' || domEvent.key === 'End') snapScroll(t)
  })

  // 容器尺寸变化（状态栏换行、文件面板开合、窗口缩放）时重排：
  // 旧版只监听 window.resize，状态栏变高后终端不会重新 fit，底部行会被状态栏盖住。
  if (typeof ResizeObserver !== 'undefined') {
    t.ro = new ResizeObserver(() => {
      if (t.roRaf) return
      t.roRaf = requestAnimationFrame(() => {
        t.roRaf = 0
        if (!t.closing) fitTerm(t)
      })
    })
    t.ro.observe(box)
  }

  // 输入/尺寸发送到「当前活跃 ws」，重连后自动指向新连接
  term.onData((d: string) => {
    if (t.ws && t.ws.readyState === 1) t.ws.send(new TextEncoder().encode(d))
  })
  term.onResize(({ cols, rows }) => {
    if (t.ws && t.ws.readyState === 1) t.ws.send(JSON.stringify({ type: 'resize', cols, rows }))
  })
}

// 建立 WS 连接（新建 createMsg 或 attach 恢复）；断开后自动重连，直到标签关闭或会话终结
const dialWS = (t: Tab, createMsg: Record<string, any> | null) => {
  const term = t.term
  if (!term) return
  if (t.statsTimer) clearInterval(t.statsTimer)
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${location.host}/api/ws/terminal`)
  ws.binaryType = 'arraybuffer'
  t.ws = markRaw(ws)
  let sinceSent = false // 本次 attach 是否带了 since 断点（true=增量续传，false=全量回放）
  ws.onopen = () => {
    // attach 带 since：快照断点（刷新恢复）或已收偏移（断线重连）时增量续传，服务端只回放缺失字节
    const since = t.consumed > 0 ? t.consumed : undefined
    sinceSent = since !== undefined
    ws.send(JSON.stringify(createMsg ?? { type: 'attach', sessionId: t.sessionId, since }))
    // 恢复/重连后立即同步终端尺寸到服务端 PTY——
    // 浏览器窗口可能在断线期间被调整过大小，PTY 仍保持旧尺寸导致 TUI 画面错乱
    if (!createMsg && term) {
      const { cols, rows } = term
      if (cols > 0 && rows > 0) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    }
  }
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) {
      const n = ev.data.byteLength
      // 回放帧不计入 consumed（服务端 offset 已覆盖），广播帧推进断点
      if (t.skipReplay > 0) {
        t.skipReplay = Math.max(0, t.skipReplay - n)
        if (t.skipReplay === 0 && t.marker) {
          term.write('\r\n\x1b[36m[' + t.marker + ']\x1b[0m\r\n') // 排队在回放帧之后，不会插进内容中间
          t.marker = null
        }
      } else {
        t.consumed += n
      }
      tWrite(t, new Uint8Array(ev.data))
      return
    }
    let msg: any
    try { msg = JSON.parse(ev.data) } catch { return }
    switch (msg.type) {
      case 'created':
        t.sessionId = msg.session.id
        t.hostId = msg.session.hostId || null
        t.everReady = true
        t.restoring = false
        t.consumed = msg.offset || 0
        t.skipReplay = msg.replayBytes || 0
        term.write('\x1b[36m[已连接 ' + msg.session.label + ']\x1b[0m\r\n')
        break
      case 'attached': {
        const first = !t.everReady
        t.sessionId = msg.sessionId
        t.everReady = true
        t.restoring = false
        t.consumed = msg.offset || 0
        t.skipReplay = msg.replayBytes || 0
        if (msg.gap) term.reset() // 断点已失效（输出滚出环形缓冲）：清掉当前画面，由随后的全量回放帧重建
        // 全量回放（无快照恢复/断点失效）补提示；增量续传保持画面连续，不插标记破坏 TUI 渲染
        const full = !sinceSent || !!msg.gap
        t.marker = full ? (first ? '已恢复会话' : '已恢复连接') : null
        if (t.marker && !(msg.replayBytes > 0)) {
          term.write('\r\n\x1b[36m[' + t.marker + ']\x1b[0m\r\n') // 回放为空：立即提示，不用等排空
          t.marker = null
        }
        break
      }
      case 'error':
        term.write('\x1b[31m[错误] ' + msg.message + '\x1b[0m\r\n')
        t.noRe = true // 会话不存在或已终结：继续重连只会反复报错
        dropSnapshot(t.sessionId) // 原会话已清理：快照随之失效
        // 刷新页面恢复时，原会话可能已被清理（后端重启/会话过期）：
        // 有来源信息就自动重建同源会话，而不是留下一个死标签
        if (t.restoring) {
          t.restoring = false
          t.noRe = false
          term.write('\x1b[90m[原会话已失效，正在重新建立…]\x1b[0m\r\n')
          const createMsg = t.kind === 'ssh' && t.hostId
            ? { type: 'create', kind: 'ssh', hostId: t.hostId }
            : { type: 'create', kind: 'local' }
          t.reconnTimer = setTimeout(() => { t.reconnTimer = null; dialWS(t, createMsg) }, 500)
        }
        break
      case 'closed':
        term.write('\r\n\x1b[31m[会话已关闭]\x1b[0m\r\n')
        dropSnapshot(t.sessionId)
        t.noRe = true
        break
      case 'disconnected': // 后端 SSH 断线：会话保留、后台重连中
        t.down = true
        break
      case 'reconnected':
        t.down = false
        break
      case 'stats':
        // 底部状态栏数据（5s 轮询响应）
        t.stats = msg.stats || null
        break
    }
  }
  // 状态栏轮询：连接建立后每 5s 拉一次（与后端采集周期一致）
  const pullStats = () => { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'stats' })) }
  setTimeout(pullStats, 600)
  t.statsTimer = setInterval(pullStats, 5000)
  ws.onclose = () => {
    if (t.ws === ws) t.ws = null
    if (t.closing || t.noRe) return
    term.write('\x1b[90m[连接中断，正在重连…]\x1b[0m\r\n')
    t.reconnTimer = setTimeout(() => { t.reconnTimer = null; dialWS(t, null) }, 3000)
  }
}

// 新建（createMsg）或恢复（attach）一个标签的会话连接
const openSession = async (t: Tab, createMsg: Record<string, any> | null) => {
  await ensureTerm(t)
  dialWS(t, createMsg)
}

const addTab = (label: string) => {
  // push 后从响应式数组取回代理对象：直接用原始对象时 t.stats 赋值不触发更新，
  // 状态栏会一直停在「资源采集中」（切标签页等偶然重渲染才显示）
  const raw: Tab = {
    id: Math.random().toString(36).slice(2), label, sessionId: null, hostId: null, kind: 'local', ws: null, term: null, fit: null,
    created: false, stats: null, statsTimer: null,
    everReady: false, down: false, noRe: false, restoring: false, closing: false, reconnTimer: null,
    consumed: 0, skipReplay: 0, pendingWrites: 0, lastWriteAt: 0, snapTimer: null, serialize: null, marker: null,
    ro: null, roRaf: 0,
  }
  tabs.value.push(raw)
  const t = tabs.value[tabs.value.length - 1]
  activeTab.value = t.id
  return t
}

const newSsh = async (h: any) => {
  const t = addTab(`${h.name || h.host}`)
  t.kind = 'ssh'
  await openSession(t, { type: 'create', kind: 'ssh', hostId: h.id })
}
const newLocal = async () => {
  const t = addTab('本地终端')
  await openSession(t, { type: 'create', kind: 'local' })
}
const closeTab = async (id: string | number, action: string) => {
  if (action === 'remove') {
    const t = tabs.value.find(x => x.id === id)
    if (!t) return
    t.closing = true
    if (t.reconnTimer) { clearTimeout(t.reconnTimer); t.reconnTimer = null }
    if (t.snapTimer) { clearTimeout(t.snapTimer); t.snapTimer = null }
    if (t.roRaf) { cancelAnimationFrame(t.roRaf); t.roRaf = 0 }
    t.ro?.disconnect()
    t.ro = null
    if (t.statsTimer) clearInterval(t.statsTimer)
    dropSnapshot(t.sessionId) // 会话随标签删除：快照一并清理
    t.ws?.close()
    if (t.sessionId) await fetch(`/api/terminal/sessions/${t.sessionId}`, { method: 'DELETE' })
    t.term?.dispose()
    tabs.value = tabs.value.filter(x => x.id !== id)
    if (activeTab.value === t.id && tabs.value.length) activeTab.value = tabs.value[0].id
  } else if (typeof id === 'string') {
    activeTab.value = id
    const t = tabs.value.find(x => x.id === id)
    if (t) setTimeout(() => { fitTerm(t); t.term?.focus() }, 100)
  }
}

// 保存/恢复：刷新页面后从后端会话列表恢复标签（含断线重连中的会话）
const restoreSessions = async () => {
  const list = await get<any[]>('/api/terminal/sessions')
  // 清理 localStorage 中不再有对应后端会话的孤立快照
  cleanOrphanSnapshots((list || []).map((s: any) => s.id))
  for (const s of list || []) {
    const t = addTab(s.label)
    t.sessionId = s.id // 关键：attach 必须带会话 ID——此前遗漏导致刷新恢复永远报「会话不存在」
    t.down = !!s.disconnected
    t.hostId = s.hostId || null
    t.kind = s.kind === 'local' ? 'local' : 'ssh'
    t.restoring = true // attach 失败（后端重启等会话被清理）时由 error 分支自动重建
    // 有快照：先写快照（覆盖 [0, c]），attach 带 since=c 增量续传 (c, end]——画面与断开前一致
    // 无快照：attach 不带 since，服务端全量回放 + SIGWINCH 抖动让 TUI 整屏重画（回退路径）
    const snap = readSnapshot(s.id)
    await ensureTerm(t)
    if (snap) {
      t.consumed = snap.c
      tWrite(t, snap.s)
    }
    dialWS(t, null) // attach 模式
  }
}

// ===== 工作台：右侧文件面板 + 常用命令条 =====

// 文件面板：绑定当前激活的 SSH 标签主机（本地终端/无标签时空态）
const activeSshTab = computed(() => {
  const t = tabs.value.find(x => x.id === activeTab.value)
  return t && t.hostId ? t : null
})
const activeHostId = computed(() => activeSshTab.value?.hostId ?? null)

const fileOpen = ref(localStorage.getItem('zj.term.fm') === '1')
const panelW = ref(Number(localStorage.getItem('zj.term.fm.w')) || 460)
const toggleFilePanel = () => {
  fileOpen.value = !fileOpen.value
  localStorage.setItem('zj.term.fm', fileOpen.value ? '1' : '0')
  setTimeout(() => fitAll(), 60) // 面板开合改变终端宽度，重排尺寸
}
const fmSideStyle = computed(() => ({ width: panelW.value + 'px' }))

// 分隔条拖拽调宽
const startDividerDrag = (ev: PointerEvent) => {
  ev.preventDefault()
  const startX = ev.clientX
  const startW = panelW.value
  const move = (e: PointerEvent) => {
    const max = Math.floor(window.innerWidth * 0.7)
    panelW.value = Math.min(max, Math.max(320, startW + (startX - e.clientX)))
  }
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    localStorage.setItem('zj.term.fm.w', String(panelW.value))
    fitAll()
  }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

// 常用命令：发送到当前激活的 SSH 会话（命令 + 回车）
const qcOpen = ref(false)
const sendQuickCmd = (cmd: string) => {
  const t = activeSshTab.value
  if (!t) { message.warning('请先选择一个 SSH 标签'); return }
  if (t.ws && t.ws.readyState === 1) t.ws.send(new TextEncoder().encode(cmd + '\r'))
}

// SSH 主机 CRUD
/** 打开新增/编辑主机弹窗：新增时预填默认端口与认证方式。 */
const openHostEditor = (host?: any) => {
  editHost.value = host ? { ...host } : { port: 22, authType: 'password' }
  hostOpen.value = true
}
const saveHost = async () => {
  // 校验失败的错误提示由 axios 拦截器统一弹出，此处只需保证不关闭弹窗、不误报成功
  try {
    await post('/api/ssh/hosts', editHost.value)
  } catch {
    return
  }
  message.success('已保存')
  hostOpen.value = false
  await loadHosts()
}
const delHost = async (h: any) => { await del(`/api/ssh/hosts/${h.id}`); loadHosts() }
const testHost = async (h: any) => {
  testingId.value = h.id
  try {
    const r = await post(`/api/ssh/hosts/${h.id}/test`)
    if (r.ok) message.success('连接成功')
    else message.error(r.error)
  } finally { testingId.value = 0 }
}

onMounted(async () => {
  window.addEventListener('beforeunload', beforeUnloadHandler)
  await loadHosts()
  await restoreSessions()
})

// 保活生命周期：终端 WS 会话保持不断（切走仍输出）；仅窗口 resize 监听随激活/停用增删，
// 激活时重排尺寸（keep-alive 恢复时容器尺寸可能已变化）
const beforeUnloadHandler = () => {
  // 页面刷新/关闭前立即拍所有标签的快照（不走防抖），确保恢复时不丢最后一批内容
  tabs.value.forEach(t => takeSnapshotNow(t))
}
onActivated(() => { window.addEventListener('resize', fitAll); fitAll() })
onDeactivated(() => window.removeEventListener('resize', fitAll))
onUnmounted(() => {
  window.removeEventListener('resize', fitAll)
  window.removeEventListener('beforeunload', beforeUnloadHandler)
  tabs.value.forEach(t => {
    t.closing = true
    if (t.reconnTimer) clearTimeout(t.reconnTimer)
    if (t.snapTimer) clearTimeout(t.snapTimer)
    if (t.roRaf) cancelAnimationFrame(t.roRaf)
    t.ro?.disconnect()
    t.ws?.close()
    if (t.statsTimer) clearInterval(t.statsTimer)
  })
})

// 主题/窗口变化：同步所有已打开终端的配色并重新适配尺寸
const fitAll = () => tabs.value.forEach(t => { if (!t.closing) fitTerm(t) })
watch(() => ui.isDark, () => {
  tabs.value.forEach(t => { if (t.term) t.term.options.theme = termTheme() })
  fitAll()
})
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
/* 卡片撑满内容区（height:100% 自适应顶栏/边距变化，勿用写死的 100vh-XX：
   顶栏高度或内容边距一变，写死值就会比可用空间高出几十像素，页面出现上下滚动） */
.terminal-card { height: 100%; display: flex; flex-direction: column; }
.terminal-card :deep(.ant-card-body) { flex: 1 1 0; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
/* 工作台主体：左终端 + 右文件面板 */
.term-body { flex: 1; min-height: 0; display: flex; }
.term-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.fm-divider { flex: none; width: 5px; cursor: col-resize; background: transparent; transition: background .15s; }
.fm-divider:hover { background: rgba(22,119,255,.35); }
.fm-side { flex: none; min-width: 0; display: flex; flex-direction: column; overflow: hidden;
  border: 1px solid var(--border-card); border-radius: 6px; background: var(--surface-card); }
@media (max-width: 900px) {
  /* 窄屏：文件面板改为全宽覆盖层 */
  .term-body { position: relative; }
  .fm-divider { display: none; }
  .fm-side { position: absolute; right: 0; top: 0; bottom: 0; z-index: 20; width: 100% !important; }
}
.term-tabs { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.term-tabs :deep(.ant-tabs-nav) { margin-bottom: 8px; }
.term-tabs :deep(.ant-tabs-content-holder) { flex: 1; min-height: 0; overflow: hidden; }
.term-tabs :deep(.ant-tabs-content) { height: 100%; }
.term-pane { height: 100%; display: flex; flex-direction: column; }
.term-pane :deep(.ant-tabs-tabpane) { display: flex; flex-direction: column; height: 100%; }
.term-box { flex: 1; min-height: 0; margin-top: 4px; overflow: hidden; }
.status-strip { flex: none; }
</style>
