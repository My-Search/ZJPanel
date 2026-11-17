<template>
  <div class="domain-input">
    <div class="tag-row">
      <a-tag v-for="(d, i) in modelValue" :key="d" color="blue" closable @close="removeAt(i)">
        {{ d }}
      </a-tag>
      <a-input
        ref="inputRef"
        v-model:value="text"
        class="tag-input"
        size="small"
        :placeholder="modelValue.length ? '' : placeholder"
        @press-enter="commit"
        @blur="commit"
        @paste="onPaste"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
// DomainInput 域名标签输入（标准 tag 语义）：
//   · 输入/粘贴的内容按逗号、空格、换行拆分为多个域名，逐个成为独立 tag
//   · 点单个 tag 的 × 只删除该 tag，不影响其它 tag
//   · 不残留已提交的原文（自建实现，规避 a-select mode="tags" 的输入框残留问题）
import { nextTick, ref } from 'vue'

const props = withDefaults(defineProps<{ modelValue: string[]; placeholder?: string }>(), {
  placeholder: '输入域名后回车（支持逗号/空格分隔）',
})
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const text = ref('')
const inputRef = ref<any>(null)

const splitDomains = (s: string): string[] =>
  s.split(/[\s,;]+/).map(d => d.trim().toLowerCase().replace(/\.$/, '')).filter(Boolean)

const commit = () => {
  const parts = splitDomains(text.value)
  text.value = ''
  if (!parts.length) return
  const cur = props.modelValue || []
  const next = [...cur]
  for (const d of parts) if (!next.includes(d)) next.push(d) // 去重
  if (next.length !== cur.length) emit('update:modelValue', next)
}

const removeAt = (i: number) => {
  const next = [...(props.modelValue || [])]
  next.splice(i, 1)
  emit('update:modelValue', next)
}

// 粘贴多段：等浏览器把内容写入输入框后再统一拆分提交
const onPaste = async () => {
  await nextTick()
  commit()
}

defineExpose({ focus: () => inputRef.value?.focus?.() })
</script>

<style scoped>
.domain-input { width: 100%; }
.tag-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 4px 6px;
  border: 1px solid var(--color-border, #d9d9d9);
  border-radius: 6px;
  background: var(--color-bg, #fff);
}
.tag-row :deep(.ant-tag) { margin-inline-end: 0; }
.tag-input { flex: 1; min-width: 180px; border: none; box-shadow: none; padding: 0 4px; }
.tag-input:focus, .tag-input :deep(.ant-input) { box-shadow: none; }
</style>
