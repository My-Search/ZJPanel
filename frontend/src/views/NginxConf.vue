<template>
  <a-card size="small" :body-style="{ padding: '12px' }">
    <a-space style="margin-bottom: 12px" wrap>
      <a-select v-model:value="root" style="width: 150px" :options="rootOpts" @change="onRootChange" />
      <a-button @click="testNginx">nginx -t</a-button>
      <a-button type="primary" ghost @click="reloadNginx">重载 nginx</a-button>
      <a-tag :color="running ? 'green' : 'red'">{{ running ? 'nginx 运行中' : 'nginx 未运行' }}</a-tag>
      <span v-if="currentPath" class="dim">{{ root }}/{{ currentPath }}</span>
      <a-button type="primary" :disabled="!currentPath" @click="saveFile">保存</a-button>
    </a-space>

    <a-row :gutter="12">
      <a-col :span="7" class="tree-col">
        <a-tree :tree-data="tree" :load-data="onLoadData" @select="onSelect" block-node />
      </a-col>
      <a-col :span="17" class="editor-col">
        <div class="editor-wrap">
          <AceEditor v-if="currentPath" v-model="content" :mode="mode" />
          <a-empty v-else description="从左侧目录选择一个文件查看 / 编辑" class="editor-empty" />
        </div>
      </a-col>
    </a-row>

    <a-modal v-model:open="outOpen" title="输出" :footer="null">
      <pre style="max-height: 300px; overflow: auto">{{ outContent }}</pre>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onMounted, ref } from 'vue'
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
const running = ref(false)
const outOpen = ref(false)
const outContent = ref('')

const editorMode = (p: string) => {
  if (p.endsWith('.conf')) return 'nginx'
  if (p.endsWith('.html')) return 'html'
  if (p.endsWith('.yml') || p.endsWith('.yaml')) return 'yaml'
  if (p.endsWith('.sh')) return 'sh'
  return 'text'
}
const mode = ref('nginx')

const toNodes = (nodes: any[]) => (nodes || []).map((n: any) => ({
  title: n.name,
  key: `${root.value}:${n.path}`,
  isLeaf: !n.dir,
  path: n.path,
}))

const loadTree = async (path = '') => toNodes(await get('/api/nginx/files', { root: root.value, path }))

// 切换根目录：旧目录的文件路径不再有效，清空编辑态后重建树
const onRootChange = async () => {
  currentPath.value = ''
  content.value = ''
  tree.value = await loadTree()
}

// 将已加载的子节点按 key 合并进新树，避免刷新时收起用户已展开的目录
const mergeTree = (fresh: any[], old: any[]): any[] => {
  const loaded = new Map<string, any[]>()
  const collect = (nodes: any[]) => {
    for (const n of nodes) {
      if (n.children) { loaded.set(n.key, n.children); collect(n.children) }
    }
  }
  collect(old)
  const apply = (nodes: any[]): any[] => nodes.map(n => {
    const kids = loaded.get(n.key)
    return kids ? { ...n, children: apply(kids) } : n
  })
  return apply(fresh)
}

// 页面激活刷新目录树与运行状态，保留当前打开的文件、未保存内容与展开的目录
const loadPage = async () => {
  tree.value = mergeTree(await loadTree(), tree.value)
  try { running.value = (await get<any>('/api/nginx/status')).running } catch { /* ignore */ }
}

/**
 * antdv 4 的 loadData 收到的是节点快照（eventData），直接改 node.children 不会更新树；
 * 必须写回 node.dataRef（指向 treeData 中的原始响应式对象）并整体替换数组触发重渲染。
 */
const onLoadData = async (node: any) => {
  if (node.dataRef?.children) return
  const kids = await loadTree(node.path)
  if (node.dataRef) node.dataRef.children = kids
  tree.value = [...tree.value]
}

const onSelect = async (_keys: any[], e: any) => {
  const node = e.node
  if (!node?.isLeaf) return
  const path = node.dataRef?.path || node.path
  currentPath.value = path
  const d = await get('/api/nginx/file', { root: root.value, path })
  content.value = d.content || ''
  mode.value = editorMode(currentPath.value)
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

onMounted(loadPage)
onActivated(loadPage)
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
.tree-col { max-height: 620px; overflow: auto; }
.editor-wrap { height: 620px; border: 1px solid #eee; }
.editor-empty { height: 100%; display: flex; flex-direction: column; justify-content: center; }
@media (max-width: 767px) {
  .tree-col { max-height: 300px; }
  .editor-wrap { height: 420px; }
}
</style>
