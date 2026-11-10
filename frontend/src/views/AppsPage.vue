<template>
  <a-card size="small">
    <a-tabs v-model:activeKey="tab">
      <!-- ============ Compose 应用（主视图） ============ -->
      <a-tab-pane key="compose" tab="Compose 应用">
        <div class="toolbar">
          <a-button type="primary" @click="openImport"><PlusOutlined /> 导入 docker-compose 应用</a-button>
          <a-button @click="scan"><SyncOutlined /> 重新扫描</a-button>
          <span class="dim">
            宿主机目录经挂载映射进容器（默认 <code>/opt:/opt</code>），在向导中选择含 docker-compose.yml 的目录导入；
            每个项目一个/多个网站，站点声明写入目录下 <code>zjpanel.json</code>（导入时优先读取，面板变化自动写回，可迁移恢复）
          </span>
        </div>

        <a-row :gutter="12">
          <!-- 项目列表 -->
          <a-col :span="7">
            <a-empty v-if="!projects.length" description="未发现 compose 项目" />
            <div v-for="p in projects" :key="p.dir || p.id" class="proj-card" :class="{ active: selected?.dir === p.dir }" @click="select(p)">
              <div class="proj-name">{{ p.name }} <a-tag v-if="p.hasDecl" color="purple">声明</a-tag></div>
              <div class="proj-sub">{{ runningCount(p) }}/{{ p.services?.length || 0 }} 服务运行 · {{ siteCount(p) }} 网站 {{ p.imported ? '· 已导入' : '' }}</div>
              <div class="proj-res" v-if="projCpu(p) !== null">CPU {{ projCpu(p) }}% · 内存 {{ projMem(p) }}</div>
              <div class="proj-res dim-res" v-else-if="runningCount(p) === 0">未运行 · 点「up」启动</div>
              <div class="proj-dir">{{ p.dir }}</div>
            </div>
          </a-col>

          <!-- 项目详情 -->
          <a-col :span="17">
            <template v-if="selected">
              <a-card size="small" :title="'项目：' + selected.name" style="margin-bottom: 12px">
                <template #extra>
                  <a-space>
                    <a-button size="small" type="primary" ghost @click="projAction('up')">up</a-button>
                    <a-button size="small" @click="projAction('down')">down</a-button>
                    <a-button size="small" @click="projAction('restart')">restart</a-button>
                    <a-dropdown>
                      <a-button size="small">更多 <DownOutlined /></a-button>
                      <template #overlay>
                        <a-menu>
                          <a-menu-item key="refresh" @click="refreshProj">刷新服务解析</a-menu-item>
                          <a-menu-item key="decl" @click="showDecl">查看 zjpanel.json</a-menu-item>
                          <a-menu-item key="sync" @click="syncDecl">按声明同步站点</a-menu-item>
                          <a-menu-item key="write" @click="writeDecl">立即写出声明文件</a-menu-item>
                        </a-menu>
                      </template>
                    </a-dropdown>
                  </a-space>
                </template>
                <a-space wrap>
                  <a-tag v-for="sv in selected.services" :key="sv.name" color="blue">
                    {{ sv.name }}{{ sv.ports?.length ? ' · ' + sv.ports.join(', ') : '' }}
                  </a-tag>
                </a-space>
              </a-card>

              <!-- 关联网站 -->
              <a-card size="small" title="网站" style="margin-bottom: 12px">
                <template #extra>
                  <a-button size="small" type="primary" @click="openAddSite"><PlusOutlined /> 添加网站</a-button>
                </template>
                <a-alert v-if="orphanList.length" type="warning" show-icon style="margin-bottom: 8px"
                  :message="`面板存在但声明文件未包含：${orphanList.join('、')}（点击「按声明同步」或「立即写出声明文件」可修复）`" />
                <a-table :data-source="projSites" :columns="siteCols" size="small" row-key="id" :pagination="false">
                  <template #emptyText>
                    <a-empty :description="runningCount(selected) === 0 ? '项目未运行，暂无关联网站数据（点上方「up」启动）' : '该项目还没有关联网站'" style="margin: 24px 0" />
                  </template>
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'ssl'">
                      <a-tag v-if="record.ssl" color="green">HTTPS</a-tag><a-tag v-else>HTTP</a-tag>
                    </template>
                    <template v-else-if="column.key === 'maint'">
                      <a-tag v-if="record.maintenance" color="orange">维护中</a-tag>
                      <a-tag v-else color="blue">运行</a-tag>
                    </template>
                    <template v-else-if="column.key === 'action'">
                      <a-space>
                        <a-button v-if="!record.maintenance" size="small" @click="setState(record, 'maintenance')">维护</a-button>
                        <a-button v-else size="small" type="primary" ghost @click="setState(record, 'enable')">恢复</a-button>
                        <a-popconfirm title="删除该网站？（zjpanel.json 将同步更新）" @confirm="delSite(record)">
                          <a-button size="small" danger>删除</a-button>
                        </a-popconfirm>
                      </a-space>
                    </template>
                  </template>
                </a-table>
              </a-card>

              <!-- 项目容器 -->
              <a-card size="small" title="容器">
                <a-table :data-source="projContainers" :columns="containerCols" size="small" row-key="id" :pagination="false">
                  <template #emptyText>
                    <a-empty :description="runningCount(selected) === 0 ? '项目未启动（点上方「up」启动 compose 服务）' : '未找到带该项目标签的容器'" style="margin: 24px 0" />
                  </template>
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'name'">{{ (record.name || '').replace('/', '') }}</template>
                    <template v-else-if="column.key === 'state'">
                      <a-tag :color="record.state === 'running' ? 'green' : 'red'">{{ record.status }}</a-tag>
                    </template>
                    <template v-else-if="column.key === 'action'">
                      <a-space>
                        <a-button v-if="record.state !== 'running'" size="small" type="primary" ghost @click="cAction(record, 'start')">启动</a-button>
                        <a-button v-else size="small" @click="cAction(record, 'stop')">停止</a-button>
                        <a-button size="small" @click="cAction(record, 'restart')">重启</a-button>
                        <a-button size="small" @click="openLogs(record)">日志</a-button>
                        <a-button size="small" @click="openExec(record)">终端</a-button>
                      </a-space>
                    </template>
                  </template>
                </a-table>
              </a-card>
            </template>
            <a-empty v-else description="选择左侧项目查看详情" style="margin-top: 60px" />
          </a-col>
        </a-row>
      </a-tab-pane>

      <!-- 全部网站 -->
      <a-tab-pane key="sites" tab="全部网站">
        <Sites />
      </a-tab-pane>

      <!-- Docker 全局 -->
      <a-tab-pane key="docker" tab="Docker 全局">
        <DockerPage />
      </a-tab-pane>
    </a-tabs>

    <!-- 导入 docker-compose 应用（文件夹选择向导） -->
    <a-modal v-model:open="importOpen" title="导入 docker-compose 应用" :width="680" :footer="null">
      <!-- 第一步：选择目录 -->
      <template v-if="!importForm.parsed">
        <a-breadcrumb style="margin-bottom: 10px">
          <a-breadcrumb-item>
            <a @click="browseDir(browseRoot)">{{ browseRoot }}</a>
          </a-breadcrumb-item>
          <a-breadcrumb-item v-for="(seg, i) in browseSegments" :key="i">
            <a @click="browseDir(seg.path)">{{ seg.name }}</a>
          </a-breadcrumb-item>
        </a-breadcrumb>
        <a-alert v-if="browseError" type="warning" show-icon :message="browseError" style="margin-bottom: 10px" />
        <div class="dir-list">
          <div v-if="!browseDirs.length" class="dim" style="padding: 16px; text-align: center">该目录下没有子目录</div>
          <div v-for="d in browseDirs" :key="d.path" class="dir-item" @click="browseDir(d.path)">
            <FolderOutlined style="color: #4f7cff" />
            <span style="margin-left: 8px">{{ d.name }}</span>
            <a-tag v-if="d.hasCompose" color="green" style="margin-left: auto">docker-compose.yml</a-tag>
          </div>
        </div>
        <div style="margin-top: 12px; display: flex; gap: 8px">
          <a-input v-model:value="manualPath" placeholder="或直接输入目录路径（容器内路径，默认映射 /opt:/opt）" size="small" />
          <a-button size="small" @click="browseDir(manualPath)">前往</a-button>
        </div>
        <div style="margin-top: 14px; text-align: right">
          <a-button type="primary" :disabled="!browseCurrent.hasCompose" @click="pickDir(browseCurrent)">
            导入此目录{{ browseCurrent.name ? `（${browseCurrent.name}）` : '' }}
          </a-button>
        </div>
        <p class="dim" style="margin-top: 8px">
          提示：进入含 docker-compose.yml 的目录后点击「导入此目录」。目录需已挂载进容器——宿主机 /opt 下放置的应用在容器内同为 /opt。
        </p>
      </template>

      <!-- 第二步：选择服务与域名 -->
      <template v-else>
        <a-form layout="vertical">
          <a-form-item label="应用目录"><a-input :value="importForm.dir" disabled /></a-form-item>
          <a-form-item label="服务">
            <a-select v-model:value="importForm.service" :options="importServiceOpts" @change="importForm.port = firstImportPort" />
          </a-form-item>
          <a-form-item label="反代端口（服务发布到宿主机的端口）">
            <a-select v-model:value="importForm.port" :options="importPortOpts" />
          </a-form-item>
          <a-form-item label="站点域名"><a-input v-model:value="importForm.domain" placeholder="app.example.com" /></a-form-item>
          <a-form-item label="HTTPS"><a-switch v-model:checked="importForm.ssl" /></a-form-item>
        </a-form>
        <div style="text-align: right">
          <a-space>
            <a-button @click="importForm.parsed = false">上一步</a-button>
            <a-button type="primary" @click="doImport">导入</a-button>
          </a-space>
        </div>
      </template>
    </a-modal>

    <!-- 添加网站 -->
    <a-modal v-model:open="addOpen" :title="'添加网站 · ' + (selected?.name || '')" @ok="doAddSite">
      <a-form layout="vertical">
        <a-form-item label="服务">
          <a-select v-model:value="addForm.service" :options="serviceOpts" @change="addForm.port = firstPort" />
        </a-form-item>
        <a-form-item label="反代端口（服务发布到宿主机的端口）">
          <a-select v-model:value="addForm.port" :options="portOpts" />
        </a-form-item>
        <a-form-item label="域名"><a-input v-model:value="addForm.domain" placeholder="app.example.com" /></a-form-item>
        <a-form-item label="HTTPS"><a-switch v-model:checked="addForm.ssl" /></a-form-item>
      </a-form>
    </a-modal>

    <!-- zjpanel.json 查看 -->
    <a-modal v-model:open="declOpen" :title="'zjpanel.json · ' + (selected?.name || '')" :footer="null" :width="680">
      <pre class="decl">{{ declContent }}</pre>
    </a-modal>

    <!-- 操作输出 -->
    <a-modal v-model:open="outOpen" :title="outTitle" :footer="null" :width="720">
      <pre class="runlog">{{ outContent || '(无输出)' }}</pre>
    </a-modal>

    <!-- 容器日志 -->
    <a-modal v-model:open="logsOpen" :title="'日志 · ' + logsName" :footer="null" :width="860">
      <pre class="runlog" style="color: #ddd">{{ logsText }}</pre>
    </a-modal>

    <!-- 容器终端 -->
    <a-modal v-model:open="execOpen" :title="'终端 · ' + execName" :footer="null" :width="860" @cancel="closeExec" @after-close="closeExec">
      <div ref="execBox" style="height: 420px" :style="{ background: boxBg() }"></div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del } from '../api'
import { useTermTheme } from '../utils/termTheme'
import Sites from './Sites.vue'
import DockerPage from './DockerPage.vue'
import { PlusOutlined, SyncOutlined, DownOutlined, FolderOutlined } from '@ant-design/icons-vue'

const { ui, current: termTheme, boxBg } = useTermTheme()
const tab = ref('compose')
const composeRoot = ref('/compose')
const projects = ref<any[]>([])
const sites = ref<any[]>([])
const containers = ref<any[]>([])
const selected = ref<any>(null)

const addOpen = ref(false)
const addForm = ref<any>({})

// ---- 导入向导（文件夹选择组件） ----
const importOpen = ref(false)
const importForm = ref<any>({})
const browseRoot = ref('/opt')
const browsePath = ref('/opt')
const browseDirs = ref<any[]>([])
const browseError = ref('')
const browseCurrent = ref<any>({})
const manualPath = ref('')
const importServiceOpts = ref<any[]>([])
const importPortOpts = ref<any[]>([])
const firstImportPort = ref('')

const browseSegments = computed(() => {
  const segs: { name: string; path: string }[] = []
  const parts = browsePath.value.split('/').filter(Boolean)
  let acc = ''
  for (const p of parts) {
    acc += '/' + p
    segs.push({ name: p, path: acc })
  }
  return segs
})

const browseDir = async (path: string) => {
  browseError.value = ''
  try {
    const r = await get<any>('/api/compose/browse', { path })
    browsePath.value = r.path
    browseDirs.value = r.dirs || []
    // 当前目录自身含 compose 文件则可作为导入目标
    browseCurrent.value = r.hasCompose
      ? { name: r.path.split(/[\\/]/).pop(), path: r.path, hasCompose: true }
      : {}
    manualPath.value = r.path
  } catch (e: any) {
    browseError.value = e?.response?.data?.error || '目录读取失败'
  }
}

const pickDir = async (d: any) => {
  if (!d?.path) return
  const r = await get<any>('/api/compose/parse', { dir: d.path })
  importForm.value = {
    dir: d.path, parsed: true, name: r.name,
    service: r.services?.[0]?.name,
    port: r.services?.[0]?.ports?.[0]?.split(':')[0],
    domain: '', ssl: false,
  }
  importServiceOpts.value = (r.services || []).map((s: any) => ({ label: s.name, value: s.name }))
  const ports: string[] = []
  for (const s of r.services || []) for (const p of s.ports || []) ports.push(p)
  importPortOpts.value = [...new Set(ports)].map(p => ({ label: p, value: p.split(':')[0] }))
  firstImportPort.value = importPortOpts.value[0]?.value || ''
  if (!importForm.value.port) importForm.value.port = firstImportPort.value
}

const openImport = async () => {
  importOpen.value = true
  importForm.value = { parsed: false }
  await browseDir(browseRoot.value)
}

const doImport = async () => {
  await post('/api/compose/import', {
    dir: importForm.value.dir, service: importForm.value.service,
    port: importForm.value.port, domain: importForm.value.domain, ssl: importForm.value.ssl,
  })
  message.success('已导入并创建网站（zjpanel.json 已写出）')
  importOpen.value = false
  scan()
}
const declOpen = ref(false)
const declContent = ref('')
const outOpen = ref(false)
const outTitle = ref('')
const outContent = ref('')
const orphanList = ref<string[]>([])

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

const siteCols = [
  { title: '域名', dataIndex: 'domain', key: 'domain' },
  { title: '目标', dataIndex: 'upstream', key: 'upstream', width: 160 },
  { title: 'SSL', key: 'ssl', width: 90 },
  { title: '状态', key: 'maint', width: 90 },
  { title: '操作', key: 'action', width: 180 },
]
const containerCols = [
  { title: '名称', key: 'name', width: 200 },
  { title: '镜像', dataIndex: 'image', key: 'image' },
  { title: '状态', key: 'state', width: 150 },
  { title: '操作', key: 'action', width: 320 },
]

const serviceOpts = computed(() => (selected.value?.services || []).map((s: any) => ({ label: s.name, value: s.name })))
const portOpts = computed(() => {
  const ports: string[] = []
  for (const s of selected.value?.services || []) for (const p of s.ports || []) ports.push(p)
  return [...new Set(ports)].map(p => ({ label: p, value: p.split(':')[0] }))
})
const firstPort = computed(() => portOpts.value[0]?.value || '')

const projSites = computed(() => sites.value.filter(s => s.composeProjectId === selected.value?.id))
const siteCount = (p: any) => sites.value.filter(s => s.composeProjectId === p.id).length
const projContainers = computed(() => {
  const name = selected.value?.name
  if (!name) return []
  return containers.value.filter(c => c.labels?.['com.docker.compose.project'] === name)
})

// ---- 资源占用（compose 项目聚合，来自 /api/docker/containers/stats） ----
const stats = ref<any[]>([])
const projectStats = (p: any) => stats.value.filter(s => s.project === p.name)
const runningCount = (p: any) => {
  const name = p.name
  return containers.value.filter(c => c.labels?.['com.docker.compose.project'] === name && c.state === 'running').length
}
const projCpu = (p: any) => {
  const list = projectStats(p)
  if (!list.length) return null
  return Math.round(list.reduce((a, s) => a + (s.cpuPct || 0), 0) * 10) / 10
}
const projMem = (p: any) => {
  const total = projectStats(p).reduce((a, s) => a + (s.memBytes || 0), 0)
  if (!total) return '0MB'
  return total >= 1073741824 ? (total / 1073741824).toFixed(2) + 'GB' : Math.round(total / 1048576) + 'MB'
}
const loadStats = async () => { stats.value = await get('/api/docker/containers/stats').catch(() => []) as any }
/** 运行时数据（容器列表 + 资源占用）统一刷新，避免两处轮询各自为政。 */
const refreshRuntime = async () => {
  containers.value = await get('/api/docker/containers').catch(() => []) as any
  await loadStats()
}

const scan = async () => {
  projects.value = await get('/api/compose/scan')
  sites.value = await get('/api/sites')
  await refreshRuntime()
  try { composeRoot.value = (await get<any>('/api/settings')).composeRoot } catch { /* ignore */ }
  if (selected.value) {
    const again = projects.value.find(p => p.dir === selected.value.dir)
    selected.value = again || null
  }
  if (projects.value.length && !selected.value) select(projects.value[0])
}

const select = async (p: any) => {
  selected.value = p
  orphanList.value = []
  // 已导入项目：检查声明文件与面板的差异（孤儿站点）
  if (p.id && p.hasDecl) {
    try {
      const content = await get<any>(`/api/compose/${p.id}/decl`)
      const decl = JSON.parse(content.content)
      const declared = (decl.sites || []).map((s: any) => s.domain)
      orphanList.value = siteCount(p) ? projSites.value.filter(s => !declared.includes(s.domain)).map(s => s.domain) : []
    } catch { /* ignore */ }
  }
}

const projAction = async (action: string) => {
  if (!selected.value?.id) { message.warning('项目尚未导入（扫描后点击「按声明同步站点」或添加网站完成导入）'); return }
  const res = await post(`/api/compose/${selected.value.id}/action`, { action })
  outTitle.value = `${selected.value.name} · ${action}`
  outContent.value = (res.output || '') + (res.error ? '\n错误: ' + res.error : '')
  outOpen.value = true
  scan()
}
const refreshProj = async () => {
  const res = await post(`/api/compose/${selected.value.id}/refresh`, {})
  if (res.error) message.error(res.error); else message.success(res.message || '已刷新')
  scan()
}
const showDecl = async () => {
  const d = await get<any>(`/api/compose/${selected.value.id}/decl`)
  declContent.value = d.content
  declOpen.value = true
}
const syncDecl = async () => {
  const res = await post(`/api/compose/${selected.value.id}/sync-decl`, {})
  if (res.error) { message.error(res.error); return }
  message.success(res.message)
  if (res.orphans?.length) message.warning(`声明未包含的面板站点：${res.orphans.join('、')}`)
  scan()
}
const writeDecl = async () => {
  await post(`/api/compose/${selected.value.id}/write-decl`, {})
  message.success('zjpanel.json 已写出')
  scan()
}

const openAddSite = () => {
  addForm.value = { service: serviceOpts.value[0]?.value, port: firstPort.value, domain: '', ssl: false }
  addOpen.value = true
}
const doAddSite = async () => {
  await post('/api/compose/import', {
    dir: selected.value.dir, service: addForm.value.service,
    port: addForm.value.port, domain: addForm.value.domain, ssl: addForm.value.ssl,
  })
  message.success('已添加网站（nginx 已重载，zjpanel.json 已写出）')
  addOpen.value = false
  scan()
}
const setState = async (r: any, state: string) => {
  await post(`/api/sites/${r.id}/state`, { state })
  message.success('状态已切换')
  scan()
}
const delSite = async (r: any) => {
  await del(`/api/sites/${r.id}`)
  message.success('已删除')
  scan()
}

// ---- 项目容器操作 ----
const cAction = async (r: any, action: string) => {
  await post(`/api/docker/containers/${r.id}/action`, { action })
  message.success(`已${action}`)
  containers.value = await get('/api/docker/containers')
}
const openLogs = (r: any) => {
  logsName.value = r.name || r.id
  logsText.value = ''
  logsOpen.value = true
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  logsWs = new WebSocket(`${proto}://${location.host}/api/ws/docker/logs/${r.id}`)
  logsWs.onmessage = (ev) => { logsText.value += ev.data }
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
}
watch(() => ui.isDark, () => { if (execTerm) execTerm.options.theme = termTheme() })
const closeExec = () => {
  execWs?.close(); execWs = null
  execTerm?.dispose(); execTerm = null
}

let timer: any = null
onMounted(async () => {
  await scan()
  timer = setInterval(refreshRuntime, 10000)
})
onUnmounted(() => { clearInterval(timer); logsWs?.close(); closeExec() })
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 14px; }
.dim { color: #999; font-size: 12px; }
.proj-card { border: 1.5px solid #e5e7eb; border-radius: 8px; padding: 12px; margin-bottom: 10px; cursor: pointer; transition: all .2s; }
.proj-card.active { border-color: #4f7cff; background: #f0f5ff; }
.proj-name { font-weight: 600; font-size: 14px; }
.proj-res { margin-top: 4px; font-size: 12px; color: #52c41a; }
.dim-res { color: #999; }
.proj-sub { font-size: 12px; color: #888; margin: 4px 0; }
.proj-dir { font-size: 11px; color: #aaa; word-break: break-all; }
.decl, .runlog { max-height: 440px; overflow: auto; background: #111; color: #0f0; padding: 12px; font-size: 12px; white-space: pre-wrap; }
.dir-list { max-height: 300px; overflow: auto; border: 1px solid #eee; border-radius: 6px; }
.dir-item { display: flex; align-items: center; padding: 9px 14px; cursor: pointer; border-bottom: 1px solid #f5f5f5; }
.dir-item:hover { background: #f0f5ff; }
</style>
