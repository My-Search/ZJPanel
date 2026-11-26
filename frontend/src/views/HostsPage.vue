<template>
  <div class="hosts-page">
    <!-- 左：主机列表 -->
    <a-card size="small" class="list-card" :body-style="{ padding: '8px' }">
      <div class="list-head">
        <a-input v-model:value="kw" size="small" placeholder="搜索主机 / 地址 / 分组" allow-clear />
        <a-dropdown>
          <a-button size="small" type="primary" block style="margin-top: 8px">
            <PlusOutlined /> 添加主机
          </a-button>
          <template #overlay>
            <a-menu @click="({ key }: any) => key === 'ssh' ? openAdd() : openRegister()">
              <a-menu-item key="ssh">SSH 添加（可顺带装 Agent）</a-menu-item>
              <a-menu-item key="cmd">命令安装（复制到目标机执行）</a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </div>
      <div class="list-body">
        <div
          v-for="h in filtered" :key="h.id"
          class="host-item" :class="{ active: h.id === current?.id }"
          @click="select(h)"
        >
          <span class="dot" :class="statusClass(h)" />
          <div class="host-meta">
            <div class="host-name" :title="h.name">
              {{ h.name }}
              <a-tag v-if="h.local" class="mini-tag">内置</a-tag>
              <a-tag v-else-if="!h.claimed" class="mini-tag" color="orange">待认领</a-tag>
            </div>
            <div class="host-addr mono">{{ h.local ? '面板所在机器' : `${h.username}@${h.host}:${h.port}` }}</div>
          </div>
          <div class="host-right">
            <a-tag v-if="h.outdated" color="orange" class="mini-tag">可升级</a-tag>
            <span v-else-if="h.agentVersion && !h.local" class="ver">{{ h.agentVersion }}</span>
          </div>
        </div>
        <a-empty v-if="!filtered.length" :image="simpleImage" description="暂无主机" />
      </div>
    </a-card>

    <!-- 右：主机详情 -->
    <a-card size="small" class="detail-card" :body-style="{ padding: '0' }">
      <template v-if="current">
        <div class="detail-head">
          <div class="dh-left">
            <div class="dh-title">
              {{ current.name }}
              <a-tag :color="statusColor(current)">{{ statusText(current) }}</a-tag>
              <a-tag v-if="current.outdated" color="orange">Agent 落后：{{ current.agentVersion }} → {{ current.panelVersion }}</a-tag>
            </div>
            <div class="dh-sub mono">
              {{ current.local ? '127.0.0.1（面板容器）' : `${current.username}@${current.host}:${current.port}` }}
              <span v-if="current.remoteIp"> · 回连 IP {{ current.remoteIp }}</span>
              <span v-if="current.lastSeen"> · 最后在线 {{ fmtTime(current.lastSeen) }}</span>
            </div>
          </div>
          <a-space wrap>
            <a-button size="small" @click="testHost" :loading="testing">测试连接</a-button>
            <a-button size="small" type="primary" ghost @click="openTerminal">打开终端</a-button>
            <a-button v-if="canInstall" size="small" type="primary" :loading="installing" @click="installAgent">安装 Agent</a-button>
            <a-button v-if="current.online && !current.local" size="small" @click="upgradeAgent" :loading="upgrading">升级 Agent</a-button>
          </a-space>
        </div>

        <a-tabs v-model:activeKey="tab" size="small" class="detail-tabs">
          <a-tab-pane key="overview" tab="概览">
            <div class="pane">
              <a-alert
                v-if="current.local" type="info" show-icon
                message="这是内置的「本机」条目，承接面板容器内的本地 Shell；不需要也不支持安装 Agent。"
              />
              <a-row :gutter="12" v-if="!current.local">
                <a-col :xs="12" :md="6"><a-card size="small" class="stat"><div class="stat-label">CPU</div><div class="stat-value">{{ m.cpu ? m.cpu.toFixed(1) : '-' }}<span class="unit">%</span></div><div class="stat-sub">负载 {{ m.load1 ?? '-' }} / {{ m.load5 ?? '-' }} / {{ m.load15 ?? '-' }}</div></a-card></a-col>
                <a-col :xs="12" :md="6"><a-card size="small" class="stat"><div class="stat-label">内存</div><div class="stat-value">{{ pct(m.memAvailable, m.memTotal) }}<span class="unit">%</span></div><div class="stat-sub">{{ fmtBytes((m.memTotal || 0) - (m.memAvailable || 0)) }} / {{ fmtBytes(m.memTotal) }}</div></a-card></a-col>
                <a-col :xs="12" :md="6"><a-card size="small" class="stat"><div class="stat-label">磁盘 /</div><div class="stat-value">{{ pct((m.diskTotal || 0) - (m.diskUsed || 0), m.diskTotal) }}<span class="unit">%</span></div><div class="stat-sub">已用 {{ fmtBytes(m.diskUsed) }} / {{ fmtBytes(m.diskTotal) }}</div></a-card></a-col>
                <a-col :xs="12" :md="6"><a-card size="small" class="stat"><div class="stat-label">连接 / SSH</div><div class="stat-value">{{ m.conns ?? '-' }}<span class="unit">/ {{ m.sshSessions ?? '-' }}</span></div><div class="stat-sub">运行 {{ uptimeText }}</div></a-card></a-col>
              </a-row>

              <a-row :gutter="12" style="margin-top: 12px">
                <a-col :xs="24" :lg="14">
                  <a-card size="small" title="监听端口" :body-style="{ padding: '0 8px 8px' }">
                    <a-table
                      :data-source="ports" :columns="portCols" row-key="key" size="small"
                      :pagination="{ pageSize: 8, size: 'small' }" :locale="{ emptyText: '未采集到端口（Agent 在线后自动上报）' }"
                    />
                  </a-card>
                </a-col>
                <a-col :xs="24" :lg="10">
                  <a-card size="small" title="Agent" style="height: 100%">
                    <a-descriptions :column="1" size="small">
                      <a-descriptions-item label="状态">{{ statusText(current) }}</a-descriptions-item>
                      <a-descriptions-item label="版本">{{ current.agentVersion || '-' }}（面板 {{ current.panelVersion }}）</a-descriptions-item>
                      <a-descriptions-item label="协议">{{ current.agentProto || '-' }}</a-descriptions-item>
                      <a-descriptions-item label="系统">{{ current.osArch || '-' }}</a-descriptions-item>
                      <a-descriptions-item label="分组">{{ current.group || '-' }}</a-descriptions-item>
                      <a-descriptions-item label="最后心跳">{{ current.lastSeen ? fmtTime(current.lastSeen) : '-' }}</a-descriptions-item>
                    </a-descriptions>
                  </a-card>
                </a-col>
              </a-row>
            </div>
          </a-tab-pane>

          <a-tab-pane key="defense" tab="防御">
            <div class="pane">
              <a-alert
                v-if="!current.online || current.local" type="warning" show-icon
                message="主机防御需要 Agent 在线；请先安装并等待回连。"
              />
              <a-card size="small" title="SSH 基线检查" extra="只读，不改配置">
                <div class="check-row">
                  <div>
                    <div class="check-title">检查 root 登录、密码登录、重试次数、空密码等关键项</div>
                    <div class="hint">检查结果会保留在「事件」里，便于事后追溯。</div>
                  </div>
                  <a-button type="primary" size="small" :disabled="!current.online || current.local" :loading="checking" @click="runSshCheck">开始检查</a-button>
                </div>
                <a-table
                  v-if="findings.length" :data-source="findings" :columns="checkCols" row-key="key" size="small"
                  :pagination="false" style="margin-top: 10px"
                >
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'risk'">
                      <a-tag :color="record.risk === 'critical' ? 'red' : record.risk === 'warn' ? 'orange' : 'green'">
                        {{ record.risk === 'critical' ? '高危' : record.risk === 'warn' ? '建议' : '正常' }}
                      </a-tag>
                    </template>
                  </template>
                </a-table>
              </a-card>

              <a-card size="small" title="后续能力（规划）" style="margin-top: 12px">
                <div class="hint">
                  账号与持久化基线（passwd / sudoers / authorized_keys / cron / systemd）、端口暴露面、webshell 扫描、nftables 封禁。
                  Agent 通道与检查框架已经就位，这些检查会以同样的「检查 → 预览 → 应用 → 回滚」形态接入。
                </div>
              </a-card>
            </div>
          </a-tab-pane>

          <a-tab-pane key="events" tab="事件">
            <div class="pane">
              <a-table
                :data-source="events" :columns="eventCols" row-key="id" size="small"
                :pagination="false" :locale="{ emptyText: '暂无事件' }"
              >
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'time'">{{ fmtTime(record.time) }}</template>
                  <template v-else-if="column.key === 'severity'">
                    <a-tag :color="record.severity === 'critical' ? 'red' : record.severity === 'warn' ? 'orange' : 'blue'">
                      {{ record.severity === 'critical' ? '严重' : record.severity === 'warn' ? '警告' : '提示' }}
                    </a-tag>
                  </template>
                </template>
              </a-table>
            </div>
          </a-tab-pane>

          <a-tab-pane key="terminal" tab="终端">
            <div class="pane">
              <a-result
                status="info" title="在终端中操作这台主机"
                sub-title="终端会以独立标签页打开，保持长连接；关闭标签页可回到这里。"
              >
                <template #extra>
                  <a-button type="primary" @click="openTerminal">打开 SSH 终端</a-button>
                  <a-button @click="openLocal" v-if="current.local">打开本地 Shell</a-button>
                </template>
              </a-result>
            </div>
          </a-tab-pane>

          <a-tab-pane key="settings" tab="设置">
            <div class="pane">
              <a-form layout="vertical" style="max-width: 720px">
                <a-row :gutter="12">
                  <a-col :span="12"><a-form-item label="名称"><a-input v-model:value="edit.name" /></a-form-item></a-col>
                  <a-col :span="12"><a-form-item label="分组"><a-input v-model:value="edit.group" placeholder="如 生产 / 边缘" /></a-form-item></a-col>
                </a-row>
                <a-form-item label="面板地址（目标机访问面板用的地址）">
                  <a-input v-model:value="panelUrl" placeholder="https://panel.example.com" />
                  <div class="hint">Agent 从这里回连；填错会导致「已安装但一直离线」。留空则用当前浏览器地址。</div>
                </a-form-item>
                <a-space>
                  <a-button type="primary" :loading="saving" @click="saveHost">保存</a-button>
                  <a-popconfirm
                    v-if="!current.local" title="确认卸载该主机的 Agent？卸载成功后才能删除记录。"
                    @confirm="uninstallAgent"
                  >
                    <a-button danger :disabled="!current.online">卸载 Agent</a-button>
                  </a-popconfirm>
                  <a-popconfirm v-if="!current.local" title="删除该主机记录？" @confirm="removeHost">
                    <a-button danger type="text" :disabled="current.online">删除记录</a-button>
                  </a-popconfirm>
                </a-space>
              </a-form>
            </div>
          </a-tab-pane>
        </a-tabs>
      </template>
      <a-empty v-else description="选择左侧主机查看详情" style="padding: 80px 0" />
    </a-card>

    <!-- 添加主机 -->
    <a-modal v-model:open="addOpen" title="SSH 添加主机" :confirm-loading="addSaving" @ok="saveNewHost" ok-text="保存">
      <a-form layout="vertical">
        <a-row :gutter="12">
          <a-col :span="12"><a-form-item label="名称"><a-input v-model:value="form.name" placeholder="可留空，默认用地址" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="分组"><a-input v-model:value="form.group" placeholder="可留空" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="16"><a-form-item label="地址" required><a-input v-model:value="form.host" placeholder="1.2.3.4 或域名" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="端口"><a-input-number v-model:value="form.port" :min="1" :max="65535" style="width: 100%" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="12"><a-form-item label="用户" required><a-input v-model:value="form.username" placeholder="root" /></a-form-item></a-col>
          <a-col :span="12">
            <a-form-item label="认证">
              <a-radio-group v-model:value="form.authType">
                <a-radio-button value="password">密码</a-radio-button>
                <a-radio-button value="key">私钥</a-radio-button>
              </a-radio-group>
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item v-if="form.authType === 'password'" label="密码"><a-input-password v-model:value="form.password" /></a-form-item>
        <template v-else>
          <a-form-item label="私钥"><a-textarea v-model:value="form.privateKey" :rows="4" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></a-form-item>
          <a-form-item label="私钥口令（可选）"><a-input-password v-model:value="form.passphrase" /></a-form-item>
        </template>
        <a-form-item label="面板地址（可选）">
          <a-input v-model:value="form.panelUrl" :placeholder="defaultPanelUrl" />
        </a-form-item>
        <a-checkbox v-model:checked="form.install">保存后立即通过 SSH 安装 Agent（推荐）</a-checkbox>
      </a-form>
    </a-modal>

    <!-- 命令安装 -->
    <a-modal v-model:open="regOpen" title="命令安装 Agent" :footer="null" width="720px">
      <a-form layout="vertical">
        <a-row :gutter="12">
          <a-col :span="12"><a-form-item label="名称（可留空）"><a-input v-model:value="reg.name" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="分组（可留空）"><a-input v-model:value="reg.group" /></a-form-item></a-col>
        </a-row>
        <a-form-item label="面板地址（目标机访问面板用的地址）">
          <a-input v-model:value="reg.panelUrl" :placeholder="defaultPanelUrl" />
        </a-form-item>
        <a-button type="primary" :loading="regLoading" @click="genCommand">生成安装命令</a-button>
        <template v-if="regCommand">
          <div class="cmd-box mono">{{ regCommand }}</div>
          <a-space style="margin-top: 8px">
            <a-button size="small" @click="copyCmd">复制命令</a-button>
            <a-button size="small" type="link" :href="regScriptUrl" target="_blank">查看脚本全文（可审阅）</a-button>
            <a-button size="small" type="link" @click="regOpen = false; load()">完成（回连后出现在列表）</a-button>
          </a-space>
        </template>
      </a-form>
    </a-modal>

    <!-- 安装输出 -->
    <a-modal v-model:open="outOpen" title="安装输出" :footer="null" width="760px">
      <pre class="out-box mono">{{ output }}</pre>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Empty, message } from 'ant-design-vue'
import { PlusOutlined } from '@ant-design/icons-vue'
import { get, getQuiet, post, del, wsTopic, fmtBytes, fmtTime } from '../api'

const simpleImage = Empty.PRESENTED_IMAGE_SIMPLE
const router = useRouter()

const hosts = ref<any[]>([])
const current = ref<any>(null)
const kw = ref('')
const tab = ref('overview')
const m = ref<any>({})
const ports = ref<any[]>([])
const events = ref<any[]>([])
const findings = ref<any[]>([])
const checking = ref(false)
const installing = ref(false)
const upgrading = ref(false)
const testing = ref(false)
const saving = ref(false)
const output = ref('')
const outOpen = ref(false)

const addOpen = ref(false)
const addSaving = ref(false)
const form = reactive<any>({ name: '', group: '', host: '', port: 22, username: 'root', authType: 'password', password: '', privateKey: '', passphrase: '', panelUrl: '', install: true })
const edit = reactive<any>({ name: '', group: '' })
const panelUrl = ref('')

const regOpen = ref(false)
const regLoading = ref(false)
const reg = reactive<any>({ name: '', group: '', panelUrl: '' })
const regCommand = ref('')
const regScriptUrl = ref('')

const defaultPanelUrl = `${location.protocol}//${location.host}`

const filtered = computed(() => {
  const q = kw.value.trim().toLowerCase()
  if (!q) return hosts.value
  return hosts.value.filter(h => [h.name, h.host, h.group].some(v => String(v || '').toLowerCase().includes(q)))
})

const canInstall = computed(() => current.value && !current.value.local && !current.value.online && current.value.agentStatus !== 'online')

const statusClass = (h: any) => h.local ? 'dot-local' : h.online || h.agentStatus === 'online' ? 'dot-on' : h.agentStatus === 'pending' ? 'dot-pending' : h.agentStatus === 'uninstalled' ? 'dot-off' : 'dot-off'
const statusText = (h: any) => h.local ? '内置' : h.agentStatus === 'local' ? '内置' : h.online || h.agentStatus === 'online' ? '在线' : h.agentStatus === 'pending' ? '待回连' : h.agentStatus === 'uninstalled' ? '已卸载' : h.agentStatus === 'none' || !h.agentStatus ? '未安装' : '离线'
const statusColor = (h: any) => h.local ? 'blue' : h.online || h.agentStatus === 'online' ? 'green' : h.agentStatus === 'pending' ? 'orange' : 'default'

const portCols = [
  { title: '协议', dataIndex: 'proto', key: 'proto', width: 70 },
  { title: '地址', dataIndex: 'addr', key: 'addr', width: 140 },
  { title: '端口', dataIndex: 'port', key: 'port', width: 80 },
]
const eventCols = [
  { title: '时间', key: 'time', width: 160 },
  { title: '级别', key: 'severity', width: 80 },
  { title: '类型', dataIndex: 'kind', key: 'kind', width: 160 },
  { title: '详情', dataIndex: 'detail', key: 'detail', ellipsis: true },
]
const checkCols = [
  { title: '配置项', dataIndex: 'key', key: 'key', width: 180 },
  { title: '当前值', dataIndex: 'value', key: 'value', width: 140 },
  { title: '结论', key: 'risk', width: 90 },
  { title: '建议', dataIndex: 'advice', key: 'advice', ellipsis: true },
]

const pct = (used: any, total: any) => {
  const t = Number(total) || 0
  if (!t) return '-'
  return ((1 - (Number(used) || 0) / t) * 100).toFixed(0)
}
const uptimeText = computed(() => {
  const s = Number(m.value.uptime) || 0
  if (!s) return '-'
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600)
  return d ? `${d} 天 ${h} 小时` : `${h} 小时`
})

const load = async () => {
  const list = await get<any[]>('/api/hosts')
  hosts.value = list
  if (!current.value && list.length) select(list[0])
  else if (current.value) {
    const h = list.find(x => x.id === current.value.id)
    if (h) current.value = h
  }
}

const select = async (h: any) => {
  current.value = h
  tab.value = 'overview'
  edit.name = h.name
  edit.group = h.group || ''
  findings.value = []
  await loadDetail()
}

const loadDetail = async () => {
  if (!current.value) return
  m.value = {}
  ports.value = []
  events.value = []
  try {
    const r = await getQuiet(`/api/hosts/${current.value.id}/metrics`)
    m.value = r.metrics || {}
    ports.value = (r.metrics?.ports || []).map((p: any) => ({ ...p, key: `${p.proto}-${p.port}` }))
  } catch { /* 离线时正常 */ }
  try { events.value = await getQuiet(`/api/hosts/${current.value.id}/events?limit=50`) } catch { /* ignore */ }
}

const testHost = async () => {
  testing.value = true
  try {
    const r = await post<any>(`/api/hosts/${current.value.id}/test`)
    message[r.ok ? 'success' : 'error'](r.ok ? 'SSH 连接正常' : (r.error || '连接失败'))
  } catch (e: any) {
    message.error(e?.response?.data?.error || '连接失败')
  } finally { testing.value = false }
}

const installAgent = async () => {
  installing.value = true
  try {
    const r = await post<any>(`/api/hosts/${current.value.id}/agent/install`, { panelUrl: panelUrl.value || '' })
    output.value = r.output || ''
    outOpen.value = true
    message.success('安装脚本已执行，等待 Agent 回连')
    setTimeout(load, 3000)
  } catch (e: any) {
    output.value = e?.response?.data?.output || ''
    outOpen.value = !!output.value
    message.error(e?.response?.data?.error || '安装失败')
  } finally { installing.value = false }
}

const upgradeAgent = async () => {
  upgrading.value = true
  try {
    const r = await post<any>(`/api/hosts/${current.value.id}/agent/upgrade`)
    output.value = r.output || '已下发升级'
    outOpen.value = true
    message.success('升级指令已下发')
    setTimeout(load, 3000)
  } catch (e: any) {
    message.error(e?.response?.data?.error || '升级失败')
  } finally { upgrading.value = false }
}

const uninstallAgent = async () => {
  try {
    const r = await post(`/api/hosts/${current.value.id}/agent/uninstall`)
    if (r.warning) message.warning(r.warning, 6)
    else message.success('Agent 已卸载，可以删除记录了')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '卸载失败')
  }
}

const removeHost = async () => {
  try {
    await del(`/api/hosts/${current.value.id}`)
    message.success('已删除')
    current.value = null
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '删除失败')
  }
}

const saveHost = async () => {
  saving.value = true
  try {
    await post('/api/hosts', {
      id: current.value.id, name: edit.name, group: edit.group,
      host: current.value.host, port: current.value.port, username: current.value.username,
      authType: current.value.authType,
    })
    message.success('已保存')
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { saving.value = false }
}

const runSshCheck = async () => {
  checking.value = true
  try {
    const r = await post<any>(`/api/hosts/${current.value.id}/check/ssh`)
    findings.value = r.result?.findings || []
    message.success('检查完成')
    await loadDetail()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '检查失败')
  } finally { checking.value = false }
}

const openTerminal = () => {
  if (current.value?.local) return openLocal()
  router.push({ path: '/terminal', query: { host: String(current.value.id) } })
}
const openLocal = () => router.push({ path: '/terminal', query: { local: '1' } })

const openAdd = () => {
  Object.assign(form, { name: '', group: '', host: '', port: 22, username: 'root', authType: 'password', password: '', privateKey: '', passphrase: '', panelUrl: '', install: true })
  addOpen.value = true
}
const saveNewHost = async () => {
  if (!form.host) { message.warning('地址必填'); return }
  addSaving.value = true
  try {
    const r = await post<any>('/api/hosts', form)
    if (r.ok === false) { message.error(r.error || '安装失败'); output.value = r.output || ''; outOpen.value = !!output.value }
    else {
      message.success(form.install ? '主机已保存，Agent 安装已执行' : '主机已保存')
      addOpen.value = false
      await load()
    }
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { addSaving.value = false }
}

const openRegister = () => {
  reg.name = ''
  reg.group = ''
  reg.panelUrl = ''
  regCommand.value = ''
  regScriptUrl.value = ''
  regOpen.value = true
}
const genCommand = async () => {
  regLoading.value = true
  try {
    const r = await post<any>('/api/agent/register', reg)
    regCommand.value = r.command
    regScriptUrl.value = r.scriptUrl
    await load()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '生成失败')
  } finally { regLoading.value = false }
}
const copyCmd = async () => {
  try { await navigator.clipboard.writeText(regCommand.value); message.success('已复制') } catch { message.warning('复制失败，请手动选择') }
}

let unsub: (() => void) | null = null
onMounted(async () => {
  await load()
  unsub = wsTopic('host-event', () => { load(); if (current.value) loadDetail() })
})
onUnmounted(() => unsub?.())
watch(() => current.value?.id, () => loadDetail())
</script>

<style scoped>
.hosts-page { display: flex; gap: 12px; height: 100%; min-height: 0; }
.list-card { width: 300px; flex: none; display: flex; flex-direction: column; }
.list-card :deep(.ant-card-body) { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.list-head { flex: none; }
.list-body { flex: 1; min-height: 0; overflow-y: auto; margin-top: 8px; }
.host-item { display: flex; align-items: center; gap: 8px; padding: 8px; border-radius: 8px; cursor: pointer; transition: background .15s; }
.host-item:hover { background: rgba(22,119,255,.06); }
.host-item.active { background: rgba(22,119,255,.12); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: #94a3b8; }
.dot-on { background: #10b981; }
.dot-pending { background: #f59e0b; }
.dot-local { background: #3b82f6; }
.dot-off { background: #cbd5e1; }
.host-meta { min-width: 0; flex: 1; }
.host-name { font-size: 13px; font-weight: 600; color: var(--color-ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.host-addr { font-size: 11px; color: var(--color-slate); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.host-right { flex: none; }
.mini-tag { font-size: 10px; line-height: 16px; padding: 0 4px; margin-left: 4px; }
.ver { font-size: 11px; color: var(--color-slate); }
.detail-card { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.detail-card :deep(.ant-card-body) { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 12px 16px 0; flex-wrap: wrap; }
.dh-title { font-size: 16px; font-weight: 600; color: var(--color-ink); }
.dh-sub { font-size: 12px; color: var(--color-slate); margin-top: 2px; }
.detail-tabs { flex: 1; min-height: 0; padding: 0 16px; display: flex; flex-direction: column; }
.detail-tabs :deep(.ant-tabs-content-holder) { overflow-y: auto; }
.pane { padding: 4px 2px 16px; }
.stat { height: 86px; }
.stat-label { font-size: 12px; color: var(--color-slate); }
.stat-value { font-size: 24px; font-weight: 600; line-height: 1.25; color: var(--color-ink); }
.unit { font-size: 12px; font-weight: 400; color: var(--color-slate); margin-left: 3px; }
.stat-sub { font-size: 12px; color: var(--color-slate); }
.hint { font-size: 12px; color: var(--color-slate); margin-top: 2px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.check-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.check-title { font-size: 13px; color: var(--color-ink); }
.cmd-box { margin-top: 12px; padding: 10px; border-radius: 8px; background: rgba(15,23,42,.06); font-size: 12px; word-break: break-all; }
.out-box { max-height: 60vh; overflow: auto; padding: 10px; border-radius: 8px; background: rgba(15,23,42,.06); font-size: 12px; white-space: pre-wrap; }
@media (max-width: 900px) {
  .hosts-page { flex-direction: column; }
  .list-card { width: 100%; max-height: 260px; }
}
</style>
