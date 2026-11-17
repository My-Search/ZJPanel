<template>
  <div class="domain-input" @click="focusInput">
    <a-tag
      v-for="(d, i) in modelValue"
      :key="d"
      color="blue"
      closable
      class="domain-tag"
      @close="removeAt(i)"
    >
      {{ d }}
    </a-tag>
    <input
      ref="inputRef"
      v-model="text"
      class="tag-input"
      :placeholder="modelValue.length ? '' : placeholder"
      @keydown="onKeydown"
      @blur="commit"
      @paste="onPaste"
    />
  </div>
</template>

<script setup lang="ts">
// DomainInput 域名标签输入（标准 tag 语义）：
//   · tag 排在输入框左侧、光标在最右；整个容器即输入框，tag 是框内内容而非框外标签
//   · 输入/粘贴的内容按逗号、空格、换行拆分为多个域名，逐个成为独立 tag
//   · 输入框为空时按 Backspace 删除最右侧 tag，连续按从右到左逐个删除
//   · 点单个 tag 的 × 只删除该 tag，不影响其它 tag
//   · 自建实现，规避 a-select mode="tags" 的输入框残留问题
import { nextTick, ref } from 'vue'

const props = withDefaults(defineProps<{ modelValue: string[]; placeholder?: string }>(), {
  placeholder: '输入域名后回车（支持逗号/空格分隔）',
})
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const text = ref('')
const inputRef = ref<HTMLInputElement | null>(null)

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

// 回车提交；输入框为空时 Backspace 删最右侧 tag（从右到左）
const onKeydown = (e: KeyboardEvent) => {
  if (e.key === 'Enter') {
    e.preventDefault()
    commit()
  } else if (e.key === 'Backspace' && !text.value && props.modelValue.length) {
    e.preventDefault()
    removeAt(props.modelValue.length - 1)
  }
}

// 粘贴多段：等浏览器把内容写入输入框后再统一拆分提交
const onPaste = async () => {
  await nextTick()
  commit()
}

const focusInput = () => inputRef.value?.focus()
defineExpose({ focus: focusInput })
</script>

<style scoped>
.domain-input {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  width: 100%;
  min-height: 32px;
  padding: 4px 6px;
  border: 1px solid var(--border-input);
  border-radius: 6px;
  background: var(--surface-card);
  cursor: text;
}
.domain-tag { margin-inline-end: 0; }
.tag-input {
  flex: 1;
  min-width: 120px;
  padding: 0 4px;
  border: none;
  outline: none;
  background: transparent;
  color: inherit;
  font: inherit;
}
.tag-input::placeholder { color: var(--color-text-4, #bfbfbf); }
</style>
