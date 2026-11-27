<template>
  <div class="hosts-page">
    <!-- 左：主机列表 -->
    <a-card size="small" class="list-card" :body-style="{ padding: '8px' }">
      <div class="list-head">
        <a-input v-model:value="kw" size="small" placeholder="搜索主机 / 地址 / 分组" allow-clear />
        <a-dropdown>
          <a-button type="primary" block class="add-host-btn">
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
            <div class="host-addr mono">{{ h.local ? '面板所在机器' : (h.host ? `${h.username}@${h.host}:${h.port}` : '待回连，地址未上报') }}</div>
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
              {{ current.local ? '127.0.0.1（面板容器）' : (current.host ? `${current.username}@${current.host}:${current.port}` : '等待 Agent 回连后上报地址') }}
              <span v-if="current.remoteIp"> · 回连 IP {{ current.remoteIp }}</span>
              <span v-if="current.lastSeen"> · 最后在线 {{ fmtTime(current.lastSeen) }}</span>
            </div>
          </div>
          <a-space wrap class="dh-actions">
            <a-button size="small" v-if="!current.local" @click="testHost" :loading="testing"><ApiOutlined /> 测试连接</a-button>
            <a-button size="small" type="primary" ghost @click="openTerminal"><CodeSandboxOutlined /> 打开终端</a-button>
            <a-button v-if="canInstall" size="small" type="primary" :loading="installing" @click="installAgent">安装 Agent</a-button>
            <a-button v-if="current.online && !current.local" size="small" @click="upgradeAgent" :loading="upgrading"><ReloadOutlined /> 升级 Agent</a-button>
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
              <div v-if="!current.local" class="overview-meta">
                每 5 秒自动刷新 · 指标采样 {{ m.t ? fmtTime(m.t) : '等待上报' }}
              </div>

              <a-row :gutter="12" style="margin-top: 12px">
                <a-col :xs="24" :lg="14">
                  <a-card size="small" title="监听端口" :body-style="{ padding: '0 8px 8px' }">
                    <a-table
                      :data-source="ports" :columns="portCols" row-key="key" size="small"
                      :scroll="narrow ? undefined : { x: 380 }"
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
            <div class="pane defense-pane">
              <a-alert
                v-if="!current.online || current.local" type="warning" show-icon
                :message="current.local ? '内置主机不运行 Agent，无法执行主机防御检查。' : `主机防御需要 Agent 在线${current.lastSeen ? '（最后心跳 ' + fmtTime(current.lastSeen) + '）' : ''}；请检查 Agent 服务或等待回连。`"
              >
                <template v-if="!current.local" #action>
                  <a-button size="small" :loading="agentChecking" @click="refreshAgentStatus">刷新状态</a-button>
                </template>
              </a-alert>
              <a-alert
                v-else-if="current.outdated" type="warning" show-icon
                :message="`Agent 版本落后（${current.agentVersion || '-'} → ${current.panelVersion}），防御检查与封禁需先升级 Agent。`"
                style="margin-bottom: 12px"
              >
                <template #action>
                  <a-button size="small" type="primary" ghost :loading="upgrading" @click="upgradeAgent"><ReloadOutlined /> 升级 Agent</a-button>
                </template>
              </a-alert>

              <div class="defense-head">
                <div>
                  <div class="defense-title">主机对外防御</div>
                  <div class="hint">只读基线审计 + nftables 主机层封禁；Agent 在线时每 6 小时自动检查，结果保存在面板，刷新页面不会丢。</div>
                </div>
                <a-space wrap>
                  <a-button :disabled="!canCheck" :loading="runningAll" @click="runAllChecks">全部检查</a-button>
                  <a-button type="primary" :disabled="!canCheck" :loading="fwLoading" @click="refreshFirewall">刷新防火墙</a-button>
                </a-space>
              </div>

              <a-row :gutter="[12, 12]" class="def-grid">
                <a-col v-for="d in checkDefs" :key="d.kind" :xs="24" :sm="12" :lg="8">
                  <div class="def-card" :class="'risk-' + (riskOf(d.kind) || 'none')">
                    <div class="def-card-top">
                      <span class="def-name">{{ d.title }}</span>
                      <a-tag v-if="checks[d.kind].error" color="red">检查失败</a-tag>
                      <a-tag v-else-if="checks[d.kind].at" :color="riskColor(riskOf(d.kind))">
                        {{ riskText(riskOf(d.kind)) }}
                      </a-tag>
                      <a-tag v-else>未检查</a-tag>
                    </div>
                    <div class="def-desc">{{ d.desc }}</div>
                    <div class="def-foot">
                      <span class="hint" :title="checks[d.kind].error">
                        {{ checks[d.kind].error || (checks[d.kind].at ? fmtTime(checks[d.kind].at) : '尚未运行') }}
                      </span>
                      <a-button
                        size="small" type="link" :disabled="!canCheck" :loading="checks[d.kind].loading"
                        @click="runCheck(d.kind)"
                      >检查</a-button>
                    </div>
                  </div>
                </a-col>
              </a-row>

              <a-card size="small" class="def-fw" title="主机层封禁">
                <template #extra>
                  <a-tag :color="fw?.active ? 'green' : 'orange'">
                    {{ fwText }}
                  </a-tag>
                </template>
                <div class="fw-bar">
                  <span class="fw-dot" :class="fw?.active ? 'on' : 'off'" />
                  <span class="fw-meta">{{ fwMeta }}</span>
                  <span class="hint">已封禁 {{ bans.length }} 个 IP</span>
                </div>

                <div class="ban-form">
                  <a-input v-model:value="banIp" placeholder="IPv4 地址，如 203.0.113.10" class="ban-input" allow-clear />
                  <a-select v-model:value="banTtl" :options="ttlOptions" class="ban-ttl" />
                  <a-button danger :disabled="!canCheck || !banIp" :loading="banning" @click="doBan">封禁</a-button>
                </div>

                <a-table
                  v-if="bans.length" :data-source="bans" :columns="banCols" row-key="ip" size="small"
                  :pagination="false" class="ban-table" :scroll="{ x: 360 }" style="margin-top: 4px"
                >
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'ip'"><span class="mono">{{ record.ip }}</span></template>
                    <template v-else-if="column.key === 'expires'">{{ fmtTtl(record.expires) || '到期时间未知' }}</template>
                    <template v-else-if="column.key === 'act'">
                      <a-button size="small" type="link" danger @click="doUnban(record.ip)">解封</a-button>
                    </template>
                  </template>
                </a-table>
                <div v-else class="hint" style="margin-top: 8px">暂无封禁。封禁写入独立表 inet zjpanel，不改动系统已有防火墙规则。</div>

                <div class="hint" style="margin-top: 8px">
                  封禁立即生效并自动过期。请谨慎封禁面板或 SSH 来源 IP，误封可能切断 Agent 回连；封禁记录可在「事件」追溯。
                </div>
              </a-card>

              <a-card size="small" class="def-result" title="检查结果">
                <template #extra>
                  <a-radio-group v-model:value="findFilter" size="small" button-style="solid" class="def-filter">
                    <a-radio-button value="">全部</a-radio-button>
                    <a-radio-button v-for="d in checkDefs" :key="d.kind" :value="d.kind">{{ d.short }}</a-radio-button>
                  </a-radio-group>
                </template>
                <a-table
                  v-if="visibleFindings.length" :data-source="visibleFindings" :columns="defCols"
                  row-key="_key" size="small" :pagination="{ pageSize: 8, size: 'small' }" :scroll="{ x: 640 }"
                >
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'kind'">
                      <span class="hint">{{ record._title }}</span>
                    </template>
                    <template v-else-if="column.key === 'risk'">
                      <a-tag :color="riskColor(record.risk)">{{ riskText(record.risk) }}</a-tag>
                    </template>
                    <template v-else-if="column.key === 'value'">
                      <span class="mono">{{ record.value || '-' }}</span>
                    </template>
                  </template>
                </a-table>
                <a-empty v-else :image="simpleImage" description="还没有检查结果，点上方「检查」或「全部检查」" />
              </a-card>
            </div>
          </a-tab-pane>

          <a-tab-pane key="events" tab="事件">
            <div class="pane">
              <a-table
                :data-source="events" :columns="eventCols" row-key="id" size="small"
                :pagination="false" :scroll="{ x: 560 }" :locale="{ emptyText: '暂无事件' }"
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
            <div class="pane term-pane">
              <HostTerminal
                :host-id="current.local ? null : current.id"
                :local="current.local"
                :name="current.name"
                :active="tab === 'terminal'"
              />
            </div>
          </a-tab-pane>

          <a-tab-pane key="settings" tab="设置">
            <div class="pane">
              <a-form layout="vertical" style="max-width: 720px">
                <a-row :gutter="12">
                  <a-col :xs="24" :sm="12"><a-form-item label="名称"><a-input v-model:value="edit.name" /></a-form-item></a-col>
                  <a-col :xs="24" :sm="12"><a-form-item label="分组"><a-input v-model:value="edit.group" placeholder="如 生产 / 边缘" /></a-form-item></a-col>
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
    <a-modal v-model:open="addOpen" title="SSH 添加主机" :width="narrow ? '94vw' : 520" :confirm-loading="addSaving" @ok="saveNewHost" ok-text="保存">
      <a-form layout="vertical">
        <a-row :gutter="12">
          <a-col :xs="24" :sm="12"><a-form-item label="名称"><a-input v-model:value="form.name" placeholder="可留空，默认用地址" /></a-form-item></a-col>
          <a-col :xs="24" :sm="12"><a-form-item label="分组"><a-input v-model:value="form.group" placeholder="可留空" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :span="16"><a-form-item label="地址" required><a-input v-model:value="form.host" placeholder="1.2.3.4 或域名" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="端口"><a-input-number v-model:value="form.port" :min="1" :max="65535" style="width: 100%" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="12">
          <a-col :xs="24" :sm="12"><a-form-item label="用户" required><a-input v-model:value="form.username" placeholder="root" /></a-form-item></a-col>
          <a-col :xs="24" :sm="12">
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
    <a-modal v-model:open="regOpen" title="命令安装 Agent" :footer="null" :width="narrow ? '94vw' : 720">
      <a-form layout="vertical">
        <a-row :gutter="12">
          <a-col :xs="24" :sm="12"><a-form-item label="名称（可留空）"><a-input v-model:value="reg.name" /></a-form-item></a-col>
          <a-col :xs="24" :sm="12"><a-form-item label="分组（可留空）"><a-input v-model:value="reg.group" /></a-form-item></a-col>
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
    <a-modal v-model:open="outOpen" title="安装输出" :footer="null" :width="narrow ? '94vw' : 760">
      <pre class="out-box mono">{{ output }}</pre>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { Empty, message } from 'ant-design-vue'
import { PlusOutlined, ApiOutlined, CodeSandboxOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import HostTerminal from '../components/HostTerminal.vue'
import { get, getQuiet, post, del, wsTopic, fmtBytes, fmtTime } from '../api'

const simpleImage = Empty.PRESENTED_IMAGE_SIMPLE

const hosts = ref<any[]>([])
const current = ref<any>(null)
const kw = ref('')
const tab = ref('overview')
// 刷新/从菜单返回时回到上次的主机与标签：终端会话按主机常驻，恢复后直接 attach
const HOST_KEY = 'zj.hosts.current'
const TAB_KEY = 'zj.hosts.tab'
const TABS = ['overview', 'defense', 'events', 'terminal', 'settings']
const m = ref<any>({})
const ports = ref<any[]>([])
const events = ref<any[]>([])
const installing = ref(false)
const upgrading = ref(false)
const testing = ref(false)
const saving = ref(false)
const output = ref('')
const outOpen = ref(false)

// 防御：五类只读基线 + 主机层封禁
const checkDefs = [
  { kind: 'ssh', title: 'SSH 基线', short: 'SSH', desc: 'root 登录、密码登录、重试次数、空密码' },
  { kind: 'accounts', title: '账号基线', short: '账号', desc: 'UID 0、空密码、sudo 免密、密钥权限' },
  { kind: 'persistence', title: '持久化点', short: '持久化', desc: 'cron、systemd、rc.local、ld.so.preload' },
  { kind: 'exposure', title: '暴露面', short: '暴露面', desc: '公网监听端口、高危端口、防火墙状态' },
  { kind: 'webshell', title: 'Webshell 扫描', short: '后门', desc: '常见一句话后门与命令执行特征' },
]
const checks = reactive<any>(Object.fromEntries(checkDefs.map(d => [d.kind, { loading: false, findings: [] as any[], at: '', error: '' }])))
const findFilter = ref('')
const runningAll = ref(false)
const fwLoading = ref(false)
const fw = ref<any>(null)
const banIp = ref('')
const banTtl = ref(3600)
const banning = ref(false)
const ttlOptions = [
  { label: '1 小时', value: 3600 },
  { label: '6 小时', value: 6 * 3600 },
  { label: '1 天', value: 24 * 3600 },
  { label: '7 天', value: 7 * 24 * 3600 },
]

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
const canCheck = computed(() => !!(current.value && current.value.online && !current.value.local))

const allFindings = computed(() => checkDefs.flatMap(d =>
  (checks[d.kind].findings || []).map((f: any, i: number) => ({ ...f, _kind: d.kind, _title: d.short, _key: `${d.kind}-${i}-${f.key}` })),
))
const visibleFindings = computed(() => findFilter.value ? allFindings.value.filter((f: any) => f._kind === findFilter.value) : allFindings.value)

const riskOf = (kind: string) => {
  const c = checks[kind]
  if (!c?.at || c.error) return ''
  const fs = c.findings || []
  if (fs.some((f: any) => f.risk === 'critical')) return 'critical'
  if (fs.some((f: any) => f.risk === 'warn')) return 'warn'
  // 检查成功且没有发现项 = 正常；不能因为 findings 为空就退回「未检查」
  return 'ok'
}
const riskText = (r: string) => r === 'critical' ? '高危' : r === 'warn' ? '建议' : r === 'ok' ? '正常' : '未检查'
const riskColor = (r: string) => r === 'critical' ? 'red' : r === 'warn' ? 'orange' : r === 'ok' ? 'green' : 'default'
// nft 输出形如 3599s，转成人类可读的剩余时间
const fmtTtl = (v: any) => {
  const m = /^(\d+)s$/.exec(String(v || ''))
  if (!m) return String(v || '')
  const sec = Number(m[1])
  if (sec >= 86400) return `${Math.round(sec / 86400)} 天`
  if (sec >= 3600) return `${Math.round(sec / 3600)} 小时`
  if (sec >= 60) return `${Math.round(sec / 60)} 分钟`
  return `${sec} 秒`
}
const fwText = computed(() => {
  if (!fw.value) return '未获取'
  if (!fw.value.active) return '未启用'
  return fw.value.backend === 'none' ? '未检测到' : '已启用'
})
const fwMeta = computed(() => {
  if (!fw.value) return current.value?.online ? '正在获取防火墙状态…' : 'Agent 离线，无法获取'
  if (fw.value.backend === 'none') return '未检测到 nftables / ufw / firewalld / iptables 规则'
  return `${fw.value.backend} · ${fw.value.rules || 0} 条规则 · ${fw.value.active ? '生效中' : '未生效'}`
})
const bans = computed(() => fw.value?.bans || [])

const statusClass = (h: any) => h.local ? 'dot-local' : h.online ? 'dot-on' : h.agentStatus === 'pending' ? 'dot-pending' : h.agentStatus === 'uninstalled' ? 'dot-off' : 'dot-off'
const statusText = (h: any) => h.local ? '内置' : h.agentStatus === 'local' ? '内置' : h.online ? '在线' : h.agentStatus === 'pending' ? '待回连' : h.agentStatus === 'uninstalled' ? '已卸载' : h.agentStatus === 'none' || !h.agentStatus ? '未安装' : '离线'
const statusColor = (h: any) => h.local ? 'blue' : h.online ? 'green' : h.agentStatus === 'pending' ? 'orange' : 'default'

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
const defCols = [
  { title: '类型', key: 'kind', width: 76 },
  { title: '配置项', dataIndex: 'key', key: 'key', width: 170 },
  { title: '当前值', dataIndex: 'value', key: 'value', width: 220, ellipsis: true },
  { title: '结论', key: 'risk', width: 76 },
  { title: '建议', dataIndex: 'advice', key: 'advice', ellipsis: true },
]
const banCols = [
  { title: 'IP', dataIndex: 'ip', key: 'ip', width: 160 },
  { title: '剩余时间', dataIndex: 'expires', key: 'expires', width: 130 },
  { title: '操作', key: 'act', width: 70 },
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
  if (!current.value && list.length) {
    const savedId = Number(localStorage.getItem(HOST_KEY))
    const saved = list.find((x: any) => x.id === savedId)
    select(saved || list[0])
    const savedTab = localStorage.getItem(TAB_KEY) || ''
    if (saved && TABS.includes(savedTab)) tab.value = savedTab
  } else if (current.value) {
    const h = list.find(x => x.id === current.value.id)
    if (h) current.value = h
  }
}

const agentChecking = ref(false)
// 手动刷新一次主机列表与指标，避免防御页停留在旧的在线态
const refreshAgentStatus = async () => {
  agentChecking.value = true
  try {
    await load()
    await loadMetrics()
    if (current.value) message.success(current.value.online ? 'Agent 在线' : 'Agent 仍未回连')
  } catch (e: any) {
    message.error(e?.message || '刷新失败')
  } finally {
    agentChecking.value = false
  }
}

const resetDefense = () => {
  checkDefs.forEach(d => {
    checks[d.kind].findings = []; checks[d.kind].at = ''; checks[d.kind].error = ''; checks[d.kind].loading = false
  })
  findFilter.value = ''
  fw.value = null
  banIp.value = ''
}

// 读取后端落库的最近检查结果：刷新页面/切换主机后仍显示上次状态。
const loadChecks = async (hostId = current.value?.id) => {
  if (!hostId || current.value?.local) return
  try {
    const r = await getQuiet<any>(`/api/hosts/${hostId}/checks`)
    if (current.value?.id !== hostId) return
    checkDefs.forEach(d => {
      const c = r?.[d.kind]
      checks[d.kind].at = c?.checkedAt || ''
      checks[d.kind].error = c?.error || ''
      checks[d.kind].findings = c?.result?.findings || []
    })
  } catch {
    // 读取失败不阻塞页面；保留当前内存态，下一轮切换/刷新再试。
  }
}
const select = async (h: any) => {
  current.value = h
  localStorage.setItem(HOST_KEY, String(h.id))
  tab.value = 'overview'
  edit.name = h.name
  edit.group = h.group || ''
  resetDefense()
  await loadDetail()
  if (!h.local) loadChecks(h.id)
}

// 概览轮询：只刷新指标/端口/在线态，不清空已有数据，避免每次刷新闪一下
const loadMetrics = async () => {
  const c = current.value
  if (!c || c.local) return
  const hostId = c.id
  try {
    const r = await getQuiet<any>(`/api/hosts/${hostId}/metrics`)
    if (current.value?.id !== hostId) return
    m.value = r.metrics || {}
    ports.value = (r.metrics?.ports || []).map((p: any) => ({ ...p, key: `${p.proto}-${p.port}` }))
  } catch { /* 离线/瞬时失败正常 */ }
}

const loadEvents = async () => {
  const c = current.value
  if (!c) return
  try { events.value = await getQuiet(`/api/hosts/${c.id}/events?limit=50`) } catch { /* ignore */ }
}

const loadDetail = async () => {
  if (!current.value) return
  m.value = {}
  ports.value = []
  events.value = []
  await Promise.all([loadMetrics(), loadEvents()])
}

// 主机页停留期间定时刷新：Agent 15s 一次心跳，5s 轮询能及时反映状态与指标
let pollTimer: any = null
let pollBusy = false
const pollNow = async () => {
  if (document.hidden || pollBusy) return
  pollBusy = true
  try {
    try { await load() } catch { /* 网络抖动不影响下一轮 */ }
    await loadMetrics()
    // 防御页跟随轮询拉取最近检查：后台定时检查出结果后无需手动刷新页面
    if (tab.value === 'defense') await loadChecks()
  } finally { pollBusy = false }
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

const runCheck = async (kind: string, silent = false, hostId = current.value?.id) => {
  if (!canCheck.value || !hostId) return false
  const def = checkDefs.find(d => d.kind === kind)
  checks[kind].loading = true
  try {
    const r = await post<any>(`/api/hosts/${hostId}/check/${kind}`)
    // 检查期间切了主机：结果丢弃，避免写到新主机名下
    if (current.value?.id !== hostId) return false
    checks[kind].findings = r.result?.findings || []
    checks[kind].at = new Date().toISOString()
    checks[kind].error = ''
    if (!silent) message.success(`${def?.title || kind}检查完成`)
    return true
  } catch (e: any) {
    const msg = e?.response?.data?.error || `${def?.title || kind}检查失败`
    if (current.value?.id === hostId) {
      checks[kind].at = new Date().toISOString()
      checks[kind].error = msg
      checks[kind].findings = []
    }
    message.error(msg)
    return false
  } finally { if (current.value?.id === hostId) checks[kind].loading = false }
}

const runAllChecks = async () => {
  if (!canCheck.value || !current.value) return
  const hostId = current.value.id
  runningAll.value = true
  let okCount = 0
  try {
    for (const d of checkDefs) {
      if (current.value?.id !== hostId) break
      if (await runCheck(d.kind, true, hostId)) okCount++
    }
    if (current.value?.id === hostId) {
      message[okCount === checkDefs.length ? 'success' : 'warning'](`完成 ${okCount}/${checkDefs.length} 项检查`)
    }
  } finally { runningAll.value = false }
}

const refreshFirewall = async () => {
  if (!current.value) return
  const hostId = current.value.id
  fwLoading.value = true
  try {
    const r = await getQuiet<any>(`/api/hosts/${hostId}/firewall`)
    if (current.value?.id === hostId) fw.value = r
  } catch (e: any) {
    message.error(e?.response?.data?.error || '获取防火墙状态失败')
  } finally { fwLoading.value = false }
}

const doBan = async () => {
  const ip = banIp.value.trim()
  if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) { message.warning('请输入合法 IPv4 地址'); return }
  banning.value = true
  try {
    const r = await post<any>(`/api/hosts/${current.value.id}/bans`, { ip, seconds: banTtl.value })
    message.success(r.output || `已封禁 ${ip}`)
    banIp.value = ''
    await refreshFirewall()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '封禁失败')
  } finally { banning.value = false }
}

const doUnban = async (ip: string) => {
  try {
    const r = await del<any>(`/api/hosts/${current.value.id}/bans/${ip}`)
    message.success(r.output || `已解封 ${ip}`)
    await refreshFirewall()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '解封失败')
  }
}

// 详情页的「打开终端」直接切到内嵌终端 Tab；多标签/刷新恢复走 Tab 内的「多标签终端」入口
const openTerminal = () => { tab.value = 'terminal' }

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
// 移动端（<768）与桌面共用一套模板：只把弹窗宽度、列表形态和留白降级。
const narrow = ref(false)
const mq = window.matchMedia('(max-width: 767px)')
const syncNarrow = () => { narrow.value = mq.matches }

onMounted(async () => {
  syncNarrow()
  mq.addEventListener('change', syncNarrow)
  await load()
  // 主机列表与详情数据不会自己推送（只有事件会），页面停留时靠轮询保持更新
  pollTimer = setInterval(pollNow, 5000)
  unsub = wsTopic('host-event', () => { load(); loadMetrics(); loadEvents() })
})
onUnmounted(() => {
  unsub?.()
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  mq.removeEventListener('change', syncNarrow)
})
watch(() => current.value?.id, () => loadDetail())
watch(tab, (v) => {
  localStorage.setItem(TAB_KEY, v)
  if (v === 'defense') {
    loadChecks()
    if (canCheck.value) refreshFirewall()
  }
})
</script>

<style scoped>
.hosts-page { display: flex; gap: 12px; height: 100%; min-height: 0; }
.list-card { width: 300px; flex: none; display: flex; flex-direction: column; }
.list-card :deep(.ant-card-body) { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.list-head { flex: none; }
.add-host-btn { margin-top: 10px; height: 40px; font-size: 14px; font-weight: 500; border-radius: 8px; }
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
.detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 12px 16px 12px; flex-wrap: wrap; }
.dh-title { font-size: 16px; font-weight: 600; color: var(--color-ink); }
.dh-sub { font-size: 12px; color: var(--color-slate); margin-top: 2px; }
.detail-tabs { flex: 1; min-height: 0; padding: 0 16px; display: flex; flex-direction: column; }
.detail-tabs :deep(.ant-tabs-nav) { margin-bottom: 12px; }
/* 让 tabs 内容区成为可伸缩容器，终端 tab 才能填满卡片剩余高度（而不是固定 62vh 后留白） */
.detail-tabs :deep(.ant-tabs-content-holder) {
  overflow-y: auto; display: flex; flex-direction: column; min-height: 0;
}
.detail-tabs :deep(.ant-tabs-content) { flex: 1; min-height: 0; }
.detail-tabs :deep(.ant-tabs-tabpane) { height: 100%; }
.pane { padding: 4px 2px 16px; }
.term-pane {
  height: 100%; min-height: clamp(380px, 62vh, 760px); box-sizing: border-box;
  display: flex; padding: 2px 2px 12px;
}
.term-pane > * { flex: 1; min-height: 0; min-width: 0; }
.stat { height: 86px; }
.stat-label { font-size: 12px; color: var(--color-slate); }
.stat-value { font-size: 24px; font-weight: 600; line-height: 1.25; color: var(--color-ink); }
.unit { font-size: 12px; font-weight: 400; color: var(--color-slate); margin-left: 3px; }
.stat-sub { font-size: 12px; color: var(--color-slate); }
.overview-meta { margin-top: 8px; font-size: 12px; color: var(--color-slate); }
.hint { font-size: 12px; color: var(--color-slate); margin-top: 2px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.defense-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin: 2px 0 12px; }
.defense-title { font-size: 15px; font-weight: 600; color: var(--color-ink); }
.def-grid { margin-bottom: 4px; }
.def-card {
  height: 100%; position: relative; overflow: hidden;
  display: flex; flex-direction: column; gap: 6px;
  padding: 10px 12px 8px; border-radius: 10px;
  border: 1px solid rgba(148, 163, 184, .22);
  background: rgba(148, 163, 184, .05);
  min-height: 108px;
}
.def-card::before { content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 3px; background: var(--color-slate); }
.def-card.risk-critical::before { background: #ef4444; }
.def-card.risk-warn::before { background: #f59e0b; }
.def-card.risk-ok::before { background: #10b981; }
.def-card-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.def-name { font-size: 13px; font-weight: 600; color: var(--color-ink); }
.def-desc { font-size: 12px; line-height: 1.5; color: var(--color-slate); min-height: 36px; }
.def-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: auto; }
.def-foot .hint { margin-top: 0; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.def-result { margin-top: 12px; }
.def-fw { margin-top: 12px; }
.fw-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12px; color: var(--color-ink); }
.fw-dot { width: 7px; height: 7px; border-radius: 50%; background: #f59e0b; box-shadow: 0 0 0 3px rgba(245, 158, 11, .14); }
.fw-dot.on { background: #10b981; box-shadow: 0 0 0 3px rgba(16, 185, 129, .14); }
.fw-meta { font-size: 12px; }
.ban-form { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 10px; }
.ban-input { width: min(260px, 100%); }
.ban-ttl { width: 120px; }
.ban-table { max-width: 560px; }
.cmd-box { margin-top: 12px; padding: 10px; border-radius: 8px; background: rgba(15,23,42,.06); font-size: 12px; word-break: break-all; }
.out-box { max-height: 60vh; overflow: auto; padding: 10px; border-radius: 8px; background: rgba(15,23,42,.06); font-size: 12px; white-space: pre-wrap; }
@media (max-width: 900px) {
  .hosts-page { flex-direction: column; height: auto; gap: 8px; }
  .list-card { width: 100%; max-height: none; flex: none; }
  /* 列表降级为横向滑动的「主机胶囊」：不占纵向空间，也不会把详情挤到屏幕外 */
  .list-body { display: flex; gap: 8px; overflow-x: auto; overflow-y: hidden; padding-bottom: 4px; margin-top: 8px; }
  .list-body :deep(.ant-empty) { margin: 4px auto; }
  .host-item { flex: 0 0 auto; max-width: 70vw; padding: 6px 10px; border: 1px solid rgba(148,163,184,.18); background: rgba(148,163,184,.06); }
  .host-item.active { border-color: rgba(22,119,255,.35); }
  .host-addr { display: none; }
  .detail-card { width: 100%; min-width: 0; min-height: 0; overflow: hidden; }
  .detail-head { flex-direction: column; align-items: stretch; gap: 8px; padding: 12px 12px 0; }
  .dh-title { font-size: 15px; line-height: 1.5; overflow-wrap: anywhere; }
  .dh-sub { font-size: 11px; overflow-wrap: anywhere; }
  .dh-actions { width: 100%; flex-wrap: wrap; }
  .detail-tabs { padding: 0 12px; }
  /* 窄屏详情卡不再有固定高度：终端回落到视口高度，避免被 flex 高度链拉成长条 */
  .term-pane { height: clamp(360px, 62vh, 640px); min-height: 0; }
  .defense-head { flex-direction: column; align-items: stretch; }
  .def-desc { min-height: 0; }
  .def-filter { margin-top: 4px; }
  .pane { padding-bottom: 12px; }
}
@media (max-width: 767px) {
  .stat { height: auto; min-height: 76px; }
  .stat-value { font-size: 20px; }
  .cmd-box, .out-box { font-size: 11px; }
  .detail-head { padding-top: 10px; }
  .detail-tabs :deep(.ant-tabs-nav) { margin-bottom: 8px; }
  .detail-tabs :deep(.ant-tabs-nav-list) { flex-wrap: nowrap; }
  .detail-tabs :deep(.ant-tabs-tab) { padding: 8px 0; font-size: 13px; }
}
</style>
