<template>
  <!--
    回合容器（对齐 ZCode ConversationTurnGroup）：
    输入行（用户右对齐气泡 / 事件横幅）+ 可折叠工作段（「已工作 N 秒」摘要条包住
    中间助手文本/思考卡/工具卡）+ 最终回答（思考卡 + 全宽流式 markdown）。
    工作段默认：运行中/失败/无最终回答时展开，其余折叠（完成自动收起）；
    用户手动切换后按记忆状态（模块级 Map）。
  -->
  <div class="om-turn">
    <!-- 输入行 -->
    <div v-if="turn.input && !turn.inputIsEvent" class="om-turn-user-row">
      <div class="om-turn-user">{{ turn.input.content }}</div>
    </div>
    <div v-else-if="turn.input" class="om-turn-event">
      <ThunderboltOutlined class="om-turn-ev-icon" />
      <pre class="om-turn-ev-text">{{ turn.input.content }}</pre>
    </div>

    <!-- 工作段（可折叠） -->
    <div v-if="turn.work.length" class="om-turn-work">
      <button type="button" class="om-turn-work-head" @click="toggleWork">
        <CaretRightFilled class="om-turn-caret" :class="{ open: workOpen }" />
        <span v-if="turn.running" class="om-shimmer">正在处理{{ elapsedSuffix }}</span>
        <span v-else class="om-turn-work-title">
          已工作{{ doneDuration }}<span v-if="turn.failed" class="om-turn-fail"> · 出现错误</span>
        </span>
      </button>
      <div v-if="workOpen" class="om-turn-work-body">
        <template v-for="row in turn.work" :key="row.id">
          <div v-if="row.role === 'assistant'" class="om-turn-work-assist">
            <OpsReasoningCard
              v-if="row.reasoning"
              :id="'r' + row.id"
              :text="row.reasoning"
              :streaming="reasoningLive(row)"
              :duration-ms="rowDuration(row)"
            />
            <StreamMarkdown v-if="row.content" :text="row.content" />
          </div>
          <OpsToolCallCard v-else :msg="row" />
        </template>
      </div>
    </div>

    <!-- 最终回答 -->
    <div v-if="turn.final" class="om-turn-final" :class="{ err: isErrText(turn.final) }">
      <OpsReasoningCard
        v-if="turn.final.reasoning"
        :id="'r' + turn.final.id"
        :text="turn.final.reasoning"
        :streaming="reasoningLive(turn.final)"
        :duration-ms="rowDuration(turn.final)"
      />
      <StreamMarkdown :text="turn.final.content" :streaming="isStreaming(turn.final)" />
    </div>
  </div>
</template>

<script lang="ts">
// 模块级工作段展开状态（按回合 key 记忆）
const workOpenMap = new Map<string, boolean>()
export default { name: 'OpsTurnGroup' }
</script>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { CaretRightFilled, ThunderboltOutlined } from '@ant-design/icons-vue'
import type { OpsMessage } from '../../store/opsagent'
import type { OpsTurn } from '../../composables/useConversationTurns'
import { turnWorkDefaultOpen } from '../../composables/useConversationTurns'
import { isErrText, isStreaming } from './meta'
import StreamMarkdown from './StreamMarkdown.vue'
import OpsReasoningCard from './OpsReasoningCard.vue'
import OpsToolCallCard from './OpsToolCallCard.vue'

const props = defineProps<{ turn: OpsTurn }>()

const workOpen = ref(workOpenMap.get(props.turn.key) ?? turnWorkDefaultOpen(props.turn))
const toggleWork = () => {
  workOpen.value = !workOpen.value
  workOpenMap.set(props.turn.key, workOpen.value)
}
// 用户未手动操作过时跟随默认值（运行中→完成 自动收起；失败→展开）
watch(() => props.turn.key, k => {
  workOpen.value = workOpenMap.get(k) ?? turnWorkDefaultOpen(props.turn)
})
watch(() => turnWorkDefaultOpen(props.turn), d => {
  if (!workOpenMap.has(props.turn.key)) workOpen.value = d
})

const ts = (m: OpsMessage) => {
  const t = new Date(m.createdAt).getTime()
  return isNaN(t) ? 0 : t
}

// 运行中的实时计时（1s tick；停止即销毁）
const now = ref(Date.now())
let timer: number | undefined
watch(() => props.turn.running, r => {
  if (r && timer === undefined) {
    now.value = Date.now()
    timer = window.setInterval(() => { now.value = Date.now() }, 1000)
  } else if (!r && timer !== undefined) {
    clearInterval(timer)
    timer = undefined
  }
}, { immediate: true })
onUnmounted(() => { if (timer !== undefined) clearInterval(timer) })

const elapsedSuffix = computed(() => {
  const start = props.turn.work.length
    ? ts(props.turn.work[0])
    : (props.turn.input ? ts(props.turn.input) : now.value)
  const s = Math.max(0, Math.round((now.value - start) / 1000))
  return s > 0 ? ` ${s} 秒` : ''
})

const doneDuration = computed(() => {
  const ms = props.turn.durationMs
  if (ms <= 0) return ''
  const s = Math.round(ms / 100) / 10
  return s >= 60 ? ` ${Math.floor(s / 60)} 分 ${Math.round(s % 60)} 秒` : ` ${s} 秒`
})

// 思考卡流式判定：行仍在流式且正文尚未开始（正文一旦出现，思考视为结束）
const reasoningLive = (m: OpsMessage) => isStreaming(m) && !m.content
// 思考时长：该行 createdAt → 下一条工作行/最终行 createdAt（无法精确时省略时长文案）
const rowDuration = (m: OpsMessage) => {
  const all = [...props.turn.work, props.turn.final].filter((x): x is OpsMessage => !!x && x.id !== m.id)
  const next = all.map(ts).filter(t => t > ts(m)).sort((a, b) => a - b)[0]
  return next ? next - ts(m) : 0
}
</script>

<style scoped>
.om-turn { display: flex; flex-direction: column; gap: 8px; }

/* 用户输入：右对齐气泡（唯一的气泡元素） */
.om-turn-user-row { display: flex; justify-content: flex-end; }
.om-turn-user {
  max-width: min(620px, 88%); border-radius: 12px 12px 4px 12px; padding: 9px 13px;
  font-size: 13px; line-height: 1.6; white-space: pre-wrap; word-break: break-word;
  background: linear-gradient(135deg, #1677ff, #4c8dff); color: #fff;
  box-shadow: 0 4px 12px rgba(22, 119, 255, 0.22);
}

/* 事件横幅（事件/巡检会话的触发输入） */
.om-turn-event {
  display: flex; gap: 9px; padding: 10px 13px; border-radius: 10px;
  background: rgba(245, 158, 11, 0.08); border: 1px solid rgba(245, 158, 11, 0.4); border-left-width: 3px;
}
.om-turn-ev-icon { color: #f59e0b; font-size: 14px; margin-top: 2px; flex-shrink: 0; }
.om-turn-ev-text {
  margin: 0; flex: 1; min-width: 0; white-space: pre-wrap; word-break: break-all;
  font-size: 12px; line-height: 1.6; color: #b45309; font-family: inherit;
}
html[data-theme='dark'] .om-turn-ev-text { color: #fbbf24; }

/* 工作段 */
.om-turn-work { min-width: 0; }
.om-turn-work-head {
  display: inline-flex; align-items: center; gap: 6px; border: none; background: transparent;
  cursor: pointer; padding: 4px 8px; border-radius: 8px; font-size: 12px;
  color: var(--color-slate, #909399);
}
.om-turn-work-head:hover { background: rgba(128, 128, 128, 0.1); }
.om-turn-work-title { font-weight: 500; }
.om-turn-fail { color: #ef4444; }
.om-turn-caret { font-size: 10px; transition: transform 0.15s; }
.om-turn-caret.open { transform: rotate(90deg); }
.om-turn-work-body {
  margin: 2px 0 0 10px; padding-left: 12px; border-left: 1px solid var(--border-card, rgba(128, 128, 128, 0.18));
  display: flex; flex-direction: column;
}
.om-turn-work-assist { color: var(--color-slate, #595959); font-size: 12.5px; }
.om-turn-work-assist :deep(.om-md) { font-size: 12.5px; color: var(--color-slate, #595959); }

/* 最终回答 */
.om-turn-final { min-width: 0; }
.om-turn-final.err :deep(.om-md) { color: #ef4444; }
</style>
