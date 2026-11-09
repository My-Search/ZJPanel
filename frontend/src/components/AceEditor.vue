<template>
  <div ref="host" style="width: 100%; height: 100%"></div>
</template>

<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import ace from 'ace-builds'
import 'ace-builds/src-noconflict/mode-nginx'
import 'ace-builds/src-noconflict/mode-html'
import 'ace-builds/src-noconflict/mode-sh'
import 'ace-builds/src-noconflict/mode-yaml'
import 'ace-builds/src-noconflict/theme-chrome'
import 'ace-builds/src-noconflict/theme-terminal'
import 'ace-builds/src-noconflict/ext-searchbox'

const props = defineProps<{ modelValue: string; mode?: string; theme?: string; readonly?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const host = ref<HTMLElement | null>(null)
let editor: any = null
let suppress = false

onMounted(() => {
  editor = ace.edit(host.value!)
  editor.setOptions({
    mode: 'ace/mode/' + (props.mode || 'text'),
    theme: 'ace/theme/' + (props.theme || 'chrome'),
    fontSize: '13px',
    useWorker: false,
    readOnly: !!props.readonly,
    tabSize: 4,
    printMargin: false,
  })
  editor.setValue(props.modelValue ?? '', -1)
  editor.on('change', () => {
    if (!suppress) emit('update:modelValue', editor.getValue())
  })
  setTimeout(() => editor?.resize(), 200)
})

watch(() => props.modelValue, (v) => {
  if (editor && editor.getValue() !== v) {
    suppress = true
    const pos = editor.getCursorPosition()
    editor.setValue(v ?? '', -1)
    editor.moveCursorToPosition(pos)
    suppress = false
  }
})
watch(() => props.mode, (m) => editor?.getSession().setMode('ace/mode/' + (m || 'text')))
watch(() => props.readonly, (r) => editor?.setReadOnly(!!r))

onBeforeUnmount(() => editor?.destroy())
</script>
