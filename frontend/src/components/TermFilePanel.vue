<template>
  <div class="fm-panel">
    <template v-if="hostId">
      <!-- ============ 列表视图 ============ -->
      <div v-if="!editing" class="fm-list">
        <div class="fm-bar">
          <a-space :size="4" wrap>
            <a-button size="small" :disabled="path === '/'" @click="nav(parentPath(path))" title="上一级">
              <ArrowUpOutlined />
            </a-button>
            <a-button size="small" :loading="loading" @click="load" title="刷新">
              <ReloadOutlined />
            </a-button>
            <a-button size="small" :type="showHidden ? 'primary' : 'default'" @click="showHidden = !showHidden" title="显示隐藏文件">
              <EyeOutlined v-if="!showHidden" /><EyeInvisibleOutlined v-else />
            </a-button>
            <span class="fm-sep"></span>
            <a-button size="small" @click="promptNew('file')" title="新建文件"><FileAddOutlined /></a-button>
            <a-button size="small" @click="promptNew('dir')" title="新建文件夹"><FolderAddOutlined /></a-button>
            <a-upload :show-upload-list="false" :custom-request="doUpload" multiple>
              <a-button size="small" title="上传到当前目录"><UploadOutlined /></a-button>
            </a-upload>
          </a-space>
        </div>

        <!-- 路径导航：面包屑 / 可编辑输入 -->
        <div class="fm-path">
          <template v-if="!pathEditing">
            <a-breadcrumb class="fm-crumb">
              <a-breadcrumb-item><a @click="nav('/')">/</a></a-breadcrumb-item>
              <a-breadcrumb-item v-for="seg in pathSegs" :key="seg.path">
                <a @click="nav(seg.path)">{{ seg.name }}</a>
              </a-breadcrumb-item>
            </a-breadcrumb>
            <a-button size="small" type="text" class="fm-path-edit" @click="startPathEdit" title="输入路径跳转">
              <FormOutlined />
            </a-button>
          </template>
          <a-input v-else ref="pathInputRef" v-model:value="pathInput" size="small" placeholder="/etc/nginx/nginx.conf"
            @press-enter="nav(pathInput)" @blur="pathEditing = false">
            <template #suffix><span class="fm-dim">回车跳转 · Esc 取消</span></template>
          </a-input>
        </div>

        <div class="fm-table">
          <a-table :data-source="visibleEntries" :columns="cols" size="small" row-key="name" :pagination="false"
            :loading="loading" :custom-row="rowOps" table-layout="fixed">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'name'">
                <span class="fm-name" :title="record.perm ? `${record.name} · ${record.perm}` : record.name">
                  <FolderOutlined v-if="record.isDir" class="fm-ic-dir" />
                  <FileOutlined v-else class="fm-ic-file" />
                  {{ record.name }}
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
                  <a-button size="small" type="text"><MoreOutlined /></a-button>
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
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, upload } from '../api'
import {
  ArrowUpOutlined, ReloadOutlined, EyeOutlined, EyeInvisibleOutlined, FileAddOutlined,
  FolderAddOutlined, UploadOutlined, FolderOutlined, FileOutlined, MoreOutlined,
  ArrowLeftOutlined, FormOutlined,
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
  { key: 'act', width: 34 },
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
const doUpload = async (opt: any) => {
  const file = opt.file as File
  const target = joinPath(path.value, file.name)
  try {
    await upload(api('/upload') + `?path=` + encodeURIComponent(target), file)
    message.success('已上传 ' + file.name)
    opt.onSuccess?.(file)
    load()
  } catch (e) {
    opt.onError?.(e)
    /* toast 由拦截器处理 */
  }
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
.fm-list, .fm-editor { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.fm-bar { flex: none; padding-bottom: 8px; }
.fm-sep { display: inline-block; width: 6px; }
.fm-path { flex: none; display: flex; align-items: center; gap: 4px; margin-bottom: 8px; }
.fm-crumb { flex: 1; min-width: 0; overflow-x: auto; white-space: nowrap; }
.fm-path-edit { flex: none; }
.fm-table { flex: 1; min-height: 0; overflow: auto; }
/* 固定布局下长文件名不再挤压大小/时间列；整行 pointer，双击进入 */
.fm-table :deep(.ant-table-tbody > tr.ant-table-row) { cursor: pointer; }
.fm-table :deep(.ant-table-tbody > tr > td),
.fm-table :deep(.ant-table-thead > tr > th) { white-space: nowrap; }
.fm-table :deep(.ant-table-tbody > tr:hover > td) { background: rgba(128, 128, 128, .13); }
.fm-name { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fm-ic-dir { color: #faad14; }
.fm-ic-file { color: #8c8c8c; }
.fm-editor-bar { gap: 8px; display: flex; align-items: center; }
.fm-editor-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.fm-grow { flex: 1; }
.fm-warn { color: #fa8c16; font-size: 12px; }
.fm-editor-box { flex: 1; min-height: 0; border: 1px solid rgba(128,128,128,.25); border-radius: 6px; overflow: hidden; }
.fm-empty { flex: 1; display: flex; align-items: center; justify-content: center; }
.fm-dim { color: var(--color-slate); font-size: 12px; }
.fm-hint { color: var(--color-slate); font-size: 12px; margin-top: 8px; word-break: break-all; }
</style>
