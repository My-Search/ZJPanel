<template>
  <!-- 顶栏触发：常驻 WS 连接，运行中会话数作角标（与会话状态解耦，弹框开关不影响） -->
  <a-badge :count="ops.running" :offset="[-2, 4]" size="small">
    <a-button type="text" class="ops-btn" title="管理 Agent（AI 运维）" @click="openWorkbench">
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
        <span class="owm-logo"><RobotOutlined /></span>
        <span class="owm-name">
          <b>管理 Agent</b>
          <small>会话在服务器后台运行 · 关闭/刷新页面不中断</small>
        </span>
        <a-tag v-if="modeLabel" class="owm-mode" :color="modeColor" style="margin-right: 0">{{ modeLabel }}</a-tag>
      </div>
    </template>

    <div class="om-body">
      <!-- 左栏：会话列表 -->
      <aside class="om-side">
        <a-button type="primary" block class="om-new" @click="startNew">
          <PlusOutlined /> 新会话
        </a-button>
        <div class="om-sessions">
          <div
            v-for="s in ops.sessions"
            :key="s.id"
            class="om-sess"
            :class="{ on: s.id === ops.currentId, ['st-' + s.status]: true }"
            @click="ops.selectSession(s.id)"
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
          <div v-if="!ops.sessions.length" class="om-empty">暂无会话</div>
        </div>
      </aside>

      <!-- 右栏：消息流 + 输入 -->
      <section class="om-main">
        <a-alert
          v-if="ops.settings && !ops.settings.llm.ready"
          type="warning"
          show-icon
          message="尚未配置模型：请前往「设置 → 运维Agent」填写 LLM 地址、API Key 与模型名后再开始对话。"
          style="margin: 12px 16px 0"
        />
        <div v-if="curError" class="om-error"><CloseCircleOutlined /> {{ curError }}</div>

        <div ref="scrollRef" class="om-msgs">
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
            <template v-for="m in curMessages" :key="m.id">
              <!-- 压缩分隔条：早期上下文已压成摘要（原始记录保留在库） -->
              <div v-if="m.role === 'summary'" class="om-compact">
                <CompressOutlined /> 早期上下文已压缩（{{ summaryCount(m) }} 条）· 摘要已注入模型上下文
              </div>
              <!-- 事件横幅 -->
              <div v-else-if="m.role === 'event'" class="om-ev">
                <ThunderboltOutlined class="om-ev-icon" />
                <pre class="om-ev-text">{{ m.content }}</pre>
              </div>
              <!-- 用户气泡 -->
              <div v-else-if="m.role === 'user'" class="om-row right">
                <div class="om-bubble om-user">{{ m.content }}</div>
                <span class="om-avatar me"><UserOutlined /></span>
              </div>
              <!-- 助手 -->
              <div v-else-if="m.role === 'assistant'" class="om-row">
                <span class="om-avatar ai"><RobotOutlined /></span>
                <div class="om-bubble om-assist">
                  <div v-if="m.content" class="om-text" :class="{ 'om-err-text': isErrText(m) }">{{ m.content }}</div>
                  <div v-if="toolCallsOf(m).length" class="om-calls">
                    <span v-for="tc in toolCallsOf(m)" :key="tc.id" class="om-call-chip" :title="tc.name + ' ' + fmtArgs(tc.arguments)">
                      <ApiOutlined /> {{ tc.name }}
                    </span>
                  </div>
                </div>
              </div>
              <!-- 工具执行 -->
              <div v-else-if="m.role === 'tool'" class="om-row">
                <span class="om-avatar ai tool"><ApiOutlined /></span>
                <div class="om-tool" :class="{ pending: isPending(m) }">
                  <div class="om-tool-head">
                    <LoadingOutlined v-if="isPending(m)" spin />
                    <ToolOutlined v-else />
                    <span class="om-tool-name">{{ m.toolName }}</span>
                    <span class="om-tool-args">{{ fmtArgs(m.toolArgs) }}</span>
                  </div>
                  <pre v-if="m.content" class="om-tool-out">{{ m.content }}</pre>
                </div>
              </div>
            </template>
          </template>
        </div>

        <!-- confirm 模式：待确认操作卡片 -->
        <div v-if="pendingMsg && curSession?.status === 'waiting_confirm'" class="om-confirm">
          <div class="om-confirm-title"><ExclamationCircleOutlined /> 待确认操作 · 「执行前确认」模式下处置操作需人工放行</div>
          <div class="om-confirm-tool"><span class="om-tool-name">{{ pendingMsg.toolName }}</span> <span class="om-tool-args">{{ fmtArgs(pendingMsg.toolArgs) }}</span></div>
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
            <a-select
              v-model:value="hostId"
              class="om-host"
              size="small"
              :placeholder="curSession ? '绑定主机' : '绑定主机（可选）'"
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
            <span class="om-kbd">Ctrl + Enter 发送</span>
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
  LoadingOutlined, ExclamationCircleOutlined, ThunderboltOutlined, ApiOutlined,
  UserOutlined, ToolOutlined, CloseCircleOutlined, DashboardOutlined,
  SafetyOutlined, CloudServerOutlined, CompressOutlined,
} from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useOpsAgent, type OpsMessage, type OpsSession } from '../store/opsagent'
import { wsTopic, get } from '../api'
import { usePolling } from '../composables/usePolling'

const ops = useOpsAgent()
const input = ref('')
const hostId = ref<number | undefined>(undefined)
const hosts = ref<{ id: number; name: string }[]>([])
const sending = ref(false)
const scrollRef = ref<HTMLElement | null>(null)
const taRef = ref<HTMLTextAreaElement | null>(null)
const isMobile = ref(window.innerWidth < 768)
const composerFocus = ref(false)

const curSession = computed(() => ops.sessions.find(s => s.id === ops.currentId) || null)
const curMessages = computed(() => (ops.currentId != null ? ops.messages[ops.currentId] || [] : []))
const curError = computed(() => curSession.value?.error || '')
const modeLabel = computed(() =>
  ({ observe: '仅观察', confirm: '执行前确认', auto: '自动处置' }[ops.settings?.agent.defenseMode || ''] || ''))
const modeColor = computed(() =>
  ({ observe: 'blue', confirm: 'orange', auto: 'red' }[ops.settings?.agent.defenseMode || ''] || 'default'))
const pendingMsg = computed(() => curMessages.value.find(m => m.role === 'tool' && isPending(m)) || null)

const statusLabel = (s: string) =>
  ({ running: '运行中', waiting_confirm: '待确认', idle: '已完成', interrupted: '已中断', error: '出错' }[s] || s)
const sourceLabel = (s: OpsSession) =>
  s.source === 'inspect' ? '巡检' : s.source.startsWith('event:') ? '事件' : ''
const dotClass = (s: OpsSession) =>
  (({ running: 'run', waiting_confirm: 'confirm', error: 'err' }) as Record<string, string>)[s.status] || ''

const toolCallsOf = (m: OpsMessage): { id: string; name: string; arguments?: string }[] => {
  try {
    const meta = m.meta ? JSON.parse(m.meta) : null
    return meta?.toolCalls || []
  } catch { return [] }
}
const isPending = (m: OpsMessage) => !!m.meta && m.meta.includes('"pending":true')
const isErrText = (m: OpsMessage) => !!m.meta && m.meta.includes('"error":true') || m.content.startsWith('⚠')
const summaryCount = (m: OpsMessage) => {
  try { return JSON.parse(m.meta || '{}')?.count ?? '?' } catch { return '?' }
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
const fmtArgs = (a?: string) => {
  if (!a) return ''
  const s = a.replace(/\s+/g, ' ')
  return s.length > 72 ? s.slice(0, 72) + '…' : s
}
const prettyArgs = (a?: string) => {
  try { return JSON.stringify(JSON.parse(a || '{}'), null, 2) } catch { return a || '' }
}
const fmtTime = (t: string) => {
  const d = new Date(t)
  return `${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const fillExample = (t: string) => {
  input.value = t
  nextTick(() => taRef.value?.focus())
}
// 新会话：回到空态引导页并聚焦输入框（输入区常驻，发送即创建会话）
const startNew = () => {
  ops.selectSession(null)
  nextTick(() => taRef.value?.focus())
}

async function send() {
  const msg = input.value.trim()
  if (!msg || sending.value) return
  sending.value = true
  try {
    if (ops.currentId == null) {
      await ops.newSession(msg, hostId.value)
    } else {
      await ops.sendMessage(ops.currentId, msg)
    }
    input.value = ''
  } catch (e: any) {
    message.error(e?.response?.data?.error || '发送失败')
  } finally {
    sending.value = false
    scrollBottom()
  }
}

async function openWorkbench() {
  ops.open = true
  await Promise.all([ops.fetchSessions(), ops.loadSettings(), loadHosts()])
  if (ops.currentId == null && ops.sessions.length) ops.selectSession(ops.sessions[0].id)
  scrollBottom()
}

async function loadHosts() {
  if (hosts.value.length) return
  try {
    const list = await get<any[]>('/api/hosts')
    hosts.value = (list || []).map(h => ({ id: h.id, name: h.local ? h.name + '（本机）' : h.name }))
  } catch { /* ignore */ }
}

function scrollBottom() {
  nextTick(() => {
    const el = scrollRef.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

watch(() => curMessages.value.length, scrollBottom)
watch(() => curSession.value?.status, scrollBottom)

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
.om-body { display: flex; height: min(70vh, 640px); background: var(--surface-card, transparent); }

/* ---- 左栏会话列表 ---- */
.om-side {
  width: 248px; flex-shrink: 0; display: flex; flex-direction: column; gap: 10px;
  padding: 14px 12px; border-right: 1px solid var(--border-card, rgba(128,128,128,.18));
  background: rgba(128,128,128,.04);
}
.om-new { border-radius: 8px; font-weight: 600; }
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
.om-sess-title {
  flex: 1; min-width: 0; font-size: 12.5px; color: var(--color-ink);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
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

/* ---- 消息流 ---- */
.om-msgs { flex: 1; overflow-y: auto; padding: 16px 18px; display: flex; flex-direction: column; gap: 4px; }
.om-row { display: flex; gap: 8px; margin-top: 10px; align-items: flex-start; }
.om-row.right { justify-content: flex-end; }
.om-avatar {
  width: 28px; height: 28px; border-radius: 50%; flex-shrink: 0; display: grid; place-items: center; font-size: 13px;
  background: rgba(22,119,255,.14); color: #1677ff;
}
.om-avatar.me { background: rgba(128,128,128,.16); color: var(--color-slate); }
.om-avatar.tool { background: rgba(103,194,58,.16); color: #67c23a; }
.om-bubble {
  max-width: 76%; border-radius: 14px; padding: 9px 13px; font-size: 13px; line-height: 1.65;
  white-space: pre-wrap; word-break: break-word;
}
.om-user {
  background: linear-gradient(135deg, #1677ff, #4c8dff); color: #fff;
  border-bottom-right-radius: 5px; box-shadow: 0 4px 12px rgba(22,119,255,.22);
}
.om-assist {
  background: var(--surface-card, rgba(128,128,128,.08));
  border: 1px solid var(--border-card, rgba(128,128,128,.16));
  border-top-left-radius: 5px; color: var(--color-ink);
}
.om-err-text { color: #ef4444; }
.om-calls { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.om-call-chip {
  display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-family: ui-monospace, Consolas, monospace;
  background: rgba(22,119,255,.1); color: #1677ff; border: 1px solid rgba(22,119,255,.25);
  border-radius: 999px; padding: 2px 10px;
}

/* 事件横幅 */
.om-ev {
  display: flex; gap: 9px; margin: 10px 0; padding: 10px 13px; border-radius: 10px;
  background: rgba(245,158,11,.08); border: 1px solid rgba(245,158,11,.4); border-left-width: 3px;
}
.om-ev-icon { color: #f59e0b; font-size: 14px; margin-top: 2px; }
.om-ev-text { margin: 0; flex: 1; white-space: pre-wrap; word-break: break-all; font-size: 12px; line-height: 1.6; color: #b45309; font-family: inherit; }
html[data-theme='dark'] .om-ev-text { color: #fbbf24; }

/* 工具卡 */
.om-tool {
  border: 1px solid var(--border-card, rgba(128,128,128,.18)); border-left: 3px solid #67c23a;
  border-radius: 10px; padding: 8px 12px; font-size: 12px; max-width: 82%;
  background: var(--surface-card, rgba(128,128,128,.05));
}
.om-tool.pending { border-left-color: #f59e0b; }
.om-tool-head { display: flex; align-items: center; gap: 7px; min-width: 0; }
.om-tool-name { color: #1677ff; font-weight: 600; font-family: ui-monospace, Consolas, monospace; flex-shrink: 0; }
.om-tool-args {
  color: var(--color-slate); font-family: ui-monospace, Consolas, monospace;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0;
}
.om-tool-out {
  margin: 7px 0 0; padding: 8px 10px; max-height: 200px; overflow: auto;
  background: rgba(128,128,128,.07); border-radius: 6px;
  white-space: pre-wrap; word-break: break-word; font-size: 12px; line-height: 1.55;
  color: var(--color-slate); font-family: ui-monospace, Consolas, monospace;
}

/* 确认卡 */
.om-confirm {
  margin: 0 16px 10px; border: 1px solid rgba(245,158,11,.5); border-radius: 12px;
  background: rgba(245,158,11,.07); padding: 12px 14px;
  box-shadow: 0 6px 18px rgba(245,158,11,.12);
}
.om-confirm-title { font-size: 12.5px; font-weight: 600; color: #b45309; display: flex; align-items: center; gap: 6px; }
html[data-theme='dark'] .om-confirm-title { color: #fbbf24; }
.om-confirm-tool { display: flex; gap: 8px; margin-top: 8px; align-items: baseline; min-width: 0; }
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
.om-composer-bar { display: flex; align-items: center; gap: 8px; padding: 4px 8px 8px 4px; }
.om-host { min-width: 170px; flex-shrink: 0; }
.om-kbd { margin-left: auto; font-size: 11px; color: var(--color-slate); }
.om-ctx { margin-left: auto; font-family: ui-monospace, Consolas, monospace; }
.om-ctx + .om-kbd { margin-left: 0; }
.om-ctx.warn { color: #f59e0b; font-weight: 600; }
.om-compact {
  text-align: center; font-size: 11px; color: var(--color-slate); margin: 8px 0;
  display: flex; align-items: center; justify-content: center; gap: 6px;
}
.om-compact::before, .om-compact::after {
  content: ''; height: 1px; flex: 1; background: rgba(128,128,128,.2);
}
.om-send { border-radius: 8px; font-weight: 600; }

@media (max-width: 768px) {
  .om-side { width: 168px; padding: 10px 8px; }
  .om-guide-cards { flex-direction: column; }
  .om-guide-card { width: 100%; }
}
</style>

<style>
/* 弹框框架样式（teleport 到 body，需全局作用域，用 wrap-class 限定） */
.ops-workbench .ant-modal-content { padding: 0; overflow: hidden; border-radius: 14px; }
.ops-workbench .ant-modal-header {
  padding: 14px 20px; margin-bottom: 0;
  border-bottom: 1px solid var(--border-card, rgba(128,128,128,.16));
  background: linear-gradient(180deg, rgba(22,119,255,.06), transparent);
}
.ops-workbench .ant-modal-close { top: 14px; right: 16px; }
.owm-title { display: flex; align-items: center; gap: 11px; padding-right: 34px; }
.owm-logo {
  width: 36px; height: 36px; border-radius: 10px; display: inline-grid; place-items: center;
  font-size: 18px; color: #fff; background: linear-gradient(135deg, #1677ff, #6a5cff);
  box-shadow: 0 6px 16px rgba(22,119,255,.3);
}
.owm-name { display: flex; flex-direction: column; gap: 1px; line-height: 1.3; }
.owm-name b { font-size: 15px; color: var(--color-ink, #fff); }
.owm-name small { font-size: 11px; font-weight: 400; color: var(--color-slate, #999); }
.owm-mode { margin-left: auto !important; }
</style>
