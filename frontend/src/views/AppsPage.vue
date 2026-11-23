<template>
  <a-card size="small">
    <a-spin :spinning="scanning" size="small">
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

        <a-row :gutter="12" class="apps-layout">
          <!-- 项目列表 -->
          <a-col :span="7" class="proj-col">
            <a-empty v-if="!projects.length" description="未发现 compose 项目" />
            <div v-for="p in sortedProjects" :key="p.dir || p.id" class="proj-card" :class="{ active: selected?.dir === p.dir }" @click="select(p)">
              <div class="proj-name">{{ p.name }} <a-tag v-if="p.hasDecl" color="purple">声明</a-tag></div>
              <div class="proj-sub">{{ runningCount(p) }}/{{ p.services?.length || 0 }} 服务运行 · {{ siteCount(p) }} 网站 {{ p.imported ? '· 已导入' : '' }}</div>
              <div class="proj-res" v-if="projCpu(p) !== null">CPU {{ projCpu(p) }}% · 内存 {{ projMem(p) }}</div>
              <div class="proj-res dim-res" v-else-if="runningCount(p) === 0">未运行 · 点「up」启动</div>
              <div class="proj-dir">{{ p.dir }}</div>
            </div>
          </a-col>

          <!-- 项目详情 -->
          <a-col :span="17" class="detail-col">
            <template v-if="selected">
              <a-card size="small" :title="'项目：' + selected.name" style="margin-bottom: 12px">
                <template #extra>
                  <a-space>
                    <a-button size="small" type="primary" ghost :disabled="projBusy" @click="projAction('up')">up</a-button>
                    <a-button size="small" :disabled="projBusy" @click="projAction('down')">down</a-button>
                    <a-button size="small" :disabled="projBusy" @click="projAction('restart')">restart</a-button>
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
                <a-table :data-source="projSites" :columns="siteCols" size="small" row-key="id" :pagination="false" :scroll="{ x: 820 }"
                  :expanded-row-keys="expandedKeys" :show-expand-column="false" :expand-icon="() => null" @expandedRowsChange="onExpandedRowsChange">
                  <template #emptyText>
                    <a-empty :description="runningCount(selected) === 0 ? '项目未运行，暂无关联网站数据（点上方「up」启动）' : '该项目还没有关联网站'" style="margin: 24px 0" />
                  </template>
                  <!-- 行内展开：该网站最近请求树（最近 24 小时，数据懒加载 + 30s 静默刷新） -->
                  <template #expandedRowRender="{ record }">
                    <div v-if="!reqSummary" style="padding: 4px 0"><a-spin size="small" /></div>
                    <div v-else-if="!siteReqTree(record.domain).length" class="dim" style="padding: 2px 0">最近 24 小时暂无请求</div>
                    <template v-else>
                      <div class="dim" style="margin-bottom: 6px">
                        最近 24 小时 · {{ siteReq(record.domain).count }} 次请求 · {{ siteReq(record.domain).pathCount }} 种路径 · {{ fmtBytes(siteReq(record.domain).bytes) }}
                      </div>
                      <a-tree :tree-data="siteReqTree(record.domain)" block-node :selectable="false" :height="260" />
                    </template>
                  </template>
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'domain'">
                      <!-- 域名可点击展开该网站最近请求树；行首自带展开图标已隐藏（expand-icon 置空），
                           仅域名前保留小箭头，域名保持文本色；打开网站走右侧独立外链图标，避免歧义 -->
                      <span class="domain-toggle"
                        :title="expandedKeys.includes(record.id) ? '点击收起最近请求' : '点击展开最近 24 小时请求'"
                        @click="toggleSiteReq(record)">
                        <CaretRightOutlined class="domain-caret" :class="{ open: expandedKeys.includes(record.id) }" />
                        {{ record.domain }}
                      </span>
                      <a-tooltip title="新标签打开网站">
                        <span class="site-open" @click.stop="openSite(record)">
                          <svg viewBox="0 0 1024 1024" xmlns="http://www.w3.org/2000/svg" aria-label="打开网站">
                            <path d="M398.912 106.688c15.36 0 27.776 12.416 27.776 27.712v29.888c0 15.296-12.416 27.712-27.776 27.712H170.688v661.312H832V625.088c0-15.36 12.416-27.776 27.712-27.776H889.6c15.36 0 27.712 12.416 27.712 27.776V896c0 22.464-17.408 40.96-39.488 42.56l-3.2 0.128H128A42.688 42.688 0 0 1 85.44 899.2l-0.128-3.2V149.312c0-22.464 17.408-40.96 39.488-42.56l3.2-0.064h270.912z m475.776 0c22.464 0 40.96 17.408 42.56 39.488l0.064 3.2v249.6c0 15.296-12.416 27.712-27.712 27.712h-29.888A27.712 27.712 0 0 1 832 398.912v-146.56L539.968 544.32a27.712 27.712 0 0 1-39.232 0l-21.12-21.12a27.712 27.712 0 0 1 0-39.232L771.584 192h-146.56a27.712 27.712 0 0 1-27.712-27.712V134.4c0-15.36 12.416-27.712 27.776-27.712h249.6z" />
                          </svg>
                        </span>
                      </a-tooltip>
                    </template>
                    <template v-else-if="column.key === 'ssl'">
                      <a-tag v-if="record.ssl" color="green">HTTPS</a-tag><a-tag v-else>HTTP</a-tag>
                    </template>
                    <template v-else-if="column.key === 'maint'">
                      <a-tag v-if="record.maintenance" color="orange">维护中</a-tag>
                      <a-tag v-else color="blue">运行</a-tag>
                    </template>
                    <template v-else-if="column.key === 'action'">
                      <!-- 与「全部网站」页同一套操作（维护/停用/编辑/配置/删除），由 SiteActions 组件提供 -->
                      <SiteActions :site="record" @changed="scan" />
                    </template>
                  </template>
                </a-table>
              </a-card>

              <!-- 项目容器 -->
              <a-card size="small" title="容器">
                <a-table :data-source="projContainers" :columns="containerCols" size="small" row-key="id" :pagination="false" :scroll="{ x: 880 }">
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
        <DockerPage :active="tab === 'docker'" />
      </a-tab-pane>
    </a-tabs>
    </a-spin>

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
            <FolderOutlined style="color: var(--color-corporate-blue)" />
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
    <a-modal v-model:open="outOpen" :footer="null" :width="720">
      <template #title>
        <a-space :size="8">
          <span>{{ outTitle }}</span>
          <a-tag v-if="outStatus === 'running'" color="processing" style="margin: 0"><LoadingOutlined spin /> 执行中 · {{ outElapsed }}s</a-tag>
          <a-tag v-else-if="outStatus === 'success'" color="success" style="margin: 0">完成 · {{ outElapsed }}s</a-tag>
          <a-tag v-else-if="outStatus === 'error'" color="error" style="margin: 0">失败 · {{ outElapsed }}s</a-tag>
        </a-space>
      </template>
      <pre ref="outBox" class="runlog" @scroll="onOutScroll">{{ outText || '（等待输出…）' }}</pre>
      <a-alert v-if="outStatus === 'error' && outError" type="error" show-icon style="margin-top: 10px" :message="outError" />
    </a-modal>

    <!-- 容器日志 -->
    <a-modal v-model:open="logsOpen" :title="'日志 · ' + logsName" :footer="null" :width="860">
      <pre class="runlog">{{ logsText }}</pre>
    </a-modal>

    <!-- 容器终端 -->
    <a-modal v-model:open="execOpen" :title="'终端 · ' + execName" :footer="null" :width="860" @cancel="closeExec" @after-close="closeExec">
      <div ref="execBox" style="height: 420px" :style="{ background: boxBg() }"></div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, getQuiet, post, fmtBytes, wsTopic } from '../api'
import { useTermTheme } from '../utils/termTheme'
import { wsMessageToText } from '../utils/wsMessage'
import Sites from './Sites.vue'
import SiteActions from '../components/SiteActions.vue'
import DockerPage from './DockerPage.vue'
import { PlusOutlined, SyncOutlined, DownOutlined, FolderOutlined, CaretRightOutlined, LoadingOutlined } from '@ant-design/icons-vue'

const { ui, current: termTheme, boxBg } = useTermTheme()
const tab = ref('compose')
const composeRoot = ref('/compose')
const projects = ref<any[]>([])
const sites = ref<any[]>([])
const containers = ref<any[]>([])
const selected = ref<any>(null)
const scanning = ref(true)

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
const orphanList = ref<string[]>([])

// ---- Compose 项目操作实时输出：点击 up/down/restart 立即开窗，WS 主题 compose-act-<id>
//      逐行推送（含拉镜像 \r 进度帧），快照接口引导/补齐（订阅前已产生的行、WS 断连丢帧），
//      3s 轮询兜底终帧丢失；结束后自动 scan() 刷新项目与容器状态 ----
const outOpen = ref(false)
const outTitle = ref('')
const outText = ref('')
const outStatus = ref<'idle' | 'running' | 'success' | 'error'>('idle')
const outError = ref('')
const outStartedAt = ref(0)
const outEndedAt = ref(0)
const outJobProjectId = ref<number | null>(null)
const outBox = ref<HTMLElement | null>(null)
let outNext = 0 // 已消费的全局行号（后端 \r/\n 切行的单调序号），WS 与快照据此去重
let outStopWs: (() => void) | null = null
let outElapsedTimer: any = null
let outPollTimer: any = null
let outAutoScroll = true // 用户上滑离开底部时暂停自动滚动

const nowTick = ref(0)
const outElapsed = computed(() => {
  void nowTick.value // running 时由秒级定时器驱动重算
  if (!outStartedAt.value) return 0
  const end = outStatus.value === 'running' ? Date.now() : (outEndedAt.value || Date.now())
  return Math.max(0, Math.round((end - outStartedAt.value) / 1000))
})
const projBusy = computed(() => outStatus.value === 'running' && outJobProjectId.value === selected.value?.id)

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
  { title: '操作', key: 'action', width: 300 },
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
// compose 项目 label 一律为目录小写，而面板项目名可能含大写（如 AgentsWorkStudio），统一按小写比对
const sameProject = (a?: string, b?: string) => !!a && !!b && a.toLowerCase() === b.toLowerCase()
const projContainers = computed(() => {
  const name = selected.value?.name
  if (!name) return []
  return containers.value.filter(c => sameProject(c.labels?.['com.docker.compose.project'], name))
})

// ---- 资源占用（compose 项目聚合，来自 /api/docker/containers/stats） ----
const stats = ref<any[]>([])
const projectStats = (p: any) => stats.value.filter(s => sameProject(s.project, p.name))
const runningCount = (p: any) => {
  const name = p.name
  return containers.value.filter(c => sameProject(c.labels?.['com.docker.compose.project'], name) && c.state === 'running').length
}
// 项目列表排序：运行中的排前面（组内保持原顺序），未运行的排后面（用户反馈）
const sortedProjects = computed(() => {
  const running: any[] = [], stopped: any[] = []
  for (const p of projects.value) (runningCount(p) > 0 ? running : stopped).push(p)
  return [...running, ...stopped]
})
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
let statsPromise: Promise<void> | null = null
const loadStats = () => {
  if (statsPromise) return statsPromise // 首屏补帧与 10s 轮询撞车时复用同一请求
  statsPromise = getQuiet('/api/docker/containers/stats')
    .then(v => { stats.value = (v as any) || [] })
    .catch(() => { /* 保留上一帧，等下一轮轮询 */ })
    .finally(() => { statsPromise = null })
  return statsPromise
}
/** 运行时数据（容器列表 + 资源占用）定时刷新：两路并发，单路失败保留上一帧。 */
let runtimePromise: Promise<void> | null = null
const refreshRuntime = () => {
  if (runtimePromise) return runtimePromise // 定时刷新与其它触发撞车时复用同一批请求
  runtimePromise = (async () => {
    // 两个接口互不依赖：并发拉取，避免 stats 等 containers 返回后才开始第二跳。
    await Promise.all([
      getQuiet('/api/docker/containers')
        .then(v => { containers.value = (v as any) || [] })
        .catch(() => { /* 保留上一帧，避免瞬时失败把列表清空 */ }),
      loadStats(),
    ])
  })().finally(() => { runtimePromise = null })
  return runtimePromise
}

// 并发去重：应用页在 keep-alive 内，首次进入时 onMounted 与 onActivated 会在同一 tick
// 各调一次 scan；不合并会重复扫描 compose 目录并重复 SyncDB（dir 唯一索引还会让后一次 500）。
let scanPromise: Promise<void> | null = null
const scan = () => {
  if (scanPromise) return scanPromise
  scanPromise = (async () => {
    scanning.value = true
    try {
      // 资源占用是慢腿，只影响卡片上的 CPU/内存小字：现在起跑，但不挡首屏。
      void loadStats()
      // 四路首屏数据互不依赖，全部并发：等待 ≈ 最慢一路，而不是四路串行之和。
      // allSettled 而非 all：Docker 不可用时仍要展示项目/站点，反之亦然。
      const [projRes, sitesRes, ctrRes, setRes] = await Promise.allSettled([
        get('/api/compose/scan'),
        get('/api/sites'),
        getQuiet<any>('/api/docker/containers'),
        getQuiet<any>('/api/settings'),
      ])
      if (projRes.status === 'fulfilled') projects.value = (projRes.value as any[]) || []
      if (sitesRes.status === 'fulfilled') sites.value = (sitesRes.value as any[]) || []
      if (ctrRes.status === 'fulfilled') containers.value = (ctrRes.value as any[]) || []
      if (setRes.status === 'fulfilled') composeRoot.value = setRes.value?.composeRoot || composeRoot.value
      // 选中项依赖项目列表，必须等并发结果落地后再对齐
      if (projRes.status === 'fulfilled') {
        if (selected.value) {
          selected.value = projects.value.find(p => p.dir === selected.value.dir) || null
        }
        // 默认选中列表第一项（按展示排序：运行中优先），而非后端扫描顺序
        if (projects.value.length && !selected.value) select(sortedProjects.value[0] || projects.value[0])
      }
    } finally {
      scanning.value = false
      scanPromise = null
    }
  })()
  return scanPromise
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
  // 该项目有仍在执行的操作任务（如刚刷新过页面）则自动恢复实时视图
  if (p.id) resumeOutJob(p.id, p.name)
}

const stopOutWatch = () => {
  outStopWs?.(); outStopWs = null
  clearInterval(outElapsedTimer); outElapsedTimer = null
  clearInterval(outPollTimer); outPollTimer = null
}

// 快照对齐：按全局行号去重追加；同时同步起点时间与终态（轮询兜底/补齐共用）
const applySnapshot = (r: any) => {
  if (!r?.found || !r.job) return
  const job = r.job
  if (!outStartedAt.value && job.startedAt) outStartedAt.value = new Date(job.startedAt).getTime() || Date.now()
  const start: number = job.start ?? 0
  const lines: string[] = job.lines || []
  for (let i = 0; i < lines.length; i++) {
    const idx = start + i
    if (idx < outNext) continue
    if (idx > outNext) outText.value += `…（缓冲已裁剪，跳过 ${idx - outNext} 行）\n`
    outText.value += lines[i] + '\n'
    outNext = idx + 1
  }
  if (job.status && job.status !== 'running') markFinished(job.status, job.error || '', job.endedAt)
}

let outPolling = false
const pollOnce = async () => {
  if (outPolling || outStatus.value !== 'running' || outJobProjectId.value == null) return
  outPolling = true
  try {
    applySnapshot(await get(`/api/compose/${outJobProjectId.value}/job`, { since: outNext }))
  } catch { /* 下轮重试 */ } finally { outPolling = false }
}

const markFinished = (status: 'success' | 'error', error: string, endedAt: string | null) => {
  if (outStatus.value !== 'running') return
  outStatus.value = status
  outError.value = error
  outEndedAt.value = endedAt ? new Date(endedAt).getTime() : Date.now()
  stopOutWatch()
  if (status === 'success') message.success(`${outTitle.value} 完成（${outElapsed.value}s）`)
  else message.error(`${outTitle.value} 失败：${outError.value || '请查看输出'}`)
  scan()
}

// 订阅 WS 实时帧 + 引导快照 + 3s 轮询兜底（WS 断连丢帧/终帧丢失时由快照补齐）
const trackRunningJob = (projectId: number) => {
  outJobProjectId.value = projectId
  outStopWs = wsTopic(`compose-act-${projectId}`, (m: any) => {
    if (outStatus.value !== 'running') return
    if (m.type === 'log') {
      if (m.idx === outNext) { outText.value += m.line + '\n'; outNext++ }
      else if (m.idx > outNext) pollOnce() // 漏帧：立即定向补拉
    } else if (m.type === 'done') {
      markFinished(m.ok ? 'success' : 'error', m.error || '', null)
    }
  })
  outElapsedTimer = setInterval(() => { nowTick.value++ }, 1000)
  outPollTimer = setInterval(pollOnce, 3000)
  pollOnce()
}

const projAction = async (action: string) => {
  if (!selected.value?.id) { message.warning('项目尚未导入（扫描后点击「按声明同步站点」或添加网站完成导入）'); return }
  if (projBusy.value) { outOpen.value = true; return }
  // 乐观开窗：POST 立即返回 jobId，输出随后经 WS 实时推送
  stopOutWatch()
  outText.value = ''
  outNext = 0
  outStartedAt.value = 0
  outEndedAt.value = 0
  outError.value = ''
  outStatus.value = 'running'
  outTitle.value = `${selected.value.name} · ${action}`
  outOpen.value = true
  outAutoScroll = true
  try {
    await post(`/api/compose/${selected.value.id}/action`, { action })
  } catch {
    outOpen.value = false // 启动失败（拦截器已提示），收起空窗
    outStatus.value = 'idle'
    return
  }
  trackRunningJob(selected.value.id)
}

// 页面刷新/切换项目后恢复仍在执行的任务视图（快照发现 running 即接管）
const resumeOutJob = async (projectId: number, projectName: string) => {
  if (outStatus.value === 'running') return
  let r: any
  try { r = await get(`/api/compose/${projectId}/job`, { since: 0 }) } catch { return }
  if (!r?.found || r.job?.status !== 'running') return
  stopOutWatch()
  outText.value = ''
  outNext = 0
  outStartedAt.value = 0
  outEndedAt.value = 0
  outError.value = ''
  outStatus.value = 'running'
  outTitle.value = `${projectName || r.job.project} · ${r.job.action}`
  outOpen.value = true
  outAutoScroll = true
  trackRunningJob(projectId)
}

// 自动滚动到底部；用户上滑离开底部（>40px）即暂停，回到底部自动恢复
watch(outText, () => {
  if (!outOpen.value || !outAutoScroll) return
  nextTick(() => { const el = outBox.value; if (el) el.scrollTop = el.scrollHeight })
})
const onOutScroll = () => {
  const el = outBox.value
  if (!el) return
  outAutoScroll = el.scrollHeight - el.scrollTop - el.clientHeight < 40
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

// ---- 站点最近请求：完整地图页「站点请求树」的单站点行内展开视图。数据同源 /api/logmap/summary?paths=1，
//      前端按域名过滤（logmap 的 site 键 = nginx server_name = 站点域名）。
//      首次展开拉取，存在展开行时每 30s 静默刷新，全部收起即停止 ----
const expandedKeys = ref<(string | number)[]>([])
const reqSummary = ref<any>(null)
let reqSummaryAt = 0
let reqTimer: any = null
const loadSiteReq = async (force = false) => {
  if (!force && Date.now() - reqSummaryAt < 30000) return // 30s 内展开过其它行直接复用缓存
  try {
    reqSummary.value = await get('/api/logmap/summary', { minutes: 1440, recent: 0, paths: 1 })
    reqSummaryAt = Date.now()
  } catch { /* 拦截器提示 */ }
}
const siteReq = (domain: string) => (reqSummary.value?.sites || []).find((s: any) => s.site === domain) || null
// 树结构：路径（·次数），节点样式与完整地图页请求树一致
const siteReqTree = (domain: string) =>
  (siteReq(domain)?.paths || []).map((p: any) => ({ key: p.path, title: `${p.path} · ${p.count} 次`, selectable: false, isLeaf: true }))
const ensureReqPolling = () => {
  loadSiteReq()
  if (!reqTimer) reqTimer = setInterval(() => { if (expandedKeys.value.length) loadSiteReq(true) }, 30000)
}
const stopReqPolling = () => { clearInterval(reqTimer); reqTimer = null }
const toggleSiteReq = (r: any) => {
  const has = expandedKeys.value.includes(r.id)
  expandedKeys.value = has ? expandedKeys.value.filter(k => k !== r.id) : [...expandedKeys.value, r.id]
  if (!has) ensureReqPolling()
  else if (!expandedKeys.value.length) stopReqPolling()
}
// 新标签打开该站点（按 SSL 标记决定协议）
const openSite = (r: any) => {
  window.open(`${r.ssl ? 'https' : 'http'}://${r.domain}`, '_blank', 'noopener')
}
// 展开图标切换与域名点击走同一份受控 expandedRowKeys
const onExpandedRowsChange = (keys: (string | number)[]) => {
  expandedKeys.value = keys
  if (keys.length) ensureReqPolling()
  else stopReqPolling()
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
  logsWs.onmessage = (ev) => { wsMessageToText(ev.data).then(t => { logsText.value += t }) }
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
  execWs.onmessage = (ev) => { wsMessageToText(ev.data).then(t => execTerm?.write(t)) }
  execWs.onclose = () => { execTerm?.write('\r\n\x1b[31m[会话结束]\x1b[0m') }
}
watch(() => ui.isDark, () => { if (execTerm) execTerm.options.theme = termTheme() })
const closeExec = () => {
  execWs?.close(); execWs = null
  execTerm?.dispose(); execTerm = null
}

let timer: any = null
onMounted(() => { scan() })

// 保活生命周期：激活重新扫描并恢复运行中任务的实时视图与展开行轮询；切走时全部暂停。
// （此前 outPollTimer/reqTimer 切走不清理：compose 任务运行中或站点行展开状态下离开页面，
// 3s/30s 轮询在后台持续打接口）
onActivated(() => {
  scan()
  timer = setInterval(refreshRuntime, 10000)
  // compose 任务运行中：切走时已停 WS/兜底轮询，回来接上（输出文本保留，续传不重放）
  if (outStatus.value === 'running' && outJobProjectId.value != null && !outPollTimer) {
    trackRunningJob(outJobProjectId.value)
  }
  if (expandedKeys.value.length) ensureReqPolling()
})
onDeactivated(() => {
  clearInterval(timer); timer = null
  if (outStatus.value === 'running') stopOutWatch() // WS 订阅与兜底轮询停掉，返回时 trackRunningJob 恢复
  stopReqPolling()
})
onUnmounted(() => { clearInterval(timer); stopReqPolling(); stopOutWatch(); logsWs?.close(); closeExec() })
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 14px; }
.dim { color: var(--color-slate); font-size: 12px; }
.proj-card { border: 1px solid var(--border-card); border-radius: 8px; padding: 12px; margin-bottom: 10px; cursor: pointer; transition: all var(--motion-fast); }
.proj-card.active { border-color: var(--color-corporate-blue); background: var(--bg-selected); }
/* 桌面端：左右两栏各自独立滚动（左列表/右详情最大高度 = 视口 - 页头/工具条占用） */
@media (min-width: 768px) {
  .apps-layout { align-items: flex-start; }
  .proj-col, .detail-col {
    max-height: calc(100vh - 230px);
    overflow-y: auto;
    padding-right: 4px;
  }
  .proj-col::-webkit-scrollbar, .detail-col::-webkit-scrollbar { width: 8px; }
  .proj-col::-webkit-scrollbar-thumb, .detail-col::-webkit-scrollbar-thumb {
    background: rgba(128,128,128,.3); border-radius: 4px;
  }
  .proj-col::-webkit-scrollbar-thumb:hover, .detail-col::-webkit-scrollbar-thumb:hover { background: rgba(128,128,128,.5); }
}
.proj-name { font-weight: 600; font-size: 14px; }
.proj-res { margin-top: 4px; font-size: 12px; color: #10b981; }
.dim-res { color: var(--color-slate); }
.proj-sub { font-size: 12px; color: var(--color-slate); margin: 4px 0; }
.proj-dir { font-size: 11px; color: #aaa; word-break: break-all; }
.domain-toggle { cursor: pointer; user-select: none; }
.domain-caret { font-size: 10px; margin-right: 6px; color: var(--color-slate); display: inline-block; transition: transform var(--motion-fast); }
.domain-caret.open { transform: rotate(90deg); }
.domain-toggle:hover .domain-caret { color: var(--color-corporate-blue); }
/* 域名右侧「新标签打开网站」外链图标按钮 */
.site-open {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; margin-left: 6px; padding: 3px;
  border-radius: 4px; color: var(--color-slate); cursor: pointer;
  vertical-align: middle; transition: color var(--motion-fast), background var(--motion-fast);
}
.site-open svg { width: 1em; height: 1em; fill: currentColor; font-size: 12px; }
.site-open:hover { color: var(--color-corporate-blue); background: rgba(59, 130, 246, .1); }
.decl, .runlog { max-height: 440px; overflow: auto; background: var(--surface-card); color: var(--color-ink); padding: 12px; font-size: 12px; white-space: pre-wrap; border: 1px solid var(--border-card); border-radius: 8px; font-family: ui-monospace, Consolas, monospace; }
.dir-list { max-height: 300px; overflow: auto; border: 1px solid var(--border-card); border-radius: 8px; }
.dir-item { display: flex; align-items: center; padding: 9px 14px; cursor: pointer; border-bottom: 1px solid var(--border-card); }
.dir-item:hover { background: var(--bg-selected); }
/* 移动端：项目列表压缩成横向滑动的卡片带，详情占满宽度（上下布局，不出横向溢出） */
@media (max-width: 767px) {
  .apps-layout { display: block; }
  .proj-col { width: 100%; max-width: 100%; display: flex; gap: 8px; overflow-x: auto; padding-bottom: 6px; }
  .proj-col .proj-card { flex: 0 0 74%; max-width: 74%; margin-bottom: 0; }
  .proj-col .ant-empty { flex: 0 0 100%; }
  .detail-col { width: 100%; max-width: 100%; margin-top: 10px; }
}
</style>
