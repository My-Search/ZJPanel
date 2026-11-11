<template>
  <a-card size="small">
    <a-tabs v-model:activeKey="tab">
      <!-- ===== 证书任务（certd 式管线列表，默认页） ===== -->
      <a-tab-pane key="tasks" tab="证书任务">
        <div class="toolbar">
          <a-button type="primary" size="large" @click="openWizard()">
            <PlusOutlined /> 申请证书
          </a-button>
          <span class="dim">
            三步完成：申请信息 → 验证方式（HTTP-01 / DNS-01 通配符）→ 部署配置（签发后自动部署、按 cron 自动续期）
          </span>
        </div>
        <a-empty v-if="!tasks.length" description="还没有证书任务" style="margin: 48px 0">
          <a-button type="primary" @click="openWizard()">立即申请第一张证书</a-button>
        </a-empty>
        <a-table v-else :data-source="tasks" :columns="taskCols" size="small" row-key="id" :pagination="false">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'domains'">
              <a-tag v-for="d in record.domains" :key="d" color="blue">{{ d }}</a-tag>
            </template>
            <template v-else-if="column.key === 'challenge'">
              <a-tag :color="record.challenge === 'dns' ? 'purple' : 'cyan'">{{ record.challenge === 'dns' ? 'DNS-01' : 'HTTP-01' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'cert'">
              <a-tag v-if="!record.cert" color="default">未签发</a-tag>
              <a-tag v-else-if="record.cert.daysLeft <= 10" color="red">即将到期 · 剩 {{ record.cert.daysLeft }} 天</a-tag>
              <a-tag v-else-if="record.cert.daysLeft <= 30" color="orange">剩 {{ record.cert.daysLeft }} 天</a-tag>
              <a-tag v-else color="green">有效 · 剩 {{ record.cert.daysLeft }} 天</a-tag>
            </template>
            <template v-else-if="column.key === 'targets'">
              <a-tag v-for="(dt, i) in record.deployTargets" :key="i" color="geekblue">{{ targetTitle(dt.type) }}</a-tag>
              <span v-if="!record.deployTargets?.length" class="dim">仅保存仓库</span>
            </template>
            <template v-else-if="column.key === 'enabled'">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '自动续期' : '已停用' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'nextRunAt'">
              <span v-if="record.nextRunAt">{{ fmtTime(record.nextRunAt) }}</span>
              <span v-else class="dim">-</span>
            </template>
            <template v-else-if="column.key === 'action'">
              <a-space>
                <a-button size="small" type="primary" ghost @click="runTask(record)">立即执行</a-button>
                <a-button size="small" @click="openRuns(record)">日志</a-button>
                <a-button size="small" @click="openWizard(record)">编辑</a-button>
                <a-popconfirm title="删除该任务？（已签发证书保留在仓库）" @confirm="delTask(record)">
                  <a-button size="small" danger>删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </template>
        </a-table>
      </a-tab-pane>

      <!-- ===== 证书仓库 ===== -->
      <a-tab-pane key="infos" tab="证书仓库">
        <a-table :data-source="infos" :columns="infoCols" size="small" row-key="id" :pagination="false">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'domains'">
              <a-tag v-for="d in parseDomains(record.domains)" :key="d">{{ d }}</a-tag>
            </template>
            <template v-else-if="column.key === 'daysLeft'">
              <a-tag :color="daysLeft(record) <= 10 ? 'red' : daysLeft(record) <= 30 ? 'orange' : 'green'">剩 {{ daysLeft(record) }} 天</a-tag>
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status }}</a-tag>
            </template>
            <template v-else-if="column.key === 'action'">
              <a-space>
                <a-button size="small" @click="viewCert(record, 'cert')">证书</a-button>
                <a-button size="small" @click="viewCert(record, 'key')">私钥</a-button>
              </a-space>
            </template>
          </template>
        </a-table>
      </a-tab-pane>

      <!-- ===== 运行历史 ===== -->
      <a-tab-pane key="runs" tab="运行历史">
        <a-table :data-source="runs" :columns="runCols" size="small" row-key="id" :pagination="{ pageSize: 10 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'status'">
              <a-tag :color="runColor(record.status)">{{ record.status }}</a-tag>
            </template>
            <template v-else-if="column.key === 'action'">
              <a-button size="small" @click="viewRun(record)">日志</a-button>
            </template>
          </template>
        </a-table>
      </a-tab-pane>

      <!-- ===== 账户与凭证 ===== -->
      <a-tab-pane key="accounts" tab="账户与凭证">
        <a-space direction="vertical" style="width: 100%">
          <a-card title="ACME 账户" size="small">
            <template #extra><a-button size="small" type="primary" @click="editAcct = { type: 'staging' }; acctOpen = true">添加账户</a-button></template>
            <a-table :data-source="accounts" :columns="acctCols" size="small" row-key="id" :pagination="false">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'action'">
                  <a-popconfirm title="删除该账户？" @confirm="delAcct(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
                </template>
              </template>
            </a-table>
          </a-card>
          <a-card title="DNS 提供商凭证" size="small">
            <template #extra><a-button size="small" type="primary" @click="openCred()">添加凭证</a-button></template>
            <a-table :data-source="credentials" :columns="credCols" size="small" row-key="id" :pagination="false">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'action'">
                  <a-popconfirm title="删除该凭证？" @confirm="delCred(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
                </template>
              </template>
            </a-table>
          </a-card>
        </a-space>
      </a-tab-pane>
    </a-tabs>

    <!-- ============ 申请证书向导（certd 式三步） ============ -->
    <a-modal v-model:open="wizOpen" title="申请证书" :width="800" :mask-closable="false" :footer="null">
      <a-steps :current="step" size="small" style="margin: 8px 0 20px"
        :items="[{ title: '申请信息' }, { title: '验证方式' }, { title: '部署配置' }]" />
      <a-form layout="vertical" style="max-height: 52vh; overflow: auto; padding: 0 4px">
        <!-- Step 1 申请信息 -->
        <div v-show="step === 0">
          <a-form-item label="任务名称" required>
            <a-input v-model:value="wiz.name" placeholder="如：主站证书" />
          </a-form-item>
          <a-form-item label="域名（回车添加，支持通配符 *.example.com）" required>
            <a-select v-model:value="wiz.domains" mode="tags" placeholder="example.com" :token-separators="[',', ' ']" />
          </a-form-item>
          <a-row :gutter="8">
            <a-col :span="12">
              <a-form-item label="ACME 账户" required>
                <a-select v-model:value="wiz.acmeAccountId" :options="acctOpts" placeholder="选择账户" />
              </a-form-item>
              <a-button size="small" style="margin-top: -18px" @click="quickAcct = { type: 'staging' }; quickAcctOpen = true">
                没有账户？快速创建
              </a-button>
            </a-col>
            <a-col :span="12">
              <a-form-item label="续期提前天数">
                <a-input-number v-model:value="wiz.renewDays" :min="1" style="width: 100%" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-row :gutter="8">
            <a-col :span="12">
              <a-form-item label="检查 Cron（每日自动检查到期并续期）">
                <a-input v-model:value="wiz.cronSpec" placeholder="0 3 * * *" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="自动续期">
                <a-switch v-model:checked="wiz.enabled" />
                <span class="dim" style="margin-left: 10px">未到期自动跳过（certd SkipWhenSucceed 策略）</span>
              </a-form-item>
            </a-col>
          </a-row>
        </div>

        <!-- Step 2 验证方式 -->
        <div v-show="step === 1">
          <a-form-item label="验证方式" required>
            <div class="mode-cards">
              <div class="mode-card" :class="{ active: wiz.challenge === 'http' }" @click="wiz.challenge = 'http'">
                <div class="mode-title">HTTP-01</div>
                <div class="mode-desc">nginx 已将 /.well-known/acme-challenge 转发到面板挑战端口，无需额外配置；不支持通配符</div>
              </div>
              <div class="mode-card" :class="{ active: wiz.challenge === 'dns' }" @click="wiz.challenge = 'dns'">
                <div class="mode-title">DNS-01</div>
                <div class="mode-desc">通过 DNS 提供商 API 添加 TXT 记录验证；支持通配符证书</div>
              </div>
            </div>
          </a-form-item>
          <template v-if="wiz.challenge === 'dns'">
            <a-form-item label="DNS 凭证（提供商 + API 密钥）" required>
              <a-select v-model:value="wiz.dnsCredentialId" :options="credOpts" placeholder="选择凭证" />
            </a-form-item>
            <a-button size="small" @click="openCred()">没有凭证？添加 DNS 凭证</a-button>
          </template>
          <template v-else>
            <a-alert type="info" show-icon
              message="HTTP-01 由 nginx 自动转发挑战请求，站点 conf 已内置转发规则，无需手动配置。"
              style="margin-top: 8px" />
          </template>
        </div>

        <!-- Step 3 部署配置 -->
        <div v-show="step === 2">
          <a-alert type="info" show-icon style="margin-bottom: 12px"
            message="签发成功后并行执行以下部署目标（可多个）；不添加则证书仅保存到仓库。" />
          <div v-for="(dt, i) in wiz.deployTargets" :key="i" class="target-card">
            <div class="target-head">
              <a-select v-model:value="dt.type" style="width: 280px" :options="targetOpts" @change="dt.config = {}" />
              <a-button size="small" danger @click="wiz.deployTargets.splice(i, 1)">移除</a-button>
            </div>
            <a-form v-if="targetDef(dt.type)" layout="vertical" style="margin: 0">
              <a-form-item v-for="f in targetDef(dt.type).fields" :key="f.key" :label="f.title" style="margin-bottom: 8px" :help="f.help">
                <a-textarea v-if="f.type === 'textarea'" v-model:value="dt.config[f.key]" :rows="2" />
                <a-input-password v-else-if="f.type === 'password'" v-model:value="dt.config[f.key]" :placeholder="f.default" />
                <a-input v-else v-model:value="dt.config[f.key]" :placeholder="f.default" />
              </a-form-item>
            </a-form>
          </div>
          <a-dropdown>
            <a-button style="width: 100%; border-style: dashed">
              <PlusOutlined /> 添加部署目标
            </a-button>
            <template #overlay>
              <a-menu>
                <a-menu-item v-for="t in targets" :key="t.type" @click="wiz.deployTargets.push({ type: t.type, config: {} })">
                  {{ t.title }}
                </a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </div>
      </a-form>

      <div style="margin-top: 16px; text-align: right">
        <a-space>
          <a-button v-if="step > 0" @click="step--">上一步</a-button>
          <a-button v-if="step < 2" type="primary" @click="nextStep">下一步</a-button>
          <a-button v-else type="primary" :loading="saving" @click="saveWizard">提交任务</a-button>
        </a-space>
      </div>
    </a-modal>

    <!-- 快速创建 ACME 账户 -->
    <a-modal v-model:open="quickAcctOpen" title="快速创建 ACME 账户" :width="520" @ok="saveAcctThenUse">
      <a-form layout="vertical">
        <a-form-item label="邮箱" required><a-input v-model:value="quickAcct.email" placeholder="you@example.com" /></a-form-item>
        <a-form-item label="类型">
          <a-select v-model:value="quickAcct.type" :options="[{ label: 'Let\'s Encrypt 测试（staging，推荐先测试）', value: 'staging' }, { label: 'Let\'s Encrypt 生产', value: 'prod' }, { label: '自定义 ACME 目录', value: 'custom' }]" />
        </a-form-item>
        <a-form-item v-if="quickAcct.type === 'custom'" label="目录 URL"><a-input v-model:value="quickAcct.dirUrl" /></a-form-item>
      </a-form>
    </a-modal>

    <!-- DNS 凭证 -->
    <a-modal v-model:open="credOpen" title="添加 DNS 凭证" :width="560" @ok="saveCred">
      <a-form layout="vertical">
        <a-form-item label="凭证名称" required><a-input v-model:value="credForm.name" placeholder="我的 Cloudflare" /></a-form-item>
        <a-form-item label="DNS 提供商" required>
          <div class="provider-grid">
            <div v-for="p in providers" :key="p.name" class="provider-card" :class="{ active: credForm.provider === p.name }" @click="credForm.provider = p.name; credForm.config = {}">
              <div class="provider-title">{{ p.title }}</div>
              <div class="provider-name">{{ p.name }}</div>
            </div>
          </div>
        </a-form-item>
        <a-form-item v-for="f in providerDef(credForm.provider)?.fields || []" :key="f.key" :label="f.title" :help="f.help">
          <a-input-password v-if="f.type === 'password'" v-model:value="credForm.config[f.key]" />
          <a-input v-else v-model:value="credForm.config[f.key]" />
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- ACME 账户（账户与凭证 tab 用） -->
    <a-modal v-model:open="acctOpen" title="ACME 账户" @ok="saveAcct">
      <a-form layout="vertical">
        <a-form-item label="邮箱"><a-input v-model:value="editAcct.email" placeholder="you@example.com" /></a-form-item>
        <a-form-item label="类型">
          <a-select v-model:value="editAcct.type" :options="[{ label: 'Let\'s Encrypt 测试（staging）', value: 'staging' }, { label: 'Let\'s Encrypt 生产', value: 'prod' }, { label: '自定义 ACME 目录', value: 'custom' }]" />
        </a-form-item>
        <a-form-item v-if="editAcct.type === 'custom'" label="目录 URL"><a-input v-model:value="editAcct.dirUrl" /></a-form-item>
        <a-form-item label="私钥 PEM（留空自动生成）"><a-textarea v-model:value="editAcct.keyPem" :rows="4" /></a-form-item>
      </a-form>
    </a-modal>

    <!-- 证书内容查看 -->
    <a-modal v-model:open="certViewOpen" :title="certViewKind === 'key' ? '私钥（PEM）' : '证书（PEM）'" :footer="null" :width="760">
      <pre class="code-block" style="max-height: 420px">{{ certViewText }}</pre>
    </a-modal>

    <!-- 任务运行日志（certd 式抽屉：历史 + 节点日志） -->
    <a-drawer v-model:open="runsOpen" :title="'执行记录 · ' + (runsTask?.name || '')" :width="760">
      <a-table :data-source="taskRuns" :columns="drawerRunCols" size="small" row-key="id" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'">
            <a-tag :color="runColor(record.status)">{{ record.status }}</a-tag>
          </template>
          <template v-else-if="column.key === 'action'">
            <a-button size="small" @click="viewRun(record)">查看日志</a-button>
          </template>
        </template>
      </a-table>
      <div v-if="runLog" style="margin-top: 12px">
        <a-divider style="margin: 8px 0">节点日志 #{{ viewRunId }}</a-divider>
        <pre class="runlog">{{ runLog }}</pre>
      </div>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del, wsTopic, fmtTime } from '../api'
import { PlusOutlined } from '@ant-design/icons-vue'

const tab = ref('tasks')
const step = ref(0)
const wizOpen = ref(false)
const saving = ref(false)
const wiz = ref<any>({})

const infos = ref<any[]>([])
const tasks = ref<any[]>([])
const runs = ref<any[]>([])
const accounts = ref<any[]>([])
const credentials = ref<any[]>([])
const providers = ref<any[]>([])
const targets = ref<any[]>([])

const quickAcctOpen = ref(false)
const quickAcct = ref<any>({})
const credOpen = ref(false)
const credForm = ref<any>({})
const acctOpen = ref(false)
const editAcct = ref<any>({})

const runsOpen = ref(false)
const runsTask = ref<any>(null)
const taskRuns = ref<any[]>([])
const runLog = ref('')
const viewRunId = ref(0)
const certViewOpen = ref(false)
const certViewText = ref('')
const certViewKind = ref('cert')

const taskCols: any[] = [
  { title: '任务', dataIndex: 'name', key: 'name', width: 130 },
  { title: '域名', key: 'domains' },
  { title: '验证', key: 'challenge', width: 90 },
  { title: '证书状态', key: 'cert', width: 150 },
  { title: '部署目标', key: 'targets', width: 220 },
  { title: '续期', key: 'enabled', width: 90 },
  { title: '下次执行', key: 'nextRunAt', width: 150 },
  { title: '操作', key: 'action', width: 250 },
]
const infoCols = [
  { title: '域名', key: 'domains' },
  { title: '到期', dataIndex: 'notAfter', key: 'notAfter', width: 110, customRender: (o: any) => String(o.value).slice(0, 10) },
  { title: '剩余', key: 'daysLeft', width: 100 },
  { title: '状态', key: 'status', width: 80 },
  { title: '签发时间', dataIndex: 'issuedAt', key: 'issuedAt', width: 160, customRender: (o: any) => fmtTime(o.value) },
  { title: '操作', key: 'action', width: 140 },
]
const runCols = [
  { title: 'ID', dataIndex: 'id', key: 'id', width: 70 },
  { title: '任务', dataIndex: 'taskId', key: 'taskId', width: 70 },
  { title: '状态', key: 'status', width: 90 },
  { title: '开始', dataIndex: 'startedAt', key: 'startedAt', width: 160, customRender: (o: any) => fmtTime(o.value) },
  { title: '结束', dataIndex: 'finishedAt', key: 'finishedAt', width: 160, customRender: (o: any) => o.value ? fmtTime(o.value) : '-' },
  { title: '操作', key: 'action', width: 80 },
]
const drawerRunCols: any[] = [
  { title: 'ID', dataIndex: 'id', key: 'id', width: 60 },
  { title: '状态', key: 'status', width: 90 },
  { title: '开始', dataIndex: 'startedAt', key: 'startedAt', customRender: (o: any) => fmtTime(o.value) },
  { title: '操作', key: 'action', width: 100 },
]
const acctCols = [
  { title: '邮箱', dataIndex: 'email', key: 'email' },
  { title: '类型', dataIndex: 'type', key: 'type', width: 120 },
  { title: '操作', key: 'action', width: 100 },
]
const credCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '提供商', dataIndex: 'provider', key: 'provider' },
  { title: '操作', key: 'action', width: 100 },
]

const acctOpts = computed(() => accounts.value.map(a => ({ label: `${a.email}（${a.type}）`, value: a.id })))
const credOpts = computed(() => credentials.value.map(c => ({ label: `${c.name}（${c.provider}）`, value: c.id })))
const targetOpts = computed(() => targets.value.map(t => ({ label: t.title, value: t.type })))

const providerDef = (name: string) => providers.value.find(p => p.name === name)
const targetDef = (type: string) => targets.value.find(t => t.type === type)
const targetTitle = (type: string) => targets.value.find(t => t.type === type)?.title || type
const runColor = (s: string) => s === 'success' ? 'green' : s === 'error' ? 'red' : s === 'skip' ? 'orange' : 'blue'

const parseDomains = (s: string) => { try { return JSON.parse(s) } catch { return [s] } }
const daysLeft = (r: any) => Math.max(0, Math.floor((new Date(r.notAfter).getTime() - Date.now()) / 86400000))

const loadAll = async () => {
  infos.value = await get('/api/cert/infos')
  tasks.value = await get('/api/cert/tasks')
  runs.value = await get('/api/cert/runs')
  accounts.value = await get('/api/cert/accounts')
  credentials.value = await get('/api/cert/credentials')
  providers.value = await get('/api/cert/dns-providers')
  targets.value = await get('/api/cert/targets')
}

// ---- 向导 ----
const openWizard = (t?: any) => {
  if (t) {
    wiz.value = { ...t, deployTargets: JSON.parse(JSON.stringify(t.deployTargets || [])) }
  } else {
    wiz.value = {
      name: '', domains: [], challenge: 'http',
      acmeAccountId: accounts.value[0]?.id, dnsCredentialId: null,
      renewDays: 20, cronSpec: '0 3 * * *', enabled: true,
      deployTargets: [{ type: 'nginx_site', config: {} }],
    }
  }
  step.value = 0
  wizOpen.value = true
}

const nextStep = async () => {
  if (step.value === 0) {
    if (!wiz.value.name || !wiz.value.domains?.length) { message.warning('请填写任务名称与域名'); return }
    if (!wiz.value.acmeAccountId) { message.warning('请选择或快速创建 ACME 账户'); return }
  }
  if (step.value === 1 && wiz.value.challenge === 'dns' && !wiz.value.dnsCredentialId) {
    message.warning('DNS-01 需要选择 DNS 凭证'); return
  }
  step.value++
}

const saveWizard = async () => {
  saving.value = true
  try {
    await post('/api/cert/tasks', wiz.value)
    message.success('任务已保存，可点击「立即执行」签发，或等待 cron 自动续期')
    wizOpen.value = false
    loadAll()
  } finally { saving.value = false }
}

// ---- 任务操作 ----
const runTask = async (t: any) => {
  const r = await post(`/api/cert/tasks/${t.id}/run`, { force: false })
  if (r.error) message.error(`执行失败：${r.error}`)
  else message.success(`已提交（runId=${r.runId}）`)
  setTimeout(() => { loadAll(); if (runsOpen.value && runsTask.value?.id === t.id) loadTaskRuns(t) }, 1000)
  openRuns(t)
}
const delTask = async (t: any) => { await del(`/api/cert/tasks/${t.id}`); loadAll() }

// ---- 运行日志抽屉 ----
const openRuns = async (t: any) => {
  runsTask.value = t
  runsOpen.value = true
  runLog.value = ''
  await loadTaskRuns(t)
}
const loadTaskRuns = async (t: any) => {
  taskRuns.value = await get('/api/cert/runs', { taskId: t.id })
}
const viewRun = async (r: any) => {
  viewRunId.value = r.id
  const d = await get(`/api/cert/runs/${r.id}`)
  runLog.value = d.log || '(暂无日志)'
}

// ---- 账户 / 凭证 ----
const saveAcct = async (payload?: any) => {
  const r = await post('/api/cert/accounts', payload ?? editAcct.value)
  message.success('账户已保存')
  acctOpen.value = false
  loadAll()
  return r
}
const saveAcctThenUse = async () => {
  const r = await saveAcct(quickAcct.value)
  quickAcctOpen.value = false
  if (r?.id) wiz.value.acmeAccountId = r.id
}
const delAcct = async (a: any) => { await del(`/api/cert/accounts/${a.id}`); loadAll() }
const openCred = () => {
  credForm.value = { name: '', provider: providers.value[0]?.name, config: {} }
  credOpen.value = true
}
const saveCred = async () => {
  const r = await post('/api/cert/credentials', credForm.value)
  message.success('凭证已保存')
  credOpen.value = false
  loadAll()
  if (r?.id && wizOpen.value) wiz.value.dnsCredentialId = r.id
}
const delCred = async (c: any) => { await del(`/api/cert/credentials/${c.id}`); loadAll() }

// ---- 证书查看 ----
const viewCert = async (r: any, kind: string) => {
  certViewKind.value = kind
  certViewText.value = await get(`/api/cert/infos/${r.id}/file?kind=${kind}`)
  certViewOpen.value = true
}

let stopRunWs: (() => void) | null = null
onMounted(loadAll)

// 保活生命周期：激活重连证书签发日志流；切走时断开（签发进度通过周期任务与下次激活补偿）
onActivated(() => {
  loadAll()
  stopRunWs = wsTopic('cert-run', (m) => {
    if (m.type === 'log') {
      if (m.runId === viewRunId.value) runLog.value += m.line + '\n'
      if (runsOpen.value) loadTaskRuns(runsTask.value)
    }
  })
})
onDeactivated(() => { stopRunWs?.(); stopRunWs = null })
onUnmounted(() => stopRunWs?.())
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 16px; margin-bottom: 16px; }
.dim { color: #999; font-size: 12px; }
.mode-cards { display: flex; gap: 12px; }
.mode-card { flex: 1; border: 1.5px solid #e5e7eb; border-radius: 8px; padding: 14px; cursor: pointer; transition: all .2s; }
.mode-card.active { border-color: #4f7cff; background: #f0f5ff; }
.mode-title { font-weight: 600; margin-bottom: 6px; }
.mode-desc { font-size: 12px; color: #888; line-height: 1.6; }
.provider-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.provider-card { border: 1.5px solid #e5e7eb; border-radius: 8px; padding: 10px; cursor: pointer; text-align: center; transition: all .2s; }
.provider-card.active { border-color: #4f7cff; background: #f0f5ff; }
.provider-title { font-weight: 600; font-size: 13px; }
.provider-name { font-size: 11px; color: #999; }
.target-card { border: 1px dashed #d1d5db; border-radius: 8px; padding: 12px; margin-bottom: 10px; }
.target-head { display: flex; justify-content: space-between; margin-bottom: 10px; }
.runlog { max-height: 420px; overflow: auto; background: #111; color: #0f0; padding: 12px; font-size: 12px; white-space: pre-wrap; }
</style>
