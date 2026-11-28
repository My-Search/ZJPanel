<template>
  <!--
    工具调用折叠卡（对齐 ZCode ToolLayout/ToolSummaryRow）：
    头部 = 状态图标（挂起 spinner / 完成 check）+ 工具名 + 参数单行摘要 + chevron；
    内容 = pretty 参数 JSON + 输出 pre（限高内滚动）。
    挂起/执行中默认展开、完成自动收起；用户手动切换后按记忆状态（模块级 Map，重挂载不丢）。
  -->
  <div class="om-tc" :class="{ pending: pending }">
    <button type="button" class="om-tc-head" @click="toggle">
      <LoadingOutlined v-if="pending" spin class="om-tc-status" />
      <CheckCircleFilled v-else class="om-tc-status done" />
      <span class="om-tc-name">{{ msg.toolName || '工具' }}</span>
      <span class="om-tc-args">{{ fmtArgs(msg.toolArgs, 88) }}</span>
      <CaretRightFilled class="om-tc-caret" :class="{ open }" />
    </button>
    <div v-if="open" class="om-tc-body">
      <div class="om-tc-label">参数</div>
      <pre class="om-tc-pre">{{ argsPretty }}</pre>
      <template v-if="msg.content">
        <div class="om-tc-label">输出</div>
        <pre class="om-tc-pre out">{{ msg.content }}</pre>
      </template>
    </div>
  </div>
</template>

<script lang="ts">
// 模块级展开状态（按消息 id 记忆）：重渲染/重挂载后不丢失
const toolOpenMap = new Map<number, boolean>()
export default { name: 'OpsToolCallCard' }
</script>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CaretRightFilled, CheckCircleFilled, LoadingOutlined } from '@ant-design/icons-vue'
import type { OpsMessage } from '../../store/opsagent'
import { fmtArgs, isPending, prettyArgs } from './meta'

const props = defineProps<{ msg: OpsMessage }>()

const pending = computed(() => isPending(props.msg))
const argsPretty = computed(() => prettyArgs(props.msg.toolArgs) || '（无参数）')

// 无记忆时：挂起中默认展开，完成默认收起 → 即「完成自动收起」
const open = ref(toolOpenMap.get(props.msg.id) ?? pending.value)
const toggle = () => {
  open.value = !open.value
  toolOpenMap.set(props.msg.id, open.value)
}
watch(() => props.msg.id, () => {
  open.value = toolOpenMap.get(props.msg.id) ?? pending.value
})
// 挂起行从未被手动操作过时，确认完成后自动收起
watch(pending, (p, old) => {
  if (old && !p && !toolOpenMap.has(props.msg.id)) open.value = false
})
</script>

<style scoped>
.om-tc {
  margin: 6px 0 6px 10px; border-radius: 8px;
  border: 1px solid var(--border-card, rgba(128, 128, 128, 0.16));
  background: var(--surface-card, rgba(128, 128, 128, 0.04));
  font-size: 12px; max-width: 720px;
}
.om-tc.pending { border-color: rgba(245, 158, 11, 0.4); }
.om-tc-head {
  display: flex; align-items: center; gap: 8px; width: 100%; border: none;
  background: transparent; cursor: pointer; padding: 7px 10px; text-align: left; min-width: 0;
}
.om-tc-head:hover { background: rgba(128, 128, 128, 0.06); border-radius: 8px; }
.om-tc-status { color: #67c23a; font-size: 13px; flex-shrink: 0; }
.om-tc.pending .om-tc-status { color: #f59e0b; }
.om-tc-name { color: #1677ff; font-weight: 600; font-family: ui-monospace, Consolas, monospace; flex-shrink: 0; }
.om-tc-args {
  color: var(--color-slate, #909399); font-family: ui-monospace, Consolas, monospace; font-size: 11.5px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; flex: 1;
}
.om-tc-caret { font-size: 10px; color: var(--color-slate, #909399); transition: transform 0.15s; flex-shrink: 0; }
.om-tc-caret.open { transform: rotate(90deg); }
.om-tc-body { padding: 2px 10px 10px 12px; }
.om-tc-label { font-size: 10.5px; color: var(--color-slate, #909399); margin: 6px 0 3px; }
.om-tc-pre {
  margin: 0; padding: 7px 10px; border-radius: 6px; background: rgba(128, 128, 128, 0.07);
  white-space: pre-wrap; word-break: break-word; font-size: 11.5px; line-height: 1.55;
  color: var(--color-slate, #595959); font-family: ui-monospace, Consolas, monospace;
  max-height: 130px; overflow: auto;
}
.om-tc-pre.out { max-height: 200px; }
html[data-theme='dark'] .om-tc-pre { color: #b8bcc4; }
</style>
