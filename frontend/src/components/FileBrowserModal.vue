<template>
  <a-modal :open="open" :title="title || (mode === 'dir' ? '选择目录' : '选择文件')" :width="640" :footer="null" @cancel="close">
    <a-breadcrumb style="margin-bottom: 10px">
      <a-breadcrumb-item>
        <a @click="browse('/')">根目录</a>
      </a-breadcrumb-item>
      <a-breadcrumb-item v-for="(seg, i) in segments" :key="i">
        <a @click="browse(seg.path)">{{ seg.name }}</a>
      </a-breadcrumb-item>
    </a-breadcrumb>
    <a-alert v-if="error" type="warning" show-icon :message="error" style="margin-bottom: 10px" />
    <a-spin :spinning="loading">
      <div class="fb-list">
        <div v-if="!entries.length && !loading && !error" class="dim" style="padding: 16px; text-align: center">空目录</div>
        <div v-if="currentPath !== '/'" class="fb-item" @click="browse(parentPath)">
          <ArrowUpOutlined />
          <span style="margin-left: 8px">..（上级目录）</span>
        </div>
        <div
          v-for="e in entries"
          :key="e.name"
          class="fb-item"
          :class="{ selected: !e.isDir && selected === e.name }"
          @click="onEntryClick(e)"
        >
          <FolderOutlined v-if="e.isDir" style="color: var(--color-corporate-blue)" />
          <FileOutlined v-else />
          <span style="margin-left: 8px">{{ e.name }}</span>
          <span v-if="!e.isDir" class="dim fb-meta">{{ fmtBytes(e.size) }} · {{ e.modTime }}</span>
        </div>
      </div>
    </a-spin>
    <div style="margin-top: 12px; display: flex; gap: 8px">
      <a-input v-model:value="manualPath" placeholder="或直接输入路径（面板所在主机/容器内路径）" size="small" @press-enter="browse(manualPath)" />
      <a-button size="small" @click="browse(manualPath)">前往</a-button>
    </div>
    <div style="margin-top: 14px; display: flex; justify-content: space-between; align-items: center; gap: 8px">
      <span class="dim fb-current">当前：{{ currentPath }}</span>
      <a-space>
        <a-button @click="close">取消</a-button>
        <a-button type="primary" :disabled="mode === 'file' && !selected" @click="confirmPick">
          {{ mode === 'dir' ? '选择此目录' : '确定' }}
        </a-button>
      </a-space>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
// FileBrowserModal 面板本机文件/目录选择弹窗（GET /api/fs/browse，只读浏览，登录保护）。
// mode=file：点选文件后「确定」回传完整文件路径；mode=dir：直接回传当前目录。
// 预留 hostId prop：将来可接 SFTP 浏览 SSH 主机路径（当前仅面板本机）。
import { ref, computed, watch } from 'vue'
import { getQuiet, fmtBytes } from '../api'
import { FolderOutlined, FileOutlined, ArrowUpOutlined } from '@ant-design/icons-vue'

const props = withDefaults(defineProps<{ open: boolean; mode?: 'file' | 'dir'; title?: string; hostId?: number | null }>(), {
  mode: 'file',
  hostId: null,
})
const emit = defineEmits<{ (e: 'update:open', v: boolean): void; (e: 'picked', path: string): void }>()

const currentPath = ref('/')
const entries = ref<any[]>([])
const loading = ref(false)
const error = ref('')
const selected = ref('')
const manualPath = ref('')

// 分段兼容 Windows 反斜杠路径：按 / 与 \ 统一切分，盘符（如 C:）单独成根段
const segments = computed(() => {
  const p = (currentPath.value || "").replace(/\\/g, "/")
  if (!p || p === "/") return []
  const parts = p.split("/").filter(Boolean)
  const segs: { name: string; path: string }[] = []
  let acc = ""
  for (const name of parts) {
    if (!acc) {
      acc = /^[A-Za-z]:$/.test(name) ? name : "/" + name
    } else {
      acc = acc.replace(/\/$/, "") + "/" + name
    }
    segs.push({ name, path: acc })
  }
  return segs
})
const parentPath = computed(() => {
  const segs = segments.value
  if (segs.length > 1) return segs[segs.length - 2].path
  return "/"
})

const joinPath = (dir: string, name: string) => {
  if (dir.endsWith("/") || dir.endsWith("\\")) return dir + name
  return (dir.includes("\\") ? dir + "\\" : dir + "/") + name
}

const browse = async (p?: string) => {
  const target = (p || '').trim() || '/'
  loading.value = true
  error.value = ''
  try {
    const r = await getQuiet<any>('/api/fs/browse', { path: target })
    currentPath.value = r.path || target
    entries.value = Array.isArray(r.entries) ? r.entries : []
    selected.value = ''
    manualPath.value = currentPath.value
  } catch (e: any) {
    error.value = String(e?.response?.data?.error || e?.message || '目录读取失败')
  } finally {
    loading.value = false
  }
}

const onEntryClick = (e: any) => {
  if (e.isDir) {
    browse(joinPath(currentPath.value, e.name))
  } else {
    selected.value = e.name
  }
}

const confirmPick = () => {
  if (props.mode === 'dir') {
    emit('picked', currentPath.value)
  } else {
    if (!selected.value) return
    emit('picked', joinPath(currentPath.value, selected.value))
  }
  close()
}

const close = () => emit('update:open', false)

// 每次打开从根目录重新开始，避免上次的深层路径让用户迷失
watch(() => props.open, (v) => {
  if (v) {
    error.value = ''
    selected.value = ''
    manualPath.value = ''
    browse('/')
  }
})
</script>

<style scoped>
.fb-list {
  max-height: 320px;
  overflow: auto;
  border: 1px solid var(--border-card);
  border-radius: 6px;
}
.fb-item {
  display: flex;
  align-items: center;
  padding: 7px 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--border-card);
}
.fb-item:last-child {
  border-bottom: none;
}
.fb-item:hover {
  background: var(--surface-canvas);
}
.fb-item.selected {
  background: var(--bg-selected);
}
.fb-meta {
  margin-left: auto;
  font-size: 12px;
  padding-left: 12px;
  color: var(--color-slate);
}
.fb-current {
  font-size: 12px;
  word-break: break-all;
  color: var(--color-slate);
}
</style>
