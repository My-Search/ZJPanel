<template>
  <!--
    思考过程折叠卡（对齐 ZCode ReasoningRow 的产品决策）：
    默认收起（流式中也收起）；触发器 = 脑图标 + shimmer「思考中…」/「已思考 Ns」
    + 流式时展示推理文本最后一行的实时预览；内容为 pre-wrap 纯文本（不做 markdown，
    长思考逐块重渲成本高），限高内滚动，展开且流式中自动贴底。
  -->
  <div class="om-reason" :class="{ open }">
    <button type="button" class="om-reason-head" @click="toggle">
      <BulbOutlined class="om-reason-icon" />
      <span v-if="streaming" class="om-shimmer om-reason-title">思考中…</span>
      <span v-else class="om-reason-title">已思考{{ durationText }}</span>
      <span v-if="streaming && preview" class="om-reason-preview">{{ preview }}</span>
      <CaretRightFilled class="om-reason-caret" :class="{ open }" />
    </button>
    <div v-if="open" ref="bodyRef" class="om-reason-body">{{ text }}</div>
  </div>
</template>

<script lang="ts">
// 模块级展开状态（按消息 id 记忆）：重挂载/流式定稿后不丢失
const reasonOpenMap = new Map<string, boolean>()
export default { name: 'OpsReasoningCard' }
</script>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { BulbOutlined, CaretRightFilled } from '@ant-design/icons-vue'

const props = withDefaults(defineProps<{
  id: string
  text: string
  streaming?: boolean
  durationMs?: number
}>(), { streaming: false })

const open = ref(reasonOpenMap.get(props.id) ?? false)
watch(() => props.id, id => { open.value = reasonOpenMap.get(id) ?? false })
const toggle = () => {
  open.value = !open.value
  reasonOpenMap.set(props.id, open.value)
}

const durationText = computed(() => {
  if (!props.durationMs || props.durationMs <= 0) return ''
  return ' ' + (Math.round(props.durationMs / 100) / 10) + ' 秒'
})

// 流式预览：推理文本最后一个非空行（单行省略）
const preview = computed(() => {
  const lines = props.text.split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i].trim()
    if (l) return l
  }
  return ''
})

const bodyRef = ref<HTMLElement | null>(null)
// 展开且流式中：内容增长时贴底（仅跟随时；用户上滚该内滚区不拉回）
watch([() => props.text, open], () => {
  if (!open.value || !props.streaming) return
  void nextTick(() => {
    const el = bodyRef.value
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
      el.scrollTop = el.scrollHeight
    }
  })
})
</script>

<style scoped>
.om-reason { margin: 6px 0; }
.om-reason-head {
  display: inline-flex; align-items: center; gap: 6px; border: none; background: transparent;
  cursor: pointer; padding: 4px 8px; border-radius: 8px; max-width: 100%;
  color: var(--color-slate, #909399); font-size: 12px;
}
.om-reason-head:hover { background: rgba(128, 128, 128, 0.1); }
.om-reason-icon { font-size: 13px; color: #a855f7; flex-shrink: 0; }
.om-reason-title { flex-shrink: 0; font-weight: 500; }
.om-reason-preview {
  min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-slate, #909399); opacity: 0.9;
}
.om-reason-caret { font-size: 10px; transition: transform 0.15s; flex-shrink: 0; }
.om-reason-caret.open { transform: rotate(90deg); }
.om-reason-body {
  margin: 4px 0 0 8px; padding: 8px 12px; border-left: 2px solid rgba(168, 85, 247, 0.45);
  max-height: 240px; overflow-y: auto; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.65; color: var(--color-slate, #909399);
  background: rgba(128, 128, 128, 0.05); border-radius: 0 8px 8px 0;
}
</style>
