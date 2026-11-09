<template>
  <a-card size="small" :body-style="{ padding: '12px' }">
    <a-space style="margin-bottom: 12px" wrap>
      <a-select v-model:value="root" style="width: 150px" :options="rootOpts" @change="loadTree" />
      <a-button @click="testNginx">nginx -t</a-button>
      <a-button type="primary" ghost @click="reloadNginx">重载 nginx</a-button>
      <a-tag :color="running ? 'green' : 'red'">{{ running ? 'nginx 运行中' : 'nginx 未运行' }}</a-tag>
      <span v-if="currentPath" class="dim">{{ root }}/{{ currentPath }}</span>
      <a-button type="primary" :disabled="!currentPath" @click="saveFile">保存</a-button>
    </a-space>

    <a-row :gutter="12">
      <a-col :span="7" style="max-height: 620px; overflow: auto">
        <a-tree :tree-data="tree" :load-data="onLoadData" @select="onSelect" block-node />
      </a-col>
      <a-col :span="17">
        <div style="height: 620px; border: 1px solid #eee">
          <AceEditor v-model="content" :mode="mode" />
        </div>
      </a-col>
    </a-row>

    <a-modal v-model:open="outOpen" title="输出" :footer="null">
      <pre style="max-height: 300px; overflow: auto">{{ outContent }}</pre>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, put, post } from '../api'
import AceEditor from '../components/AceEditor.vue'

const root = ref('nginx')
const rootOpts = [
  { label: 'nginx 配置目录', value: 'nginx' },
  { label: '站点默认页 (html)', value: 'html' },
  { label: '日志目录（只读）', value: 'logs' },
]
const tree = ref<any[]>([])
const content = ref('')
const currentPath = ref('')
const currentDir = ref(false)
const running = ref(false)
const outOpen = ref(false)
const outContent = ref('')

const editorMode = () => {
  const p = currentPath.value || ''
  if (p.endsWith('.conf')) return 'nginx'
  if (p.endsWith('.html')) return 'html'
  if (p.endsWith('.yml') || p.endsWith('.yaml')) return 'yaml'
  if (p.endsWith('.sh')) return 'sh'
  return 'text'
}
const mode = ref('nginx')

const loadTree = async (path = '') => {
  const nodes = await get('/api/nginx/files', { root: root.value, path })
  return (nodes || []).map((n: any) => ({
    title: n.name,
    key: `${root.value}:${n.path}`,
    isLeaf: !n.dir,
    path: n.path,
  }))
}

const onLoadData = async (node: any) => {
  const kids = await loadTree(node.path)
  node.children = kids
}

const onSelect = async (_keys: any[], e: any) => {
  const node = e.node
  if (node.isLeaf) {
    currentPath.value = node.path
    currentDir.value = false
    const d = await get('/api/nginx/file', { root: root.value, path: node.path })
    content.value = d.content || ''
    mode.value = editorMode()
  }
}

const saveFile = async () => {
  const res = await put('/api/nginx/file', { root: root.value, path: currentPath.value, content: content.value })
  if (res.error) { message.error(res.error); outContent.value = res.output || ''; outOpen.value = true; return }
  message.success(res.output || '已保存')
}

const testNginx = async () => {
  const r = await post('/api/nginx/test')
  outContent.value = r.output || ''
  outOpen.value = true
  if (!r.ok) message.error('配置测试未通过')
  else message.success('配置测试通过')
}
const reloadNginx = async () => {
  const r = await post('/api/nginx/reload')
  if (!r.ok) { message.error(r.error || 'reload 失败'); outContent.value = r.output || ''; outOpen.value = true }
  else message.success(r.output || '已重载')
}

onMounted(async () => {
  tree.value = await loadTree()
  try { running.value = (await get<any>('/api/nginx/status')).running } catch { /* ignore */ }
})
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
</style>
