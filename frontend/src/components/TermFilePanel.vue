<template>
  <div class="fm-panel">
    <template v-if="hostId">
      <!-- ============ 列表视图 ============ -->
      <div v-if="!editing" class="fm-list"
        @dragover.prevent="onDragOver" @dragleave.prevent="onDragLeave"
        @drop.prevent="onDrop">
        <!-- 拖拽遮罩 -->
        <Transition name="fm-drop-fade">
          <div v-if="dragOver" class="fm-drop-overlay">
            <div class="fm-drop-hint">
              <UploadOutlined style="font-size: 36px" />
              <span>松开上传到 {{ path }}</span>
            </div>
          </div>
        </Transition>
        <!-- 头部：地址栏（我在哪）+ 工具栏（我能做什么），两组动作各归其位 -->
        <header class="fm-head">
          <!-- 地址栏：整行路径，末端内嵌「输入路径跳转」入口 -->
          <div class="fm-path">
            <template v-if="!pathEditing">
              <a-breadcrumb class="fm-crumb" separator="/">
                <a-breadcrumb-item>
                  <a class="fm-crumb-link fm-crumb-root" title="根目录" @click="nav('/')">
                    <HomeOutlined /> 根目录
                  </a>
                </a-breadcrumb-item>
                <a-breadcrumb-item v-for="seg in pathSegs" :key="seg.path">
                  <a class="fm-crumb-link" :class="{ 'fm-crumb-cur': seg.path === path }" :title="seg.path"
                    @click="nav(seg.path)">{{ seg.name }}</a>
                </a-breadcrumb-item>
              </a-breadcrumb>
              <button type="button" class="fm-path-edit" title="输入路径跳转" aria-label="输入路径跳转"
                @click="startPathEdit">
                <FormOutlined />
              </button>
            </template>
            <a-input v-else ref="pathInputRef" v-model:value="pathInput" class="fm-path-input" size="small"
              placeholder="/etc/nginx/nginx.conf" @press-enter="nav(pathInput)" @blur="pathEditing = false">
              <template #suffix><span class="fm-dim">回车跳转</span></template>
            </a-input>
          </div>

          <!-- 工具栏：导航组 | 新建组，右侧补当前条目数 -->
          <div class="fm-bar">
            <div class="fm-tools">
              <button type="button" class="fm-tool" :disabled="path === '/'" title="上一级" aria-label="上一级"
                @click="nav(parentPath(path))">
                <ArrowUpOutlined />
              </button>
              <button type="button" class="fm-tool" :class="{ 'is-busy': loading }" title="刷新" aria-label="刷新"
                @click="load">
                <ReloadOutlined />
              </button>
              <button type="button" class="fm-tool" :class="{ 'is-on': showHidden }"
                :title="showHidden ? '不显示隐藏文件' : '显示隐藏文件'"
                :aria-label="showHidden ? '不显示隐藏文件' : '显示隐藏文件'" @click="showHidden = !showHidden">
                <EyeInvisibleOutlined v-if="showHidden" /><EyeOutlined v-else />
              </button>
            </div>
            <span class="fm-rule"></span>
            <div class="fm-tools">
              <button type="button" class="fm-tool" title="新建文件" aria-label="新建文件" @click="promptNew('file')">
                <FileAddOutlined />
              </button>
              <button type="button" class="fm-tool" title="新建文件夹" aria-label="新建文件夹" @click="promptNew('dir')">
                <FolderAddOutlined />
              </button>
              <!-- 原 a-upload 会在触发器外再套一层 role="button"，形成按钮套按钮；
                   改为隐藏 file input + 自有按钮触发，避免嵌套交互元素 -->
              <input ref="fileInputRef" type="file" multiple class="fm-file-input" @change="onPickFiles" />
              <button type="button" class="fm-tool" title="上传到当前目录" aria-label="上传到当前目录"
                @click="pickFiles">
                <UploadOutlined />
              </button>
            </div>
            <span class="fm-grow"></span>
            <span class="fm-count">{{ visibleEntries.length }} 项</span>
          </div>
        </header>

        <div class="fm-table">
          <a-table :data-source="visibleEntries" :columns="cols" size="small" row-key="name" :pagination="false"
            :loading="loading" :custom-row="rowOps" table-layout="fixed">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'name'">
                <span class="fm-name" :title="record.perm ? `${record.name} · ${record.perm}` : record.name">
                  <FolderOutlined v-if="record.isDir" class="fm-ic-dir" />
                  <FileOutlined v-else class="fm-ic-file" />
                  <span class="fm-name-text">{{ record.name }}</span>
                </span>
              </template>
              <template v-else-if="column.key === 'size'">
                <span v-if="record.isDir" class="fm-dim">—</span>
                <template v-else>{{ fmtSize(record.size) }}</template>
              </template>
              <template v-else-if="column.key === 'modTime'">
                {{ fmtTime(record.modTime) }}
              </template>
              <template v-else-if="column.key === 'act'">
                <a-dropdown :trigger="['click']">
                  <a-button size="small" type="text" aria-label="更多操作" title="更多操作">
                    <MoreOutlined />
                  </a-button>
                  <template #overlay>
                    <a-menu @click="({ key }: any) => rowAction(key as string, record)">
                      <a-menu-item v-if="!record.isDir" key="download">下载</a-menu-item>
                      <a-menu-item v-if="!record.isDir" key="edit">编辑</a-menu-item>
                      <a-menu-item key="rename">重命名</a-menu-item>
                      <a-menu-item key="chmod">修改权限</a-menu-item>
                      <a-menu-item key="zip">压缩为 zip</a-menu-item>
                      <a-menu-item key="targz">压缩为 tar.gz</a-menu-item>
                      <a-menu-item v-if="isArchive(record.name)" key="decompress">解压到当前目录</a-menu-item>
                      <a-menu-item key="delete" danger>删除{{ record.isDir ? '（含子目录）' : '' }}</a-menu-item>
                    </a-menu>
                  </template>
                </a-dropdown>
              </template>
            </template>
          </a-table>
        </div>
      </div>

      <!-- ============ 编辑器视图 ============ -->
      <div v-else class="fm-editor">
        <div class="fm-bar fm-editor-bar">
          <a-button size="small" @click="closeEditor"><ArrowLeftOutlined /></a-button>
          <span class="fm-editor-name" :title="editing.path">{{ editing.path }}</span>
          <span v-if="editing.binary" class="fm-warn">二进制文件，不可编辑</span>
          <span v-else-if="editing.truncated" class="fm-warn">文件过大，仅显示前 2MB，保存将覆盖全文！</span>
          <a-tag v-else-if="dirty" color="orange">未保存</a-tag>
          <span class="fm-grow"></span>
          <a-button size="small" type="primary" :disabled="editing.binary || !dirty" @click="save">{{ editing.binary ? '不可编辑' : '保存' }}</a-button>
        </div>
        <div class="fm-editor-box">
          <AceEditor v-model="editing.content" :mode="editMode" :readonly="editing.binary || editing.truncated" />
        </div>
      </div>
    </template>
    <div v-else class="fm-empty">
      <a-empty description="选中左侧 SSH 标签后即可浏览远程文件" />
    </div>

    <!-- 输入弹窗（新建/重命名/权限） -->
    <a-modal v-model:open="prompt.open" :title="prompt.title" @ok="prompt.onOk" :width="420">
      <a-input v-model:value="prompt.value" :placeholder="prompt.placeholder" @press-enter="prompt.onOk" />
      <div v-if="prompt.hint" class="fm-dim fm-hint">{{ prompt.hint }}</div>
    </a-modal>

    <!-- 确认弹窗（删除等） -->
    <a-modal v-model:open="confirm.open" :title="confirm.title" ok-text="确认" ok-type="danger" @ok="confirm.onOk" :width="420">
      <div class="fm-hint">{{ confirm.body }}</div>
    </a-modal>

    <!-- 上传进度弹窗 -->
    <a-modal v-model:open="uploadProgress.open" title="上传中…" :closable="false" :maskClosable="false"
      :footer="null" :width="420">
      <div class="fm-upload-progress">
        <a-progress :percent="uploadProgress.percent" :status="uploadProgress.failed ? 'exception' : 'active'" size="small" />
        <div class="fm-upload-detail">
          <span>{{ uploadProgress.current }} / {{ uploadProgress.total }} 个文件</span>
          <span v-if="uploadProgress.currentName" class="fm-dim fm-upload-name">{{ uploadProgress.currentName }}</span>
        </div>
        <div v-if="uploadProgress.failed" class="fm-upload-fail">
          {{ uploadProgress.failCount }} 个文件上传失败
        </div>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, upload } from '../api'
import {
  ArrowUpOutlined, ReloadOutlined, EyeOutlined, EyeInvisibleOutlined, FileAddOutlined,
  FolderAddOutlined, UploadOutlined, FolderOutlined, FileOutlined, MoreOutlined,
  ArrowLeftOutlined, FormOutlined, HomeOutlined,
} from '@ant-design/icons-vue'
import AceEditor from './AceEditor.vue'

interface FEntry { name: string; size: number; perm: string; mode: string; modTime: string; isDir: boolean }

const props = defineProps<{ hostId: number | null }>()

const path = ref('/')
const entries = ref<FEntry[]>([])
const loading = ref(false)
const showHidden = ref(false)
const pathEditing = ref(false)
const pathInput = ref('')
const pathInputRef = ref()
const editing = ref<{ path: string; content: string; original: string; binary: boolean; truncated: boolean } | null>(null)

const prompt = ref<{ open: boolean; title: string; placeholder: string; hint: string; value: string; onOk: () => void }>(
  { open: false, title: '', placeholder: '', hint: '', value: '', onOk: () => {} })
const confirm = ref<{ open: boolean; title: string; body: string; onOk: () => void }>(
  { open: false, title: '', body: '', onOk: () => {} })

const api = (p: string) => `/api/sftp/hosts/${props.hostId}${p}`

// ---- 路径工具（POSIX，浏览器无 path 模块，自写最小实现） ----
const parentPath = (p: string) => {
  const parts = p.split('/').filter(Boolean)
  parts.pop()
  return '/' + parts.join('/')
}
const joinPath = (dir: string, name: string) => (dir === '/' ? '' : dir) + '/' + name
const pathSegs = computed(() => {
  const segs: { name: string; path: string }[] = []
  let acc = ''
  for (const s of path.value.split('/').filter(Boolean)) {
    acc += '/' + s
    segs.push({ name: s, path: acc })
  }
  return segs
})

const startPathEdit = async () => { pathInput.value = path.value; pathEditing.value = true; await nextTick(); pathInputRef.value?.focus() }

// ---- 列表 ----
// 目录恒排前，其次按所点列排序
const cmpDir = (a: FEntry, b: FEntry) => (b.isDir ? 1 : 0) - (a.isDir ? 1 : 0)
const cols = [
  { title: '名称', key: 'name', sorter: (a: FEntry, b: FEntry) => cmpDir(a, b) || a.name.localeCompare(b.name) },
  { title: '大小', key: 'size', width: 88, align: 'right' as const, sorter: (a: FEntry, b: FEntry) => cmpDir(a, b) || a.size - b.size },
  { title: '修改时间', key: 'modTime', width: 122, align: 'right' as const, sorter: (a: FEntry, b: FEntry) => cmpDir(a, b) || a.modTime.localeCompare(b.modTime) },
  { key: 'act', width: 48 },
]

const visibleEntries = computed(() =>
  showHidden.value ? entries.value : entries.value.filter(e => !e.name.startsWith('.')))

const load = async () => {
  if (!props.hostId) return
  loading.value = true
  try {
    const r = await get<{ entries: FEntry[] }>(api('/list'), { path: path.value })
    entries.value = r.entries || []
  } catch { /* 错误提示由 axios 拦截器统一弹出 */ }
  loading.value = false
}

const nav = (p: string) => {
  path.value = p || '/'
  pathEditing.value = false
  load()
}
watch(() => props.hostId, () => { editing.value = null; nav('/') }, { immediate: true })

// rowOps: 双击进入目录 / 打开文件
const rowOps = (rec: FEntry) => ({
  onDblclick: () => { rec.isDir ? nav(joinPath(path.value, rec.name)) : openEditor(rec) },
})

const openEditor = async (rec: FEntry) => {
  try {
    const r = await get<{ read: { binary: boolean; content: string; size: number; truncated: boolean } }>(
      api('/read'), { path: joinPath(path.value, rec.name) })
    const rd = r.read
    editing.value = {
      path: joinPath(path.value, rec.name),
      content: rd.binary ? '' : rd.content,
      original: rd.binary ? '' : rd.content,
      binary: rd.binary, truncated: rd.truncated,
    }
    if (rd.binary) message.warning('二进制文件不支持编辑，可下载后本地处理')
  } catch { /* toast 由拦截器处理 */ }
}
const dirty = computed(() => !!editing.value && editing.value.content !== editing.value.original)
const editMode = computed(() => {
  const n = editing.value?.path.toLowerCase() || ''
  if (n.endsWith('.conf')) return 'nginx'
  if (/\.(sh|bash|zsh)$/.test(n)) return 'sh'
  if (/\.(yml|yaml)$/.test(n)) return 'yaml'
  if (/\.(html|htm|vue)$/.test(n)) return 'html'
  if (n.endsWith('.json')) return 'json'
  if (/\.(py|pyw)$/.test(n)) return 'python'
  if (/\.(ini|env|cfg|toml|service|cnf)$/.test(n)) return 'ini'
  if (/\.(md|markdown)$/.test(n)) return 'markdown'
  return 'text'
})
const save = async () => {
  if (!editing.value) return
  try {
    await post(api('/write'), { path: editing.value.path, content: editing.value.content })
    editing.value.original = editing.value.content
    message.success('已保存到远程服务器')
  } catch { /* toast 由拦截器处理 */ }
}
const closeEditor = () => { editing.value = null }

// ---- 行操作 ----
const rowAction = (key: string, rec: FEntry) => {
  const full = joinPath(path.value, rec.name)
  switch (key) {
    case 'download': {
      const a = document.createElement('a')
      a.href = api('/download') + `?path=` + encodeURIComponent(full)
      a.download = rec.name
      a.click()
      break
    }
    case 'edit': openEditor(rec); break
    case 'rename':
      prompt.value = {
        open: true, title: '重命名', placeholder: rec.name, hint: full, value: rec.name,
        onOk: async () => {
          const np = joinPath(path.value, prompt.value.value)
          if (np === full) return
          await post(api('/rename'), { from: full, to: np })
          prompt.value.open = false
          load()
        },
      }
      break
    case 'chmod':
      prompt.value = {
        open: true, title: '修改权限', placeholder: rec.perm, hint: full + '（八进制，如 0644）', value: rec.perm,
        onOk: async () => {
          await post(api('/chmod'), { path: full, mode: prompt.value.value })
          prompt.value.open = false
          load()
        },
      }
      break
    case 'zip':
    case 'targz': {
      const dest = key === 'zip' ? full + '.zip' : full + '.tar.gz'
      message.loading({ content: '压缩中…', key: 'cmprs' })
      post(api('/compress'), { paths: [full], format: key, dest })
        .then(() => { message.success({ content: '已生成 ' + path2Base(dest), key: 'cmprs' }); load() })
        .catch(() => message.error({ content: '压缩失败', key: 'cmprs' }))
      break
    }
    case 'decompress':
      message.loading({ content: '解压中…', key: 'cmprs' })
      post(api('/decompress'), { path: full, destDir: path.value })
        .then(() => { message.success({ content: '解压完成', key: 'cmprs' }); load() })
        .catch(() => message.error({ content: '解压失败', key: 'cmprs' }))
      break
    case 'delete':
      confirm.value = {
        open: true, title: '删除确认',
        body: `确认删除「${rec.name}」？${rec.isDir ? '目录及全部子内容将被递归删除，' : ''}此操作不可恢复。`,
        onOk: async () => {
          await post(api('/delete'), { paths: [full] })
          confirm.value.open = false
          load()
        },
      }
      break
  }
}
const path2Base = (p: string) => p.split('/').filter(Boolean).pop() || p
const isArchive = (n: string) => /\.(zip|tar\.gz|tgz)$/i.test(n)

// ---- 新建 ----
const promptNew = (kind: 'file' | 'dir') => {
  prompt.value = {
    open: true, title: kind === 'file' ? '新建文件' : '新建文件夹', placeholder: kind === 'file' ? '例如 app.conf' : '例如 logs',
    hint: '位置：' + path.value, value: '',
    onOk: async () => {
      const name = prompt.value.value.trim()
      if (!name) return
      const full = joinPath(path.value, name)
      if (kind === 'file') {
        await post(api('/touch'), { path: full })
        prompt.value.open = false
        openEditor({ name, isDir: false } as FEntry)
        load()
      } else {
        await post(api('/mkdir'), { path: full })
        prompt.value.open = false
        load()
      }
    },
  }
}

// ---- 上传 ----
const fileInputRef = ref<HTMLInputElement | null>(null)
const pickFiles = () => fileInputRef.value?.click()
const onPickFiles = async (ev: Event) => {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files || [])
  input.value = '' // 清空，保证下次能再次选中同一文件
  if (!files.length) return
  await uploadFiles(files.map(f => ({ file: f, relativePath: f.name })))
}

// ---- 上传进度 ----
const uploadProgress = ref({
  open: false, percent: 0, current: 0, total: 0,
  currentName: '', failed: false, failCount: 0,
})

/** 统一上传入口：接收 { file, relativePath } 数组，逐个上传并显示进度弹窗 */
const uploadFiles = async (items: { file: File; relativePath: string }[]) => {
  if (!items.length) return
  const prog = uploadProgress.value
  prog.open = true
  prog.total = items.length
  prog.current = 0
  prog.percent = 0
  prog.failed = false
  prog.failCount = 0
  prog.currentName = ''
  for (const item of items) {
    prog.current++
    prog.currentName = item.relativePath
    prog.percent = Math.round((prog.current - 1) / prog.total * 100)
    try {
      await upload(api('/upload') + `?path=` + encodeURIComponent(joinPath(path.value, item.relativePath)), item.file)
    } catch {
      prog.failCount++
      prog.failed = true
    }
  }
  prog.percent = 100
  prog.currentName = ''
  // 短暂停留后关闭弹窗
  setTimeout(() => { prog.open = false }, prog.failed ? 1500 : 500)
  if (!prog.failed) {
    message.success(`已上传 ${prog.total} 个文件`)
  } else {
    message.warning(`${prog.total - prog.failCount} 个文件上传成功，${prog.failCount} 个失败`)
  }
  load()
}

// ---- 拖拽上传 ----
const dragOver = ref(false)
let dragLeaveTimer = 0

const onDragOver = (ev: DragEvent) => {
  clearTimeout(dragLeaveTimer)
  // 只在有文件拖入时激活（忽略纯文本拖拽等）
  if (ev.dataTransfer?.types.includes('Files')) {
    dragOver.value = true
  }
}
const onDragLeave = () => {
  // 子元素切换会连续触发 leave/enter，用短延迟防闪烁
  clearTimeout(dragLeaveTimer)
  dragLeaveTimer = window.setTimeout(() => { dragOver.value = false }, 80)
}
const onDrop = async (ev: DragEvent) => {
  clearTimeout(dragLeaveTimer)
  dragOver.value = false
  if (!ev.dataTransfer) return

  // 优先用 webkitGetAsEntry（支持文件夹递归遍历）
  const items = Array.from(ev.dataTransfer.items || [])
  const hasEntry = items.length > 0 && typeof items[0].webkitGetAsEntry === 'function'

  if (hasEntry) {
    const entryList: FileSystemEntry[] = []
    for (const item of items) {
      const entry = item.webkitGetAsEntry()
      if (entry) entryList.push(entry)
    }
    const collected = await collectEntries(entryList, '')
    if (collected.length) await uploadFiles(collected)
  } else {
    // 回退：只能拿到扁平文件（不支持文件夹）
    const files = Array.from(ev.dataTransfer.files || [])
    if (files.length) await uploadFiles(files.map(f => ({ file: f, relativePath: f.name })))
  }
}

/** 递归遍历 FileSystemEntry 树，收集全部文件及其相对路径 */
const collectEntries = async (
  entries: FileSystemEntry[], prefix: string,
): Promise<{ file: File; relativePath: string }[]> => {
  const result: { file: File; relativePath: string }[] = []
  for (const entry of entries) {
    const relPath = prefix ? prefix + '/' + entry.name : entry.name
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) =>
        (entry as FileSystemFileEntry).file(resolve, reject))
      result.push({ file, relativePath: relPath })
    } else if (entry.isDirectory) {
      const dirReader = (entry as FileSystemDirectoryEntry).createReader()
      const children = await readAllEntries(dirReader)
      const sub = await collectEntries(children, relPath)
      result.push(...sub)
    }
  }
  return result
}

/** readEntries 每次最多返回 100 条，需循环读取直至空 */
const readAllEntries = (reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> => {
  return new Promise((resolve, reject) => {
    const all: FileSystemEntry[] = []
    const readBatch = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) { resolve(all); return }
        all.push(...batch)
        readBatch()
      }, reject)
    }
    readBatch()
  })
}

// ---- 格式化 ----
const fmtSize = (n: number) => {
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n
  let i = -1
  do { v /= 1024; i++ } while (v >= 1024 && i < units.length - 1)
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + units[i]
}
const fmtTime = (s: string) => {
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  const now = new Date()
  const pad = (x: number) => String(x).padStart(2, '0')
  const hm = `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return d.getFullYear() === now.getFullYear() ? hm : `${d.getFullYear()}-${hm}`
}
</script>

<style scoped>
.fm-panel { height: 100%; display: flex; flex-direction: column; overflow: hidden; font-size: 13px; }
.fm-list, .fm-editor { flex: 1; min-height: 0; display: flex; flex-direction: column; position: relative; }

/* ---------- 头部：地址栏 + 工具栏 ---------- */
.fm-head { flex: none; padding-bottom: 10px; border-bottom: 1px solid var(--border-card); }

/* 地址栏：像输入框的一行路径；「输入路径跳转」收进框内末端，不与路径争夺横向空间 */
.fm-path {
  display: flex; align-items: center; gap: 6px;
  height: 30px; margin-bottom: 8px; padding: 0 4px 0 10px;
  border: 1px solid var(--border-input); border-radius: var(--radius-inputs);
  background: var(--surface-card);
  transition: border-color var(--motion-fast), box-shadow var(--motion-fast);
}
.fm-path:focus-within { border-color: var(--color-action-blue); box-shadow: var(--focus-ring); }
.fm-crumb { flex: 1; min-width: 0; overflow-x: auto; white-space: nowrap; }
.fm-crumb :deep(.ant-breadcrumb) { font-size: 12px; }
.fm-crumb :deep(.ant-breadcrumb-separator) { color: var(--color-slate); opacity: .5; }
.fm-crumb :deep(.ant-breadcrumb-link a) { color: var(--color-slate); cursor: pointer; }
.fm-crumb :deep(.ant-breadcrumb-link a:hover) { color: var(--color-corporate-blue); }
/* 当前目录加重，其余为可点层级 */
.fm-crumb :deep(.ant-breadcrumb-link a.fm-crumb-cur) { color: var(--color-ink); font-weight: 600; }
.fm-crumb :deep(.ant-breadcrumb-link a.fm-crumb-cur:hover) { color: var(--color-corporate-blue); }
.fm-crumb :deep(.ant-breadcrumb-link a.fm-crumb-root) { display: inline-flex; align-items: center; gap: 5px; }
.fm-path-edit {
  flex: none; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 6px;
  display: inline-flex; align-items: center; justify-content: center;
  background: transparent; color: var(--color-slate); cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast);
}
.fm-path-edit:hover { background: rgba(128, 128, 128, .14); color: var(--color-ink); }
.fm-path-input { flex: 1; min-width: 0; }
.fm-path-input :deep(.ant-input) { height: 26px; padding: 0 4px; border: 0; background: transparent; box-shadow: none; }
.fm-path-input :deep(.ant-input:focus) { box-shadow: none; }

/* 工具栏：导航组与新建组之间一条竖线分组，动作权重一致 */
.fm-bar { display: flex; align-items: center; gap: 8px; }
.fm-tools { display: flex; align-items: center; gap: 2px; }
.fm-rule { flex: none; width: 1px; height: 16px; background: var(--border-input); }
.fm-grow { flex: 1; }
.fm-count { flex: none; color: var(--color-slate); font-size: 12px; font-variant-numeric: tabular-nums; }
.fm-file-input { display: none; }
/* 26px 方块图标按钮：图标是主视觉，去掉边框只留悬停底色，避免一排小药丸 */
.fm-tool {
  width: 26px; height: 26px; padding: 0; border: 0; border-radius: 6px;
  display: inline-flex; align-items: center; justify-content: center;
  background: transparent; color: var(--color-slate); font-size: 15px; cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast);
}
.fm-tool:hover:not(:disabled) { background: rgba(128, 128, 128, .14); color: var(--color-ink); }
.fm-tool:active:not(:disabled) { background: rgba(128, 128, 128, .22); }
.fm-tool:disabled { opacity: .35; cursor: not-allowed; }
.fm-tool.is-on { background: var(--bg-selected); color: var(--color-corporate-blue); }
.fm-tool.is-on:hover { background: var(--bg-selected); color: var(--color-corporate-blue); }
.fm-tool.is-busy :deep(.anticon) { animation: fm-spin 1s linear infinite; }
@keyframes fm-spin { to { transform: rotate(360deg); } }

.fm-table { flex: 1; min-height: 0; overflow: auto; }
/* 面板可拖窄到 320px，固定列会挤掉名称列：给表格一个下限宽度，
   窄屏时改为横向滚动，而不是把文件名压成三个字 */
.fm-table :deep(.ant-table-content > table) { min-width: 420px; }
/* 固定布局下长文件名不再挤压大小/时间列；整行 pointer，双击进入 */
.fm-table :deep(.ant-table-tbody > tr.ant-table-row) { cursor: pointer; }
.fm-table :deep(.ant-table-tbody > tr > td),
.fm-table :deep(.ant-table-thead > tr > th) { white-space: nowrap; }
.fm-table :deep(.ant-table-tbody > tr:hover > td) { background: rgba(128, 128, 128, .13); }
.fm-name { display: flex; align-items: center; gap: 6px; max-width: 100%; min-width: 0; }
.fm-name-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fm-ic-dir { flex: none; color: var(--color-warning, #faad14); }
.fm-ic-file { flex: none; color: var(--color-slate); }
.fm-editor-bar { gap: 8px; display: flex; align-items: center; }
.fm-editor-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.fm-warn { color: var(--color-warning, #fa8c16); font-size: 12px; }
.fm-editor-box { flex: 1; min-height: 0; border: 1px solid rgba(128,128,128,.25); border-radius: 6px; overflow: hidden; }
.fm-empty { flex: 1; display: flex; align-items: center; justify-content: center; }
.fm-dim { color: var(--color-slate); font-size: 12px; }
.fm-hint { color: var(--color-slate); font-size: 12px; margin-top: 8px; word-break: break-all; }

/* ---------- 拖拽上传遮罩 ---------- */
.fm-drop-overlay {
  position: absolute; inset: 0; z-index: 20;
  display: flex; align-items: center; justify-content: center;
  background: rgba(var(--color-action-blue-rgb, 24, 144, 255), .08);
  border: 2px dashed var(--color-action-blue, #1890ff);
  border-radius: 8px; pointer-events: none;
}
.fm-drop-hint {
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  color: var(--color-action-blue, #1890ff); font-size: 14px; font-weight: 500;
}
.fm-drop-fade-enter-active, .fm-drop-fade-leave-active { transition: opacity .15s ease; }
.fm-drop-fade-enter-from, .fm-drop-fade-leave-to { opacity: 0; }

/* ---------- 上传进度弹窗 ---------- */
.fm-upload-progress { padding: 4px 0; }
.fm-upload-detail { display: flex; align-items: center; gap: 10px; margin-top: 6px; font-size: 13px; }
.fm-upload-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fm-upload-fail { margin-top: 6px; color: var(--color-error, #ff4d4f); font-size: 12px; }
</style>
