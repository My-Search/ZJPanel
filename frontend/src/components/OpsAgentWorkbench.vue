<template>
  <!-- 顶栏触发：常驻 WS 连接，运行中会话数作角标（与会话状态解耦，弹框开关不影响） -->
  <a-badge :count="ops.running" :offset="[-2, 4]" size="small">
    <a-button type="text" class="ops-btn" title="管理 Agent（AI 运维）" @click="openWorkbench">
      <RobotOutlined />
    </a-button>
  </a-badge>

  <a-modal
    v-model:open="ops.open"
    :width="isMobile ? '94%' : 1040"
    :footer="null"
    centered
    class="ops-modal"
    :body-style="{ padding: 0 }"
  >
    <template #title>
      <div class="om-title">
        <RobotOutlined />
        <b>管理 Agent</b>
        <a-tag v-if="modeLabel" color="blue" style="margin-right: 0">{{ modeLabel }}</a-tag>
        <span class="om-hint">会话在服务器后台运行，关闭/刷新页面不中断</span>
      </div>
    </template>

    <div class="om-body">
      <!-- 左栏：会话列表 -->
      <aside class="om-side">
        <a-button type="primary" block @click="ops.selectSession(null)">
          <PlusOutlined /> 新会话
        </a-button>
        <div class="om-sessions">
          <div
            v-for="s in ops.sessions"
            :key="s.id"
            class="om-sess"
            :class="{ on: s.id === ops.currentId }"
            @click="ops.selectSession(s.id)"
          >
            <span class="om-dot" :class="dotClass(s)">
              <LoadingOutlined v-if="s.status === 'running'" spin />
            </span>
            <div class="om-sess-main">
              <div class="om-sess-title">{{ s.title || '（无标题）' }}</div>
              <div class="om-sess-meta">{{ statusLabel(s.status) }} · {{ fmtTime(s.updatedAt) }}</div>
            </div>
            <a-popconfirm title="删除该会话及消息？" @confirm="ops.removeSession(s.id)">
              <a class="om-sess-del" @click.stop><DeleteOutlined /></a>
            </a-popconfirm>
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
          message="尚未配置模型：请前往「设置 → 智能助手」填写 LLM 地址、API Key 与模型名后再开始对话。"
          style="margin: 12px 14px 0"
        />
        <div v-if="curError" class="om-error">{{ curError }}</div>

        <div ref="scrollRef" class="om-msgs">
          <div v-if="ops.currentId == null" class="om-guide">
            <RobotOutlined class="om-guide-icon" />
            <p>描述你的运维任务，例如：</p>
            <p class="om-guide-eg" @click="fillExample('查看面板整体状态，检查有没有证书即将到期或站点异常')">「查看面板整体状态，检查证书临期与站点异常」</p>
            <p class="om-guide-eg" @click="fillExample('分析最近的安全事件，如有攻击来源请给出处置建议')">「分析最近的安全事件并给出处置建议」</p>
            <p class="om-guide-eg" @click="fillExample('检查所有主机的磁盘与内存占用，汇总成表格')">「检查所有主机的磁盘与内存占用」</p>
          </div>

          <template v-for="m in curMessages" :key="m.id">
            <!-- 事件横幅 -->
            <div v-if="m.role === 'event'" class="om-ev">{{ m.content }}</div>
            <!-- 用户气泡 -->
            <div v-else-if="m.role === 'user'" class="om-row om-row-user">
              <div class="om-bubble om-bubble-user">{{ m.content }}</div>
            </div>
            <!-- 助手 -->
            <div v-else-if="m.role === 'assistant'" class="om-row">
              <div class="om-bubble om-bubble-assist">
                <div v-if="m.content" class="om-text">{{ m.content }}</div>
                <div v-if="toolCallsOf(m).length" class="om-calls">
                  <span v-for="tc in toolCallsOf(m)" :key="tc.id" class="om-call-chip">🔧 {{ tc.name }}</span>
                </div>
              </div>
            </div>
            <!-- 工具执行 -->
            <div v-else-if="m.role === 'tool'" class="om-row">
              <div class="om-tool" :class="{ pending: isPending(m) }">
                <div class="om-tool-head">
                  <LoadingOutlined v-if="isPending(m)" spin />
                  <span class="om-tool-name">{{ m.toolName }}</span>
                  <span class="om-tool-args">{{ fmtArgs(m.toolArgs) }}</span>
                </div>
                <pre v-if="m.content" class="om-tool-out">{{ m.content }}</pre>
              </div>
            </div>
          </template>
        </div>

        <!-- confirm 模式：待确认操作卡片 -->
        <div v-if="pendingMsg && curSession?.status === 'waiting_confirm'" class="om-confirm">
          <div class="om-confirm-title"><ExclamationCircleOutlined /> 待确认操作（「执行前确认」模式）</div>
          <div class="om-confirm-body">
            <div><b>{{ pendingMsg.toolName }}</b></div>
            <pre class="om-confirm-args">{{ prettyArgs(pendingMsg.toolArgs) }}</pre>
          </div>
          <a-space>
            <a-button type="primary" danger @click="ops.confirm(curSession!.id, true)">确认执行</a-button>
            <a-button @click="ops.confirm(curSession!.id, false)">拒绝</a-button>
          </a-space>
        </div>

        <!-- 输入区 -->
        <div class="om-input">
          <a-select
            v-model:value="hostId"
            class="om-host"
            size="small"
            :placeholder="curSession ? '绑定主机' : '绑定主机（可选）'"
            :disabled="!!curSession"
            allow-clear
          >
            <a-select-option v-for="h in hosts" :key="h.id" :value="h.id">{{ h.name }}</a-select-option>
          </a-select>
          <div class="om-input-row">
            <a-textarea
              v-model:value="input"
              placeholder="描述任务，Ctrl+Enter 发送。Agent 可查询/操作面板，并在授权模式下执行防御动作。"
              :auto-size="{ minRows: 1, maxRows: 4 }"
              :disabled="!!curSession && curSession.status === 'waiting_confirm'"
              @keydown.ctrl.enter="send"
              @keydown.meta.enter="send"
            />
            <a-button
              v-if="curSession && curSession.status === 'running'"
              danger
              @click="ops.stop(curSession.id)"
            ><StopOutlined /> 停止</a-button>
            <a-button v-else type="primary" :loading="sending" :disabled="!input.trim()" @click="send">
              <SendOutlined /> 发送
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
  RobotOutlined, PlusOutlined, DeleteOutlined, SendOutlined, StopOutlined,
  LoadingOutlined, ExclamationCircleOutlined,
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
const isMobile = ref(window.innerWidth < 768)

const curSession = computed(() => ops.sessions.find(s => s.id === ops.currentId) || null)
const curMessages = computed(() => (ops.currentId != null ? ops.messages[ops.currentId] || [] : []))
const curError = computed(() => curSession.value?.error || '')
const modeLabel = computed(() =>
  ({ observe: '仅观察', confirm: '执行前确认', auto: '自动处置' }[ops.settings?.agent.defenseMode || ''] || ''))
const pendingMsg = computed(() => curMessages.value.find(m => m.role === 'tool' && isPending(m)) || null)

const statusLabel = (s: string) =>
  ({ running: '运行中', waiting_confirm: '待确认', idle: '已完成', interrupted: '已中断', error: '出错' }[s] || s)
const dotClass = (s: OpsSession) =>
  (({ running: 'run', waiting_confirm: 'confirm', error: 'err' }) as Record<string, string>)[s.status] || ''

const toolCallsOf = (m: OpsMessage): { id: string; name: string }[] => {
  try {
    const meta = m.meta ? JSON.parse(m.meta) : null
    return meta?.toolCalls || []
  } catch { return [] }
}
const isPending = (m: OpsMessage) => !!m.meta && m.meta.includes('"pending":true')
const fmtArgs = (a?: string) => {
  if (!a) return ''
  const s = a.replace(/\s+/g, ' ')
  return s.length > 60 ? s.slice(0, 60) + '…' : s
}
const prettyArgs = (a?: string) => {
  try { return JSON.stringify(JSON.parse(a || '{}'), null, 2) } catch { return a || '' }
}
const fmtTime = (t: string) => {
  const d = new Date(t)
  return `${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const fillExample = (t: string) => { input.value = t }

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
.om-title { display: flex; align-items: center; gap: 8px; }
.om-hint { margin-left: auto; font-size: 11.5px; font-weight: 400; color: var(--color-slate); }

.om-body { display: flex; height: min(68vh, 620px); }
.om-side { width: 230px; flex-shrink: 0; border-right: 1px solid rgba(128,128,128,.18); padding: 12px; display: flex; flex-direction: column; gap: 10px; }
.om-sessions { flex: 1; overflow: auto; }
.om-sess { display: flex; align-items: center; gap: 8px; padding: 8px; border-radius: 8px; cursor: pointer; }
.om-sess:hover { background: rgba(128,128,128,.1); }
.om-sess.on { background: rgba(22,119,255,.12); }
.om-dot { width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0; background: #999; display: grid; place-items: center; font-size: 10px; }
.om-dot.run { background: #1677ff; }
.om-dot.confirm { background: #f59e0b; }
.om-dot.err { background: #ef4444; }
.om-sess-main { flex: 1; min-width: 0; }
.om-sess-title { font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.om-sess-meta { font-size: 11px; color: var(--color-slate); }
.om-sess-del { font-size: 12px; color: var(--color-slate); }
.om-sess-del:hover { color: #ef4444; }
.om-empty { text-align: center; color: var(--color-slate); font-size: 12px; padding: 24px 0; }

.om-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.om-error { margin: 10px 14px 0; padding: 6px 10px; border-radius: 8px; background: rgba(239,68,68,.12); color: #ef4444; font-size: 12px; }
.om-msgs { flex: 1; overflow: auto; padding: 14px; }
.om-guide { text-align: center; color: var(--color-slate); font-size: 13px; padding-top: 12vh; }
.om-guide-icon { font-size: 42px; margin-bottom: 12px; }
.om-guide-eg { cursor: pointer; color: var(--color-corporate-blue); margin: 6px 0; font-size: 12.5px; }
.om-ev { white-space: pre-wrap; font-size: 12px; background: rgba(245,158,11,.1); border: 1px solid rgba(245,158,11,.35); color: #b45309; border-radius: 8px; padding: 8px 12px; margin: 8px 0; }
html[data-theme='dark'] .om-ev { color: #fbbf24; }

.om-row { display: flex; margin: 8px 0; }
.om-row-user { justify-content: flex-end; }
.om-bubble { max-width: 82%; border-radius: 10px; padding: 8px 12px; font-size: 13px; }
.om-bubble-user { background: var(--color-corporate-blue); color: #fff; white-space: pre-wrap; word-break: break-word; }
.om-bubble-assist { background: rgba(128,128,128,.1); white-space: pre-wrap; word-break: break-word; }
.om-text { white-space: pre-wrap; }
.om-calls { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.om-call-chip { font-size: 11px; font-family: ui-monospace, Consolas, monospace; background: rgba(22,119,255,.14); color: var(--color-corporate-blue); border-radius: 6px; padding: 2px 8px; }

.om-tool { border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 6px 10px; font-size: 12px; max-width: 88%; background: rgba(128,128,128,.06); }
.om-tool.pending { border-color: rgba(245,158,11,.5); }
.om-tool-head { display: flex; align-items: center; gap: 6px; font-family: ui-monospace, Consolas, monospace; }
.om-tool-name { color: var(--color-corporate-blue); font-weight: 600; }
.om-tool-args { color: var(--color-slate); word-break: break-all; }
.om-tool-out { margin: 6px 0 0; max-height: 180px; overflow: auto; white-space: pre-wrap; word-break: break-word; font-size: 12px; color: var(--color-slate); }

.om-confirm { margin: 0 14px; border: 1px solid rgba(245,158,11,.5); background: rgba(245,158,11,.08); border-radius: 10px; padding: 10px 12px; }
.om-confirm-title { font-size: 12.5px; font-weight: 600; color: #b45309; margin-bottom: 6px; }
html[data-theme='dark'] .om-confirm-title { color: #fbbf24; }
.om-confirm-args { font-size: 12px; font-family: ui-monospace, Consolas, monospace; max-height: 120px; overflow: auto; margin: 6px 0; white-space: pre-wrap; word-break: break-all; }

.om-input { border-top: 1px solid rgba(128,128,128,.18); padding: 10px 14px; display: flex; flex-direction: column; gap: 8px; }
.om-host { width: 200px; }
.om-input-row { display: flex; gap: 8px; align-items: flex-end; }

@media (max-width: 768px) {
  .om-side { width: 150px; padding: 8px; }
  .om-hint { display: none; }
}
</style>
