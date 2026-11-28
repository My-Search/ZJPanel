<template>
  <!-- 顶栏触发：常驻 WS 连接，运行中会话数作角标（与会话状态解耦，弹框开关不影响） -->
  <a-badge :count="ops.running" :offset="[-2, 4]" size="small">
    <a-button type="text" class="ops-btn" title="运维Agent" @click="openWorkbench">
      <RobotOutlined />
    </a-button>
  </a-badge>

  <a-modal
    v-model:open="ops.open"
    :width="isMobile ? '96%' : 1080"
    :footer="null"
    centered
    wrap-class-name="ops-workbench"
    :mask-closable="true"
  >
    <template #title>
      <div class="owm-title">
        <!-- 左栏折叠开关：状态记忆在 localStorage，移动端屏窄默认收起 -->
        <a-button type="text" class="owm-fold" :title="sideOpen ? '收起会话列表' : '展开会话列表'" @click="toggleSide">
          <MenuUnfoldOutlined v-if="!sideOpen" />
          <MenuFoldOutlined v-else />
        </a-button>
        <span class="owm-logo"><RobotOutlined /></span>
        <span class="owm-name">
          <b>运维Agent</b>
          <small>会话在服务器后台运行 · 关闭/刷新页面不中断</small>
        </span>
        <a-tag v-if="modeLabel" class="owm-mode" :color="modeColor" style="margin-right: 0">{{ modeLabel }}</a-tag>
      </div>
    </template>

    <div class="om-body">
      <!-- 左栏：会话列表（可折叠；移动端以浮层展开，不挤压聊天列） -->
      <transition name="om-side">
        <aside v-if="sideOpen" class="om-side">
          <div class="om-actions">
            <a-button type="primary" class="om-new" @click="startNew">
              <PlusOutlined /> 新会话
            </a-button>
            <a-tooltip title="搜索会话（标题/消息内容）">
              <a-button class="om-search-toggle" :class="{ on: searchOpen }" @click="toggleSearch">
                <SearchOutlined />
              </a-button>
            </a-tooltip>
          </div>
          <div v-if="searchOpen" class="om-search-row">
            <a-input-search
              ref="searchRef"
              v-model:value="searchInput"
              class="om-search"
              size="small"
              placeholder="标题或消息内容"
              allow-clear
              @search="onSearchInput"
            />
          </div>
          <div class="om-sessions" @scroll.passive="onListScroll">
            <div
              v-for="s in ops.sessions"
              :key="s.id"
              class="om-sess"
              :class="{ on: s.id === ops.currentId, ['st-' + s.status]: true, unread: !isRead(s) }"
              @click="pickSession(s.id)"
            >
              <div class="om-sess-top">
                <span class="om-dot" :class="dotClass(s)">
                  <LoadingOutlined v-if="s.status === 'running'" spin />
                </span>
                <span class="om-sess-title">{{ s.title || '（无标题）' }}</span>
                <a-popconfirm title="删除该会话及消息？" @confirm="ops.removeSession(s.id)">
                  <span class="om-sess-del" title="删除会话" @click.stop><DeleteOutlined /></span>
                </a-popconfirm>
              </div>
              <div class="om-sess-meta">
                <span v-if="sourceLabel(s)" class="om-sess-src">{{ sourceLabel(s) }}</span>
                <span class="om-sess-st" :class="'tx-' + s.status">{{ statusLabel(s.status) }}</span>
                <span class="om-sess-time">{{ fmtTime(s.updatedAt) }}</span>
              </div>
            </div>
            <div v-if="!ops.sessions.length" class="om-empty">{{ ops.sessionQuery ? '未找到匹配的会话' : '暂无会话' }}</div>
            <div v-else-if="ops.sessions.length < ops.sessionsTotal" class="om-more" @click="ops.loadMoreSessions()">
              加载更多（还有 {{ ops.sessionsTotal - ops.sessions.length }} 条）
            </div>
            <div v-else-if="ops.sessions.length > 20" class="om-more om-more-end">没有更多了</div>
          </div>
        </aside>
      </transition>
      <!-- 移动端左栏展开时的遮罩（桌面 display:none），点击收起 -->
      <transition name="om-mask">
        <div v-if="sideOpen" class="om-side-mask" @click="sideOpen = false"></div>
      </transition>

      <!-- 右栏：消息流 + 输入（扁平时间线：助手全宽、用户气泡、工具轨迹折叠） -->
      <section class="om-main">
        <a-alert
          v-if="ops.settings && !ops.settings.llm.ready"
          type="warning"
          show-icon
          message="尚未配置模型：请前往「设置 → 运维Agent」填写 LLM 地址、API Key 与模型名后再开始对话。"
          style="margin: 12px 16px 0"
        />
        <div v-if="curError" class="om-error"><CloseCircleOutlined /> {{ curError }}</div>

        <div :ref="setScrollEl" class="om-msgs">
          <!-- 空态：欢迎页（新会话发送首条消息前显示；输入区始终在下方，发送即创建会话） -->
          <div v-if="ops.currentId == null" class="om-guide">
            <div class="om-guide-icon"><RobotOutlined /></div>
            <h3>AI 运维智能体</h3>
            <p class="om-guide-sub">描述任务，Agent 会调用面板工具查询与操作，并在授权模式下执行防御动作</p>
            <div class="om-guide-cards">
              <div class="om-guide-card" @click="fillExample('查看面板整体状态，检查证书临期与站点异常')">
                <DashboardOutlined class="om-gc-icon" />
                <span>查看面板整体状态<br /><small>证书临期 · 站点异常</small></span>
              </div>
              <div class="om-guide-card" @click="fillExample('分析最近的安全事件，如有攻击来源请给出处置建议')">
                <SafetyOutlined class="om-gc-icon" />
                <span>分析安全事件<br /><small>攻击来源 · 处置建议</small></span>
              </div>
              <div class="om-guide-card" @click="fillExample('检查所有主机的磁盘与内存占用，汇总成表格')">
                <CloudServerOutlined class="om-gc-icon" />
                <span>巡检所有主机<br /><small>磁盘 · 内存 · 负载</small></span>
              </div>
            </div>
          </div>

          <template v-else>
            <div class="om-col">
              <template v-for="u in projection.units" :key="u.type === 'turn' ? u.turn.key : 's' + u.msg.id">
                <!-- 压缩分隔条：早期上下文已压成摘要（原始记录保留在库） -->
                <div v-if="u.type === 'summary'" class="om-compact">
                  <CompressOutlined /> 早期上下文已压缩（{{ summaryCount(u.msg) }} 条）· 摘要已注入模型上下文
                </div>
                <OpsTurnGroup v-else :turn="u.turn" />
              </template>
              <!-- 等待首 token / 工具间隙的思考占位 -->
              <div v-if="projection.showThinking" class="om-thinking">
                <LoadingOutlined spin class="om-thinking-icon" />
                <span class="om-shimmer">正在思考</span>
              </div>
            </div>
            <transition name="om-fadeup">
              <button v-if="showBackToBottom" class="om-to-bottom" @click="resetToBottom">
                <ArrowDownOutlined /> 回到底部
              </button>
            </transition>
          </template>
        </div>

        <!-- confirm 模式：待确认操作卡片 -->
        <div v-if="pendingMsg && curSession?.status === 'waiting_confirm'" class="om-confirm">
          <div class="om-confirm-title"><ExclamationCircleOutlined /> 待确认操作 · 「执行前确认」模式下处置操作需人工放行</div>
          <div class="om-confirm-tool"><span class="om-confirm-name">{{ pendingMsg.toolName }}</span> <span class="om-confirm-args1">{{ fmtArgs(pendingMsg.toolArgs) }}</span></div>
          <pre class="om-confirm-args">{{ prettyArgs(pendingMsg.toolArgs) }}</pre>
          <div class="om-confirm-actions">
            <a-button size="small" @click="ops.confirm(curSession!.id, false)">拒绝</a-button>
            <a-button size="small" type="primary" danger @click="ops.confirm(curSession!.id, true)">确认执行</a-button>
          </div>
        </div>

        <!-- 输入区：统一 composer（常驻；未选会话时发送即新建会话） -->
        <div class="om-composer" :class="{ focused: composerFocus }">
          <textarea
            ref="taRef"
            v-model="input"
            class="om-input"
            rows="1"
            placeholder="描述任务，Ctrl+Enter 发送…"
            @keydown.ctrl.enter.prevent="send"
            @keydown.meta.enter.prevent="send"
            @focus="composerFocus = true"
            @blur="composerFocus = false"
          ></textarea>
          <div class="om-composer-bar">
            <!-- 防御模式：会话级授权级别，新建时选、空闲时可切换（运行中锁定） -->
            <a-tooltip :title="modeTip">
              <a-select
                class="om-mode"
                size="small"
                :bordered="false"
                :value="curSession ? curSession.defenseMode : newMode"
                :disabled="modeLocked"
                :popup-match-select-width="false"
                @change="(v: any) => onModeChange(v as OpsMode)"
              >
                <a-select-option v-for="m in OPS_MODE_OPTIONS" :key="m.value" :value="m.value">
                  {{ m.label }}
                </a-select-option>
              </a-select>
            </a-tooltip>
            <a-select
              v-model:value="hostSel"
              class="om-host"
              size="small"
              :placeholder="curSession ? '未绑定主机' : '绑定主机（可选）'"
              :disabled="!!curSession"
              allow-clear
              :bordered="false"
              :popup-match-select-width="false"
            >
              <a-select-option v-for="h in hosts" :key="h.id" :value="h.id">{{ h.name }}</a-select-option>
            </a-select>
            <span class="om-kbd om-ctx" :class="{ warn: ctxRatio > 0.75 }" v-if="curSession?.contextLimit" :title="'估算值，超过窗口 75% 时自动压缩上下文'">
              ≈ {{ fmtTokens(curSession.contextTokens) }} / {{ fmtTokens(curSession.contextLimit) }}
            </span>
            <span class="om-kbd om-hint">Ctrl + Enter 发送</span>
            <a-button
              v-if="curSession && curSession.status === 'running'"
              size="small" danger
              @click="ops.stop(curSession.id)"
            ><PoweroffOutlined /> 停止</a-button>
            <a-button v-else size="small" type="primary" class="om-send" :loading="sending" :disabled="!input.trim()" @click="send">
              <template #icon><SendOutlined /></template>
              发送
            </a-button>
          </div>
        </div>
      </section>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  RobotOutlined, PlusOutlined, DeleteOutlined, SendOutlined, PoweroffOutlined,
  LoadingOutlined, ExclamationCircleOutlined, SearchOutlined,
  CloseCircleOutlined, DashboardOutlined,
  SafetyOutlined, CloudServerOutlined, CompressOutlined, ArrowDownOutlined,
  MenuFoldOutlined, MenuUnfoldOutlined,
} from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useOpsAgent, OPS_MODE_OPTIONS, opsModeLabel, type OpsSession, type OpsMode } from '../store/opsagent'
import { wsTopic, get } from '../api'
import { usePolling } from '../composables/usePolling'
import { useTimelineScroll } from '../composables/useTimelineScroll'
import { useConversationTurns } from '../composables/useConversationTurns'
import OpsTurnGroup from './opsagent/OpsTurnGroup.vue'
import { isPending, summaryCount, fmtArgs, prettyArgs } from './opsagent/meta'

const ops = useOpsAgent()
const input = ref('')
// 新会话的草稿选择（防御模式/绑定主机）：localStorage 持久化，页面刷新后重开仍能回显上次所选
const DRAFT_KEY = 'zj.opsagent.composer'
const draft = (() => {
  try { return JSON.parse(localStorage.getItem(DRAFT_KEY) || '{}') as { mode?: OpsMode; host?: number } } catch { return {} }
})()
const hostId = ref<number | undefined>(draft.host && draft.host > 0 ? draft.host : undefined)
const newMode = ref<OpsMode>(OPS_MODE_OPTIONS.some(o => o.value === draft.mode) ? (draft.mode as OpsMode) : 'observe') // 新会话的防御模式（发送时随会话创建落库）
watch([newMode, hostId], () => {
  localStorage.setItem(DRAFT_KEY, JSON.stringify({ mode: newMode.value, host: hostId.value ?? 0 }))
})
const hosts = ref<{ id: number; name: string }[]>([])
const sending = ref(false)
const taRef = ref<HTMLTextAreaElement | null>(null)
const isMobile = ref(window.innerWidth < 768)
const composerFocus = ref(false)

// 左栏会话列表折叠：桌面记忆用户选择（localStorage），无记录时默认展开；
// 移动端展开即浮层遮罩挡住聊天，不随记忆自动展开，每次打开默认收起
const SIDE_KEY = 'zj.opsagent.side'
const savedSide = localStorage.getItem(SIDE_KEY)
const sideOpen = ref(!isMobile.value && (savedSide == null || savedSide === '1'))
const toggleSide = () => {
  sideOpen.value = !sideOpen.value
  localStorage.setItem(SIDE_KEY, sideOpen.value ? '1' : '0')
}
// 移动端左栏是浮层：选中/新建会话后自动收起，把整幅留给聊天列
const pickSession = (id: number) => {
  ops.selectSession(id)
  if (isMobile.value) sideOpen.value = false
}

// ---- 会话搜索（标题/消息内容，后端过滤）与触底加载更多 ----
const searchOpen = ref(false)
const searchInput = ref('')
const searchRef = ref<any>(null)
let searchTimer: ReturnType<typeof setTimeout> | null = null
const toggleSearch = () => {
  searchOpen.value = !searchOpen.value
  if (searchOpen.value) {
    nextTick(() => searchRef.value?.focus?.())
  } else {
    searchInput.value = ''
    void ops.searchSessions('') // 收起即清除过滤
  }
}
// 输入防抖过滤；回车（@search）立即生效
const onSearchInput = () => {
  if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
  void ops.searchSessions(searchInput.value)
}
watch(searchInput, v => {
  if (!searchOpen.value) return
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void ops.searchSessions(v), 300)
})
// 触底自动加载下一页（store 内有并发与到尾守卫）
const onListScroll = (e: Event) => {
  const el = e.target as HTMLElement
  if (el.scrollTop + el.clientHeight >= el.scrollHeight - 40) void ops.loadMoreSessions()
}

// 时间线滚动（底部锚定状态机）与回合投影
const { setScrollEl, showBackToBottom, onContentChanged, resetToBottom } = useTimelineScroll()

const curSession = computed(() => ops.sessions.find(s => s.id === ops.currentId) || null)
// 绑定主机回显：选中会话时只读展示该会话绑定的主机（数据在会话上，此前的草稿 ref 看不到它）；
// 未选会话时是待创建会话的草稿选择
const hostSel = computed<number | undefined>({
  get: () => (curSession.value ? curSession.value.hostId ?? undefined : hostId.value),
  set: v => { hostId.value = v },
})
const curMessages = computed(() => (ops.currentId != null ? ops.messages[ops.currentId] || [] : []))
const curError = computed(() => curSession.value?.error || '')
const projection = useConversationTurns(
  () => curMessages.value,
  () => curSession.value?.status,
)
const curMode = computed<OpsMode>(() => curSession.value?.defenseMode || newMode.value)
const modeLocked = computed(() => {
  const st = curSession.value?.status
  return st === 'running' || st === 'waiting_confirm'
})
const modeLabel = computed(() => opsModeLabel(curMode.value))
const modeColor = computed(() =>
  ({ observe: 'blue', confirm: 'orange', auto: 'red' } as Record<string, string>)[curMode.value] || 'default')
const modeTip = computed(() => {
  if (!curSession.value) return '本次会话的防御模式：仅观察只做分析建议，执行前确认需人工放行，自动处置直接执行（全程审计）'
  if (modeLocked.value) return '会话运行中不可切换防御模式，停止后可改'
  return '切换该会话的防御模式（下一轮对话生效）'
})
const pendingMsg = computed(() => curMessages.value.find(m => m.role === 'tool' && isPending(m)) || null)

// 防御模式切换：新会话只改待创建快照；已有会话调接口（空闲才允许）
async function onModeChange(mode: OpsMode) {
  const sess = curSession.value
  if (!sess) {
    newMode.value = mode
    return
  }
  if (modeLocked.value || mode === sess.defenseMode) return
  try {
    await ops.setSessionMode(sess.id, mode)
    message.success(`防御模式已切换为「${opsModeLabel(mode)}」`)
  } catch (e: any) {
    message.error(e?.response?.data?.error || '切换失败')
  }
}

const statusLabel = (s: string) =>
  ({ running: '运行中', waiting_confirm: '待确认', idle: '已完成', interrupted: '已中断', error: '出错' }[s] || s)
const sourceLabel = (s: OpsSession) =>
  s.source === 'inspect' ? '巡检' : s.source.startsWith('event:') ? '事件' : ''
// 已查看判定（与后端分组口径一致）：终态且 viewedAt 不早于 updatedAt = 已查看；
// 进行中的会话天然属于「进行中」组，不算未读。
const isRead = (s: OpsSession) => {
  if (s.status === 'running' || s.status === 'waiting_confirm') return true
  return !!s.viewedAt && s.viewedAt >= s.updatedAt
}
const dotClass = (s: OpsSession) => {
  if (s.status === 'running') return 'run'
  if (s.status === 'waiting_confirm') return 'confirm'
  if (s.status === 'error') return 'err'
  if (!isRead(s)) return s.status === 'interrupted' ? 'confirm' : 'ok' // 完成未查看点亮（已读灰点）
  return ''
}

const ctxRatio = computed(() => {
  const s = curSession.value
  if (!s?.contextLimit || !s.contextTokens) return 0
  return s.contextTokens / s.contextLimit
})
const fmtTokens = (n?: number) => {
  if (!n || n <= 0) return '0'
  return n >= 1000 ? (n / 1000).toFixed(n >= 100000 ? 0 : 1) + 'k' : String(n)
}
const fmtTime = (t: string) => {
  const d = new Date(t)
  return `${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const fillExample = (t: string) => {
  input.value = t
  nextTick(() => taRef.value?.focus())
}
// 新会话：回到空态引导页并聚焦输入框（输入区常驻，发送即创建会话）；先清掉搜索过滤，避免新会话被滤掉
const startNew = () => {
  if (searchOpen.value) toggleSearch()
  ops.selectSession(null)
  if (isMobile.value) sideOpen.value = false
  nextTick(() => taRef.value?.focus())
}

async function send() {
  const msg = input.value.trim()
  if (!msg || sending.value) return
  sending.value = true
  try {
    if (ops.currentId == null) {
      await ops.newSession(msg, hostId.value, newMode.value)
    } else {
      await ops.sendMessage(ops.currentId, msg)
    }
    input.value = ''
  } catch (e: any) {
    message.error(e?.response?.data?.error || '发送失败')
  } finally {
    sending.value = false
    onContentChanged()
  }
}

async function openWorkbench() {
  ops.open = true
  await Promise.all([ops.fetchSessions(), ops.loadSettings(), loadHosts()])
  if (ops.currentId == null && ops.sessions.length) ops.selectSession(ops.sessions[0].id)
  resetToBottom()
}

async function loadHosts() {
  if (hosts.value.length) return
  try {
    const list = await get<any[]>('/api/hosts')
    hosts.value = (list || []).map(h => ({ id: h.id, name: h.local ? h.name + '（本机）' : h.name }))
  } catch { /* ignore */ }
}

// ---- 时间线滚动联动 ----
// 切换会话：回到底部（打开会话定位最新消息）
watch(() => ops.currentId, () => resetToBottom())
// 新消息/流式增量：行集与内容长度指纹变化 → 贴底裁决（跟随中贴底，离底不拉回）
watch(
  () => curMessages.value.map(m => `${m.id}:${m.content.length}:${(m.reasoning || '').length}`).join(',') +
    '|' + (curSession.value?.status || ''),
  () => onContentChanged(),
)

// 常驻 WS：角标实时刷新，弹框开着时增量消息由 store upsert
const stopWs = wsTopic('opsagent', (frame: any) => {
  ops.onWsFrame(frame)
})
usePolling(() => ops.fetchRunning(), 60000, { immediate: false })
const onResize = () => { isMobile.value = window.innerWidth < 768 }
window.addEventListener('resize', onResize)
onMounted(() => { ops.fetchRunning() })
onUnmounted(() => { stopWs(); window.removeEventListener('resize', onResize) })
</script>

<style scoped>
.ops-btn { padding: 0 8px; font-size: 15px; }

/* ---- 主体布局 ---- */
.om-body { display: flex; height: min(70vh, 640px); background: var(--surface-card, transparent); position: relative; }

/* ---- 左栏会话列表 ---- */
.om-side {
  width: 248px; flex-shrink: 0; display: flex; flex-direction: column; gap: 10px;
  padding: 14px 12px; border-right: 1px solid var(--border-card, rgba(128,128,128,.18));
  background: rgba(128,128,128,.04);
}
.om-actions { display: flex; gap: 8px; }
.om-new { flex: 1; border-radius: 8px; font-weight: 600; }
.om-search-toggle { flex-shrink: 0; width: 34px; border-radius: 8px; }
.om-search-toggle.on { color: #1677ff; border-color: rgba(22,119,255,.45); }
.om-search-row { display: flex; }
.om-sessions { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 6px; }
.om-sess {
  padding: 9px 11px; border-radius: 10px; cursor: pointer; position: relative;
  border: 1px solid transparent; transition: background .15s, border-color .15s;
}
.om-sess:hover { background: rgba(128,128,128,.1); }
.om-sess.on { background: rgba(22,119,255,.1); border-color: rgba(22,119,255,.45); }
.om-sess-top { display: flex; align-items: center; gap: 7px; min-width: 0; }
.om-dot {
  width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: #8c8c8c;
  display: inline-grid; place-items: center; font-size: 9px; color: #fff;
}
.om-dot.run { background: #1677ff; box-shadow: 0 0 0 3px rgba(22,119,255,.18); }
.om-dot.confirm { background: #f59e0b; box-shadow: 0 0 0 3px rgba(245,158,11,.18); }
.om-dot.err { background: #ef4444; }
.om-dot.ok { background: #22c55e; box-shadow: 0 0 0 3px rgba(34,197,94,.18); }
.om-sess-title {
  flex: 1; min-width: 0; font-size: 12.5px; color: var(--color-ink);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.om-sess.unread .om-sess-title { font-weight: 600; }
.om-sess-del {
  flex-shrink: 0; font-size: 12px; color: var(--color-slate); opacity: 0; transition: opacity .15s;
}
.om-sess:hover .om-sess-del { opacity: 1; }
.om-sess-del:hover { color: #ef4444; }
.om-sess-meta { display: flex; align-items: center; gap: 8px; margin-top: 5px; padding-left: 15px; }
.om-sess-st { font-size: 11px; }
.om-sess-st.tx-running { color: #1677ff; }
.om-sess-st.tx-waiting_confirm { color: #f59e0b; }
.om-sess-st.tx-error { color: #ef4444; }
.om-sess-st.tx-idle, .om-sess-st.tx-interrupted { color: var(--color-slate); }
.om-sess-src { font-size: 10.5px; color: var(--color-slate); background: rgba(128,128,128,.14); border-radius: 4px; padding: 0 5px; }
.om-sess-time { font-size: 11px; color: var(--color-slate); }
.om-empty { text-align: center; color: var(--color-slate); font-size: 12px; padding: 28px 0; }
.om-more { text-align: center; font-size: 11px; color: var(--color-slate); padding: 6px 0; cursor: pointer; }
.om-more:hover { color: #1677ff; }
.om-more.om-more-end { cursor: default; opacity: .6; }
.om-more.om-more-end:hover { color: var(--color-slate); }
/* 左栏折叠遮罩：仅移动端浮层模式下显示（桌面隐藏） */
.om-side-mask { display: none; }

/* ---- 右栏 ---- */
.om-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.om-error {
  margin: 12px 16px 0; padding: 8px 12px; border-radius: 8px; font-size: 12.5px;
  background: rgba(239,68,68,.1); border: 1px solid rgba(239,68,68,.35); color: #ef4444;
}

/* ---- 空态 ---- */
.om-guide { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 24px; }
.om-guide-icon {
  width: 64px; height: 64px; border-radius: 18px; display: grid; place-items: center;
  font-size: 30px; color: #fff; background: linear-gradient(135deg, #1677ff, #6a5cff);
  box-shadow: 0 12px 28px rgba(22,119,255,.28);
}
.om-guide h3 { margin: 16px 0 6px; font-size: 17px; color: var(--color-ink); }
.om-guide-sub { margin: 0 0 22px; font-size: 12.5px; color: var(--color-slate); }
.om-guide-cards { display: flex; gap: 12px; flex-wrap: wrap; justify-content: center; }
.om-guide-card {
  width: 190px; padding: 14px 16px; border-radius: 12px; cursor: pointer; text-align: left;
  border: 1px solid var(--border-card, rgba(128,128,128,.2)); background: var(--surface-card, transparent);
  display: flex; align-items: center; gap: 10px; font-size: 12.5px; color: var(--color-ink); line-height: 1.5;
  transition: border-color .15s, transform .15s, box-shadow .15s;
}
.om-guide-card:hover { border-color: rgba(22,119,255,.5); transform: translateY(-2px); box-shadow: 0 8px 20px rgba(0,0,0,.12); }
.om-guide-card small { color: var(--color-slate); font-size: 11px; }
.om-gc-icon { font-size: 18px; color: #1677ff; flex-shrink: 0; }

/* ---- 消息时间线（扁平布局：内容列居中限宽，助手全宽、用户气泡） ---- */
.om-msgs {
  flex: 1; overflow-y: auto; padding: 16px 18px 8px; position: relative;
  display: flex; flex-direction: column; overflow-anchor: none;
}
.om-col {
  width: 100%; max-width: 860px; margin: 0 auto;
  display: flex; flex-direction: column; gap: 18px; padding: 4px 2px 16px;
}
.om-compact {
  text-align: center; font-size: 11px; color: var(--color-slate); margin: 2px 0;
  display: flex; align-items: center; justify-content: center; gap: 6px;
}
.om-compact::before, .om-compact::after {
  content: ''; height: 1px; flex: 1; background: rgba(128,128,128,.2);
}
.om-thinking { display: flex; align-items: center; gap: 8px; font-size: 12.5px; padding: 2px 2px; }
.om-thinking-icon { color: #1677ff; font-size: 13px; }

/* 回到底部悬浮按钮（sticky 贴在时间线底部） */
.om-to-bottom {
  position: sticky; bottom: 14px; align-self: center; z-index: 5;
  display: inline-flex; align-items: center; gap: 6px;
  border: 1px solid var(--border-card, rgba(128,128,128,.25)); border-radius: 999px;
  background: var(--surface-card, #fff); color: var(--color-ink, #303133);
  padding: 5px 14px; font-size: 12px; cursor: pointer;
  box-shadow: 0 6px 18px rgba(0,0,0,.14);
}
.om-to-bottom:hover { border-color: rgba(22,119,255,.5); color: #1677ff; }
.om-fadeup-enter-active, .om-fadeup-leave-active { transition: opacity .18s, transform .18s; }
.om-fadeup-enter-from, .om-fadeup-leave-to { opacity: 0; transform: translateY(8px); }

/* 确认卡 */
.om-confirm {
  margin: 0 16px 10px; border: 1px solid rgba(245,158,11,.5); border-radius: 12px;
  background: rgba(245,158,11,.07); padding: 12px 14px;
  box-shadow: 0 6px 18px rgba(245,158,11,.12);
}
.om-confirm-title { font-size: 12.5px; font-weight: 600; color: #b45309; display: flex; align-items: center; gap: 6px; }
html[data-theme='dark'] .om-confirm-title { color: #fbbf24; }
.om-confirm-tool { display: flex; gap: 8px; margin-top: 8px; align-items: baseline; min-width: 0; }
.om-confirm-name { color: #1677ff; font-weight: 600; font-family: ui-monospace, Consolas, monospace; font-size: 12px; flex-shrink: 0; }
.om-confirm-args1 {
  color: var(--color-slate); font-family: ui-monospace, Consolas, monospace; font-size: 11.5px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0;
}
.om-confirm-args {
  margin: 8px 0 10px; padding: 9px 11px; border-radius: 8px; background: rgba(128,128,128,.08);
  font-size: 12px; font-family: ui-monospace, Consolas, monospace; max-height: 130px; overflow: auto;
  white-space: pre-wrap; word-break: break-all;
}
.om-confirm-actions { display: flex; justify-content: flex-end; gap: 8px; }

/* ---- 输入区 composer ---- */
.om-composer {
  margin: 10px 16px 14px; border: 1px solid var(--border-card, rgba(128,128,128,.25));
  border-radius: 14px; background: var(--surface-card, rgba(128,128,128,.04));
  transition: border-color .15s, box-shadow .15s;
}
.om-composer.focused { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22,119,255,.14); }
.om-input {
  display: block; width: 100%; border: none; outline: none; resize: none;
  background: transparent; color: var(--color-ink);
  padding: 11px 14px 4px; font-size: 13px; line-height: 1.6; font-family: inherit;
}
.om-input::placeholder { color: var(--color-slate); }
.om-composer-bar { display: flex; align-items: center; gap: 8px; padding: 4px 8px 8px 4px; flex-wrap: wrap; }
.om-host { min-width: 170px; flex-shrink: 0; }
.om-mode { min-width: 112px; flex-shrink: 0; font-weight: 600; }
.om-kbd { margin-left: auto; font-size: 11px; color: var(--color-slate); }
.om-ctx { margin-left: auto; font-family: ui-monospace, Consolas, monospace; }
.om-ctx + .om-kbd { margin-left: 0; }
.om-ctx.warn { color: #f59e0b; font-weight: 600; }
.om-send { border-radius: 8px; font-weight: 600; }

@media (max-width: 768px) {
  .om-side { width: 168px; padding: 10px 8px; }
  .om-guide-cards { flex-direction: column; }
  .om-guide-card { width: 100%; }
  .om-msgs { padding: 12px 10px 6px; }
  .om-hint { display: none; }
  .om-host { min-width: 140px; }
  /* 移动端左栏改为浮层：展开时不挤压聊天列，遮罩点击收起；铺不透明底防止聊天文字透出 */
  .om-body > .om-side {
    position: absolute; left: 0; top: 0; bottom: 0; z-index: 11;
    background: var(--surface-card, #1f1f1f);
    box-shadow: 8px 0 24px rgba(0, 0, 0, .28);
  }
  .om-side-mask { display: block; position: absolute; inset: 0; z-index: 10; background: rgba(0, 0, 0, .4); }
}

/* 左栏展开/收起过渡：宽度收缩 + 左移淡出（须置于 media 块之后，收起态宽度才能盖过默认宽度） */
.om-side-enter-active, .om-side-leave-active {
  transition: width .18s ease, transform .18s ease, opacity .18s ease;
  overflow: hidden;
}
.om-side-enter-from, .om-side-leave-to { width: 0; opacity: 0; transform: translateX(-16px); }
.om-mask-enter-active, .om-mask-leave-active { transition: opacity .18s ease; }
.om-mask-enter-from, .om-mask-leave-to { opacity: 0; }
</style>

<style>
@import './opsagent/chat.css';

/* 弹框框架样式（teleport 到 body，需全局作用域，用 wrap-class 限定） */
.ops-workbench .ant-modal-content { padding: 0; overflow: hidden; border-radius: 14px; }
.ops-workbench .ant-modal-header {
  padding: 14px 20px; margin-bottom: 0;
  border-bottom: 1px solid var(--border-card, rgba(128,128,128,.16));
  background: linear-gradient(180deg, rgba(22,119,255,.06), transparent);
}
.ops-workbench .ant-modal-close { top: 14px; right: 16px; }
.owm-title { display: flex; align-items: center; gap: 11px; padding-right: 34px; }
/* 左栏折叠开关（标题栏最左）：常驻可见，收起后靠它展开 */
.owm-fold { flex-shrink: 0; padding: 0 6px; font-size: 15px; color: var(--color-slate, #999); }
.owm-logo {
  flex-shrink: 0;
  width: 36px; height: 36px; border-radius: 10px; display: inline-grid; place-items: center;
  font-size: 18px; color: #fff; background: linear-gradient(135deg, #1677ff, #6a5cff);
  box-shadow: 0 6px 16px rgba(22,119,255,.3);
}
.owm-name { display: flex; flex-direction: column; gap: 1px; line-height: 1.3; min-width: 0; }
.owm-name b { font-size: 15px; color: var(--color-ink, #fff); }
.owm-name small { font-size: 11px; font-weight: 400; color: var(--color-slate, #999); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.owm-mode { margin-left: auto !important; flex-shrink: 0; }
</style>
