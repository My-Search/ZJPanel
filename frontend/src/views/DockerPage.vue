<template>
  <a-card size="small">
    <a-alert v-if="!available" type="warning" show-icon message="Docker 不可用（检查 docker.sock 挂载 / DOCKER_HOST 环境变量）" style="margin-bottom: 12px" />
    <a-tabs v-model:activeKey="tab">
      <a-tab-pane key="containers" tab="容器">
        <a-table :data-source="containers" :columns="containerCols" size="small" row-key="id" :pagination="false" :scroll="{ y: 460 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">{{ (record.name || '').replace('/', '') }}</template>
            <template v-else-if="column.key === 'state'">
              <a-tag :color="record.state === 'running' ? 'green' : 'red'">{{ record.status }}</a-tag>
            </template>
            <template v-else-if="column.key === 'ports'">
              <span v-for="p in record.ports" :key="p.publicPort + '-' + p.privatePort" class="dim">
                {{ p.publicPort || '' }}→{{ p.privatePort }}({{ p.type }}) </span>
            </template>
            <template v-else-if="column.key === 'action'">
              <a-space>
                <a-button v-if="record.state !== 'running'" size="small" type="primary" ghost @click="act(record, 'start')">启动</a-button>
                <a-button v-else size="small" @click="act(record, 'stop')">停止</a-button>
                <a-button size="small" @click="act(record, 'restart')">重启</a-button>
                <a-button size="small" @click="openLogs(record)">日志</a-button>
                <a-button size="small" @click="openExec(record)">终端</a-button>
                <a-popconfirm title="强制删除该容器？" @confirm="act(record, 'remove')">
                  <a-button size="small" danger>删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </template>
        </a-table>
      </a-tab-pane>

      <a-tab-pane key="images" tab="镜像">
        <a-space style="margin-bottom: 12px">
          <a-input v-model:value="pullRef" placeholder="镜像名:tag，如 redis:7" style="width: 300px" />
          <a-button type="primary" @click="doPull">拉取镜像</a-button>
        </a-space>
        <a-table :data-source="images" :columns="imageCols" size="small" row-key="id" :pagination="false" :scroll="{ y: 440 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'tags'">
              <a-tag v-for="t in record.tags.slice(0, 3)" :key="t">{{ t }}</a-tag>
            </template>
            <template v-else-if="column.key === 'size'">{{ fmtBytes(record.size) }}</template>
            <template v-else-if="column.key === 'action'">
              <a-popconfirm title="强制删除该镜像？" @confirm="removeImage(record)">
                <a-button size="small" danger>删除</a-button>
              </a-popconfirm>
            </template>
          </template>
        </a-table>
      </a-tab-pane>

      <a-tab-pane key="networks" tab="网络">
        <a-table :data-source="networks" :columns="netCols" size="small" row-key="id" :pagination="false" />
      </a-tab-pane>

      <a-tab-pane key="volumes" tab="卷">
        <a-table :data-source="volumes" :columns="volCols" size="small" row-key="name" :pagination="false" />
      </a-tab-pane>
    </a-tabs>

    <!-- 日志 -->
    <a-modal v-model:open="logsOpen" :title="'日志 · ' + logsName" :footer="null" :width="860">
      <pre ref="logsBox" style="max-height: 480px; overflow: auto; background: #111; color: #ddd; padding: 12px; font-size: 12px; white-space: pre-wrap">{{ logsText }}</pre>
    </a-modal>

    <!-- exec 终端 -->
    <a-modal v-model:open="execOpen" :title="'终端 · ' + execName" :footer="null" :width="860" @after-close="closeExec" @cancel="closeExec">
      <div ref="execBox" style="height: 420px" :style="{ background: boxBg() }"></div>
    </a-modal>

    <!-- 拉取进度 -->
    <a-modal v-model:open="pullOpen" :title="'拉取 ' + pullRef" :footer="null" :width="720">
      <pre style="max-height: 400px; overflow: auto; background: #111; color: #0f0; padding: 12px; font-size: 12px">{{ pullText }}</pre>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del, fmtBytes } from '../api'
import { useTermTheme } from '../utils/termTheme'

const { ui, current: termTheme, boxBg } = useTermTheme()
const available = ref(true)
const tab = ref('containers')
const containers = ref<any[]>([])
const images = ref<any[]>([])
const networks = ref<any[]>([])
const volumes = ref<any[]>([])
const pullRef = ref('')
const pullOpen = ref(false)
const pullText = ref('')

const logsOpen = ref(false)
const logsName = ref('')
const logsText = ref('')
let logsWs: WebSocket | null = null

const execOpen = ref(false)
const execName = ref('')
const execBox = ref<HTMLElement | null>(null)
let execWs: WebSocket | null = null
let execTerm: any = null
let execFit: any = null

const containerCols = [
  { title: '名称', key: 'name', width: 180 },
  { title: '镜像', dataIndex: 'image', key: 'image' },
  { title: '状态', key: 'state', width: 150 },
  { title: '端口', key: 'ports' },
  { title: '操作', key: 'action', width: 330 },
]
const imageCols = [
  { title: '标签', key: 'tags' },
  { title: '大小', key: 'size', width: 100 },
  { title: '创建时间', dataIndex: 'created', key: 'created', width: 180, customRender: (o: any) => new Date(o.value).toLocaleString('zh-CN', { hour12: false }) },
  { title: '操作', key: 'action', width: 80 },
]
const netCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '驱动', dataIndex: 'driver', key: 'driver', width: 120 },
  { title: '范围', dataIndex: 'scope', key: 'scope', width: 100 },
  { title: 'ID', dataIndex: 'id', key: 'id' },
]
const volCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 260 },
  { title: '驱动', dataIndex: 'driver', key: 'driver', width: 120 },
  { title: '挂载点', dataIndex: 'mountpoint', key: 'mountpoint' },
]

const load = async () => {
  try {
    const info = await get<any>('/api/docker/info').catch(() => null)
    available.value = !!info
    containers.value = (await get('/api/docker/containers')) || []
    images.value = (await get('/api/docker/images')) || []
    networks.value = (await get('/api/docker/networks')) || []
    volumes.value = (await get('/api/docker/volumes')) || []
  } catch { available.value = false }
}

const act = async (r: any, action: string) => {
  await post(`/api/docker/containers/${r.id}/action`, { action })
  message.success(`已${action}`)
  load()
}

const openLogs = (r: any) => {
  logsName.value = r.name || r.id
  logsText.value = ''
  logsOpen.value = true
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  logsWs = new WebSocket(`${proto}://${location.host}/api/ws/docker/logs/${r.id}`)
  logsWs.onmessage = (ev) => { logsText.value += ev.data; nextTick(() => { if (logsText.value.length > 400000) logsText.value = logsText.value.slice(-300000) }) }
  logsWs.onclose = () => { logsWs = null }
}

const openExec = async (r: any) => {
  execName.value = r.name || r.id
  execOpen.value = true
  await nextTick()
  const xterm = await import('@xterm/xterm')
  const fitAddon = await import('@xterm/addon-fit')
  execTerm = new xterm.Terminal({ fontSize: 13, cursorBlink: true, theme: termTheme() })
  execFit = new fitAddon.FitAddon()
  execTerm.loadAddon(execFit)
  execTerm.open(execBox.value!)
  execFit.fit()
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  execWs = new WebSocket(`${proto}://${location.host}/api/ws/docker/exec/${r.id}`)
  execWs.onopen = () => execTerm.onData((d: string) => execWs?.send(new TextEncoder().encode(d)))
  execWs.onmessage = (ev) => { if (ev.data instanceof Blob) ev.data.text().then(t => execTerm?.write(t)); else execTerm?.write(ev.data) }
  execWs.onclose = () => { execTerm?.write('\r\n\x1b[31m[会话结束]\x1b[0m') }
  setTimeout(() => execFit?.fit(), 200)
}
watch(() => ui.isDark, () => { if (execTerm) execTerm.options.theme = termTheme() })
const closeExec = () => {
  execWs?.close(); execWs = null
  execTerm?.dispose(); execTerm = null
}

const doPull = async () => {
  if (!pullRef.value) return
  pullOpen.value = true
  pullText.value = ''
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${location.host}/api/ws/docker/pull`)
  ws.onopen = () => ws.send(JSON.stringify({ ref: pullRef.value }))
  ws.onmessage = (ev) => { pullText.value += ev.data + '\n' }
  ws.onclose = () => { load() }
}

const removeImage = async (r: any) => {
  await del(`/api/docker/images/${encodeURIComponent(r.tags?.[0] || r.id)}`)
  message.success('已删除')
  load()
}

let timer: any = null
onMounted(() => { load() })

// 保活生命周期（作为 AppsPage 子组件随父级缓存）：激活恢复轮询，切走暂停
onActivated(() => {
  load()
  timer = setInterval(load, 10000)
})
onDeactivated(() => clearInterval(timer))
onUnmounted(() => { clearInterval(timer); logsWs?.close(); closeExec() })
</script>

<style scoped>
.dim { color: #999; font-size: 12px; margin-right: 6px; }
</style>
