<template>
  <div class="qc-bar">
    <div class="qc-chips">
      <button v-for="q in cmds" :key="q.id" class="qc-chip" :style="chipStyle(q.color)"
        :title="`${q.command}\n点击发送 · 右键编辑`" :disabled="disabled" @click="run(q)" @contextmenu.prevent="openForm(q)">
        {{ q.name || q.command }}
      </button>
      <button class="qc-chip qc-add" title="新增常用命令" @click="openForm()">＋ 添加</button>
      <span v-if="!cmds.length" class="qc-empty">暂无常用命令，点"＋ 添加"新建</span>
    </div>
    <a-button size="small" class="qc-manage" @click="openManage">
      <SettingOutlined /> 管理
    </a-button>

    <!-- 管理 -->
    <a-modal v-model:open="manageOpen" title="常用命令管理" :footer="null" :width="640">
      <a-button type="primary" size="small" style="margin-bottom: 10px" @click="openForm()">新增命令</a-button>
      <span class="qc-hint">命令条上点击 = 发送执行，右键 = 直接编辑</span>
      <a-table :data-source="cmds" :columns="cols" size="small" row-key="id" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'command'">
            <code class="qc-cmd">{{ record.command }}</code>
          </template>
          <template v-else-if="column.key === 'color'">
            <span class="qc-dot" :style="{ background: record.color || '#d9d9d9' }"></span>
          </template>
          <template v-else-if="column.key === 'act'">
            <a-space>
              <a-button size="small" @click="openForm(record)">编辑</a-button>
              <a-popconfirm title="删除该命令？" @confirm="delCmd(record)">
                <a-button size="small" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-modal>

    <!-- 新增/编辑 -->
    <a-modal v-model:open="form.open" :title="form.id ? '编辑命令' : '新增命令'" @ok="saveForm" :width="480">
      <a-form layout="vertical">
        <a-form-item label="名称（可空，默认显示命令本身）">
          <a-input v-model:value="form.name" placeholder="磁盘占用" />
        </a-form-item>
        <a-form-item label="命令（发送到当前 SSH 会话并回车）">
          <a-textarea v-model:value="form.command" :rows="3" placeholder="df -h" />
        </a-form-item>
        <a-form-item label="标签颜色">
          <div class="qc-swatches">
            <span v-for="c in palette" :key="c" class="qc-dot qc-pick" :style="{ background: c }"
              :class="{ active: form.color === c }" @click="form.color = c"></span>
            <span class="qc-dot qc-pick qc-none" :class="{ active: !form.color }" @click="form.color = ''">无</span>
          </div>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, put, del } from '../api'
import { SettingOutlined } from '@ant-design/icons-vue'
import { useTheme } from '../store/theme'

interface QuickCmd { id: number; name: string; command: string; color: string; usage: number }

const props = defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ (e: 'exec', cmd: string): void }>()

const ui = useTheme()

const cmds = ref<QuickCmd[]>([])
const manageOpen = ref(false)
const form = ref<{ open: boolean; id: number; name: string; command: string; color: string }>(
  { open: false, id: 0, name: '', command: '', color: '' })

// 饱和色系：底色按低透明度铺、文字加深/提亮，深浅主题下都清晰可读
const palette = ['#3b82f6', '#f5222d', '#fa8c16', '#faad14', '#52c41a', '#13c2c2', '#722ed1', '#eb2f96']

// 把十六进制颜色解析成 rgb；标签颜色同时用于底色与文字色，随主题微调
const hexRgb = (hex: string): [number, number, number] | null => {
  const m = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec((hex || '').trim())
  if (!m) return null
  const h = m[1].length === 3 ? m[1].split('').map(c => c + c).join('') : m[1]
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}
const mix = (c: [number, number, number], to: [number, number, number], r: number) =>
  `rgb(${c.map((v, i) => Math.round(v + (to[i] - v) * r)).join(',')})`

const chipStyle = (color?: string) => {
  const dark = ui.isDark
  const rgb = color ? hexRgb(color) : null
  if (!rgb) {
    return dark
      ? { background: 'rgba(255,255,255,.08)', borderColor: 'rgba(255,255,255,.2)', color: 'rgba(255,255,255,.82)' }
      : { background: 'rgba(0,0,0,.04)', borderColor: 'rgba(0,0,0,.14)', color: 'rgba(0,0,0,.72)' }
  }
  return {
    background: `rgba(${rgb.join(',')},${dark ? .16 : .12})`,
    borderColor: `rgba(${rgb.join(',')},${dark ? .45 : .35})`,
    color: mix(rgb, dark ? [255, 255, 255] : [0, 0, 0], dark ? .18 : .4),
  }
}

const cols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 140 },
  { title: '命令', key: 'command' },
  { title: '次数', dataIndex: 'usage', key: 'usage', width: 64 },
  { title: '色', key: 'color', width: 48 },
  { title: '操作', key: 'act', width: 130 },
]

const load = async () => {
  try { cmds.value = await get<QuickCmd[]>('/api/quickcmds') } catch { /* 拦截器统一提示 */ }
}
load()

const run = (q: QuickCmd) => {
  emit('exec', q.command)
  post(`/api/quickcmds/cmds/${q.id}/use`).then(() => {
    q.usage++
  }).catch(() => {})
}

const openManage = () => { manageOpen.value = true; load() }

const openForm = (q?: QuickCmd) => {
  form.value = q
    ? { open: true, id: q.id, name: q.name, command: q.command, color: q.color }
    : { open: true, id: 0, name: '', command: '', color: '' }
}

const saveForm = async () => {
  const f = form.value
  if (!f.command.trim()) { message.warning('命令不能为空'); return }
  const body = { name: f.name, command: f.command, color: f.color }
  if (f.id) await put(`/api/quickcmds/cmds/${f.id}`, body)
  else await post('/api/quickcmds', body)
  message.success('已保存')
  f.open = false
  load()
}

const delCmd = async (q: QuickCmd) => {
  await del(`/api/quickcmds/cmds/${q.id}`)
  load()
}
</script>

<style scoped>
.qc-bar { flex: none; display: flex; align-items: flex-start; gap: 8px; margin-bottom: 8px; }
/* 不撑满整行：管理按钮紧跟最后一个 chip；chip 多时在剩余宽度内换行 */
.qc-chips { min-width: 0; display: flex; flex-wrap: wrap; gap: 6px; }
.qc-chip { border: 1px solid rgba(0,0,0,.14); border-radius: 12px; padding: 1px 10px; font-size: 12px; line-height: 20px; cursor: pointer; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: rgba(0,0,0,.75); }
.qc-chip:disabled { cursor: not-allowed; opacity: .45; }
.qc-chip:not(:disabled):hover { box-shadow: 0 1px 4px rgba(0,0,0,.2); }
html[data-theme='dark'] .qc-chip:not(:disabled):hover { box-shadow: 0 1px 4px rgba(0,0,0,.6); }
.qc-add { background: transparent; border-style: dashed; }
html[data-theme='light'] .qc-add { background: rgba(0,0,0,.03); }
html[data-theme='dark'] .qc-add { background: rgba(255,255,255,.06); color: rgba(255,255,255,.7); border-color: rgba(255,255,255,.25); }
.qc-empty { color: var(--color-slate); font-size: 12px; line-height: 22px; }
.qc-hint { margin-left: 10px; font-size: 12px; color: var(--color-slate); }
.qc-manage { flex: none; }
.qc-cmd { font-family: ui-monospace, Consolas, monospace; font-size: 12px; word-break: break-all; }
.qc-dot { display: inline-block; width: 16px; height: 16px; border-radius: 4px; border: 1px solid rgba(0,0,0,.15); }
.qc-pick { cursor: pointer; }
.qc-pick.active { outline: 2px solid var(--color-action-blue); outline-offset: 2px; }
.qc-none { width: auto; padding: 0 6px; font-size: 11px; line-height: 14px; display: inline-flex; align-items: center; color: #666; }
.qc-swatches { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
</style>
