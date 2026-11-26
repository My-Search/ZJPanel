<template>
  <a-spin :spinning="loading">
    <div class="sec-page">
      <a-alert
        v-if="snap.level === 'critical'"
        type="error" show-icon banner class="banner"
        :message="`当前站点处于高风险状态：健康分 ${snap.health}/100，近 60 秒有 ${snap.active} 个严重事件`"
        description="先看「攻击事件」定位来源，再决定封 IP 还是封地区；封禁前建议先点一次预演。"
      />
      <a-alert
        v-else-if="snap.level === 'warn'"
        type="warning" show-icon banner class="banner"
        :message="`检测到异常流量：健康分 ${snap.health}/100`"
      />

      <!-- 顶部指标 -->
      <a-row :gutter="12">
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">健康分</div>
            <div class="stat-value" :style="{ color: healthColor }">{{ snap.health ?? 100 }}<span class="unit">/100</span></div>
            <div class="stat-sub">{{ levelText }}</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">实时 RPS</div>
            <div class="stat-value">{{ fmtNum(snap.rps) }}<span class="unit">req/s</span></div>
            <div class="stat-sub">基线 {{ fmtNum(snap.baselineRps) }} req/s</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">4xx / 5xx（60s）</div>
            <div class="stat-value">
              {{ fmtNum(snap.status?.['4xx']) }}<span class="unit">/</span><span :style="{ color: (snap.status?.['5xx'] || 0) > 0 ? '#ef4444' : undefined }">{{ fmtNum(snap.status?.['5xx']) }}</span>
            </div>
            <div class="stat-sub">错误率 {{ snap.errRate ?? 0 }}%</div>
          </a-card>
        </a-col>
        <a-col :xs="12" :md="6">
          <a-card size="small" class="stat">
            <div class="stat-label">L4 连接 / 半连接</div>
            <div class="stat-value">{{ fmtNum(l4.conn) }}<span class="unit">/</span><span :style="{ color: (l4.synRecv || 0) > 500 ? '#ef4444' : undefined }">{{ fmtNum(l4.synRecv) }}</span></div>
            <div class="stat-sub">{{ l4.supported ? `入向 ${fmtBps(l4.rxBps)}` : '仅 Linux 容器可用' }}</div>
          </a-card>
        </a-col>
      </a-row>

      <a-row :gutter="12">
        <a-col :xs="24" :lg="16">
          <a-card size="small" title="实时请求速率（近 2 分钟）">
            <div ref="rpsBox" class="chart"></div>
          </a-card>
        </a-col>
        <a-col :xs="24" :lg="8">
          <a-card size="small" title="内核层观测" style="height: 100%">
            <a-descriptions :column="1" size="small">
              <a-descriptions-item label="ESTABLISHED">{{ fmtNum(l4.conn) }}</a-descriptions-item>
              <a-descriptions-item label="SYN_RECV">{{ fmtNum(l4.synRecv) }}</a-descriptions-item>
              <a-descriptions-item label="TIME_WAIT">{{ fmtNum(l4.timeWait) }}</a-descriptions-item>
              <a-descriptions-item label="入向带宽">{{ fmtBps(l4.rxBps) }}</a-descriptions-item>
              <a-descriptions-item label="accept 队列溢出">{{ fmtNum(l4.overflows) }}</a-descriptions-item>
            </a-descriptions>
            <div class="hint">日志只能看见 HTTP 攻击；SYN Flood / 连接打满这类攻击靠这一层兜底。</div>
          </a-card>
        </a-col>
      </a-row>

      <a-row :gutter="12">
        <a-col :xs="24" :lg="14">
          <a-card size="small" title="攻击事件（实时）" :body-style="{ padding: '0 8px 8px' }">
            <a-table
              :data-source="snap.events || []" :columns="eventCols" row-key="id"
              size="small" :pagination="false" :scroll="{ y: 260 }"
              :locale="{ emptyText: '暂无事件，一切正常' }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'time'">{{ fmtTime(record.time) }}</template>
                <template v-else-if="column.key === 'severity'">
                  <a-tag :color="record.severity === 'critical' ? 'red' : record.severity === 'warn' ? 'orange' : 'blue'">
                    {{ record.severity === 'critical' ? '严重' : record.severity === 'warn' ? '警告' : '提示' }}
                  </a-tag>
                </template>
                <template v-else-if="column.key === 'kind'">{{ record.kind }}</template>
                <template v-else-if="column.key === 'source'">
                  <div v-if="record.site" class="mono" :title="record.site">{{ record.site }}</div>
                  <div v-if="record.ip">
                    <span class="mono">{{ record.ip }}</span>
                    <a-tag v-if="record.country" class="cc">{{ record.country }}</a-tag>
                  </div>
                  <span v-if="!record.site && !record.ip && !record.country" class="dim">-</span>
                </template>
                <template v-else-if="column.key === 'detail'">
                  <span class="detail">{{ record.detail }}</span>
                  <a v-if="record.ip" class="mini-act" @click="askBlockIp(record.ip, record.kind)">封禁</a>
                </template>
              </template>
            </a-table>
          </a-card>
        </a-col>
        <a-col :xs="24" :lg="10">
          <a-card size="small" title="来源地区 Top（60s）" :body-style="{ padding: '0 8px 8px' }">
            <div v-if="!snap.geoLoaded" class="hint" style="padding: 2px 4px 6px">
              GeoIP 库未加载：地区统计为空、无法按国家封禁，可在「请求地图」页上传 GeoLite2-Country.mmdb
            </div>
            <a-table
              :data-source="snap.topCountries || []" :columns="countryCols" row-key="name"
              size="small" :pagination="false" :scroll="{ y: 260 }"
              :locale="{ emptyText: '暂无数据' }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'name'">
                  <span class="mono">{{ record.name }}</span>
                </template>
                <template v-else-if="column.key === 'count'">{{ fmtNum(record.count) }} <span class="pct">{{ record.pct }}%</span></template>
                <template v-else-if="column.key === 'act'">
                  <a-button size="small" danger type="text" :disabled="!snap.geoLoaded" @click="askBlockCountry(record.name)">封禁</a-button>
                </template>
              </template>
            </a-table>
          </a-card>
        </a-col>
      </a-row>

      <a-row :gutter="12">
        <a-col :xs="24" :lg="12">
          <a-card size="small" title="手动处置">
            <a-space direction="vertical" style="width: 100%">
              <a-input-group compact>
                <a-input v-model:value="ipInput" placeholder="IP 或 CIDR，如 1.2.3.4 / 1.2.3.0/24" style="width: calc(100% - 180px)" />
                <a-select v-model:value="ipMinutes" style="width: 90px">
                  <a-select-option :value="60">60 分钟</a-select-option>
                  <a-select-option :value="360">6 小时</a-select-option>
                  <a-select-option :value="1440">1 天</a-select-option>
                  <a-select-option :value="0">永久</a-select-option>
                </a-select>
                <a-button type="primary" danger @click="askBlockIp(ipInput, '手动')" style="width: 90px">封禁</a-button>
              </a-input-group>
              <div class="hint">封禁前会自动跑 5 分钟预演，展示该来源的正常请求占比，避免误伤。</div>
            </a-space>
          </a-card>
        </a-col>
        <a-col :xs="24" :lg="12">
          <a-card size="small" title="当前封禁">
            <div v-if="!blockedRows.length" class="hint">暂无封禁项</div>
            <a-table
              v-else :data-source="blockedRows" :columns="blockCols" row-key="key"
              size="small" :pagination="false" :scroll="{ y: 160 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'kind'">
                  <a-tag :color="record.kind === 'country' ? 'blue' : 'default'">{{ record.kind === 'country' ? '地区' : 'IP' }}</a-tag>
                </template>
                <template v-else-if="column.key === 'until'">{{ record.until ? fmtTime(record.until) : '永久' }}</template>
                <template v-else-if="column.key === 'act'">
                  <a-button size="small" type="link" @click="unblock(record)">解封</a-button>
                </template>
              </template>
            </a-table>
          </a-card>
        </a-col>
      </a-row>

      <a-card size="small" title="防护策略">
        <a-form layout="vertical">
          <a-row :gutter="16">
            <a-col :xs="24" :md="5">
              <a-form-item label="启用处置">
                <a-switch v-model:checked="pol.enabled" />
                <div class="hint">关闭后只观测，不改动 nginx</div>
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="5">
              <a-form-item label="观察模式">
                <a-switch v-model:checked="pol.observe" />
                <div class="hint">只记录判定结果，不真正拦截</div>
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="5">
              <a-form-item label="自动封禁（P2）">
                <a-switch v-model:checked="pol.autoBan" />
                <div class="hint">扫描/爆破超阈值自动封 60 分钟</div>
              </a-form-item>
            </a-col>
            <a-col :xs="24" :md="4">
              <a-form-item label="CC 限速">
                <a-switch v-model:checked="pol.ccEnabled" />
                <div class="hint">nginx limit_req</div>
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="2">
              <a-form-item label="速率 r/s">
                <a-input-number v-model:value="pol.ccRate" :min="1" :max="2000" style="width: 100%" />
              </a-form-item>
            </a-col>
            <a-col :xs="12" :md="3">
              <a-form-item label="突发 burst">
                <a-input-number v-model:value="pol.ccBurst" :min="0" :max="2000" style="width: 100%" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-form-item label="IP 白名单（每行一个，支持 CIDR；本机 127.0.0.0/8 内置，白名单来源不参与攻击判定、永不被封）">
            <a-textarea v-model:value="whitelistText" :rows="3" placeholder="127.0.0.1&#10;192.168.0.0/16" />
          </a-form-item>
          <a-space>
            <a-button type="primary" :loading="saving" @click="savePolicy">保存并下发</a-button>
            <span class="hint">保存会写入 nginx 并 reload；测试失败自动回滚，不会把线上配置打坏。</span>
          </a-space>
        </a-form>
      </a-card>
    </div>
  </a-spin>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { get, post, put, wsTopic, fmtBytes } from '../api'

const snap = ref<any>({ status: {}, events: [], topCountries: [], topIps: [], l4: {}, policy: {} })
const loading = ref(true)
const saving = ref(false)
const ipInput = ref('')
const ipMinutes = ref(60)
const whitelistText = ref('')
const rpsBox = ref<HTMLElement | null>(null)

const pol = reactive<any>({
  enabled: true, observe: false, autoBan: false,
  ccEnabled: false, ccRate: 20, ccBurst: 30, status: 444,
  blockIps: [], blockCountries: [],
})

const l4 = computed(() => snap.value.l4 || {})

const healthColor = computed(() => {
  const h = snap.value.health ?? 100
  return h >= 80 ? '#10b981' : h >= 50 ? '#f59e0b' : '#ef4444'
})
const levelText = computed(() => {
  const l = snap.value.level
  return l === 'critical' ? '高风险' : l === 'warn' ? '有异常' : '正常'
})

const eventCols = [
  { title: '时间', key: 'time', width: 86 },
  { title: '级别', key: 'severity', width: 76 },
  { title: '类型', key: 'kind', width: 96 },
  { title: '来源', key: 'source', width: 150 },
  { title: '详情', key: 'detail', ellipsis: true },
]
const countryCols = [
  { title: '地区', key: 'name', width: 90 },
  { title: '请求', key: 'count', ellipsis: true },
  { title: '', key: 'act', width: 64 },
]
const blockCols = [
  { title: '类型', key: 'kind', width: 72 },
  { title: '值', dataIndex: 'value', ellipsis: true },
  { title: '截至', key: 'until', width: 90 },
  { title: '', key: 'act', width: 60 },
]

const blockedRows = computed(() => {
  const rows: any[] = []
  const norm = (t: any) => (t && !String(t).startsWith('0001') ? t : '')
  for (const b of pol.blockIps || []) rows.push({ key: 'ip:' + b.value, kind: 'ip', value: b.value, until: norm(b.until), reason: b.reason })
  for (const b of pol.blockCountries || []) rows.push({ key: 'cc:' + b.value, kind: 'country', value: b.value, until: norm(b.until), reason: b.reason })
  return rows
})

// ---- 图表 ----
let chart: any = null
let ro: ResizeObserver | null = null
const series: any[] = [[], []]

const updateChart = () => {
  if (!chart) return
  chart.setOption({
    grid: { left: 44, right: 16, top: 16, bottom: 24 },
    xAxis: { type: 'time', axisLabel: { fontSize: 10, formatter: (v: number) => new Date(v).toLocaleTimeString('zh-CN', { hour12: false }) } },
    yAxis: { type: 'value', axisLabel: { fontSize: 10 } },
    series: [
      { name: 'RPS', type: 'line', data: series[0], showSymbol: false, smooth: true, areaStyle: { opacity: 0.12 }, lineStyle: { color: '#3b82f6' }, itemStyle: { color: '#3b82f6' } },
      { name: '基线', type: 'line', data: series[1], showSymbol: false, smooth: true, lineStyle: { color: '#94a3b8', type: 'dashed', width: 1 }, itemStyle: { color: '#94a3b8' } },
    ],
    tooltip: { trigger: 'axis' },
    legend: { show: false },
  })
}

const pushPoint = (rps: number, base: number) => {
  const t = Date.now()
  series[0].push([t, +rps || 0])
  series[1].push([t, +base || 0])
  for (const s of series) if (s.length > 120) s.shift()
  updateChart()
}

let lastPolicySync = 0
const onSnap = (m: any) => {
  if (!m || !m.time) return
  snap.value = m
  pushPoint(m.rps, m.baselineRps)
  // 自动封禁/到期回收都发生在后端，快照里的计数变了就同步一次完整策略（限 3s 一次）
  const pv = m.policy || {}
  const want = (pv.blockedIps || 0) + (pv.blockedCountries || 0)
  const have = (pol.blockIps?.length || 0) + (pol.blockCountries?.length || 0)
  if (want !== have && Date.now() - lastPolicySync > 3000) {
    lastPolicySync = Date.now()
    refreshPolicy().catch(() => { /* 下一轮快照再试 */ })
  }
}

// ---- 数据加载 ----
const refreshPolicy = async () => {
  const p = await get<any>('/api/security/policy')
  Object.assign(pol, p)
  whitelistText.value = (p.whitelist || []).join('\n')
}

let unsub: (() => void) | null = null

onMounted(async () => {
  const echarts = await import('echarts')
  if (rpsBox.value) {
    chart = echarts.init(rpsBox.value)
    ro = new ResizeObserver(() => {
      if (rpsBox.value && rpsBox.value.clientWidth > 0) chart?.resize()
    })
    ro.observe(rpsBox.value)
  }
  try {
    await refreshPolicy()
    const first = await get<any>('/api/security/overview')
    if (first?.time) onSnap(first)
  } catch { /* 首屏失败由 ws 兜底 */ }
  loading.value = false
  unsub = wsTopic('security', onSnap)
})

onUnmounted(() => {
  unsub?.()
  ro?.disconnect()
  chart?.dispose()
  chart = null
})

// ---- 处置动作 ----
const askBlockIp = async (ip: string, reason: string) => {
  if (!ip) { message.warning('请先填写 IP 或 CIDR'); return }
  let warn = ''
  try {
    const r = await post<any>('/api/security/dryrun', { kind: 'ip', value: ip })
    warn = r.warning || ''
  } catch (e: any) {
    message.error(e?.response?.data?.error || '预演失败'); return
  }
  Modal.confirm({
    title: `封禁 ${ip}？`,
    content: `${warn}。时长 ${ipMinutes.value ? ipMinutes.value + ' 分钟' : '永久'}，可在「当前封禁」一键解封。`,
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/ip', { value: ip, minutes: ipMinutes.value, reason })
      message.success('已封禁并下发')
      ipInput.value = ''
      await refreshPolicy()
    },
  })
}

const askBlockCountry = async (code: string) => {
  let r: any = {}
  try {
    r = await post<any>('/api/security/dryrun', { kind: 'country', value: code })
  } catch (e: any) {
    message.error(e?.response?.data?.error || '预演失败'); return
  }
  Modal.confirm({
    title: `封禁地区 ${code}？`,
    content: `近 5 分钟 ${r.requests || 0} 次请求，正常请求占 ${(r.okPct || 0).toFixed(1)}%，命中 ${r.matchedCidrs || 0} 条 CIDR。${r.warning || ''}`,
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/country', { value: code, minutes: 60, reason: '手动封禁地区' })
      message.success(`已封禁 ${code}，60 分钟后自动解封`)
      await refreshPolicy()
    },
  })
}

const unblock = async (row: any) => {
  const url = row.kind === 'country' ? '/api/security/unblock/country' : '/api/security/unblock/ip'
  await post(url, { value: row.value })
  message.success('已解封')
  await refreshPolicy()
}

const savePolicy = async () => {
  saving.value = true
  try {
    const wl = whitelistText.value.split('\n').map(s => s.trim()).filter(Boolean)
    await put('/api/security/policy', { ...pol, whitelist: wl })
    message.success('策略已下发')
    await refreshPolicy()
  } catch (e: any) {
    message.error(e?.response?.data?.error || '下发失败')
  } finally {
    saving.value = false
  }
}

// ---- 格式化 ----
const fmtNum = (v: any) => (v === undefined || v === null ? 0 : Number(v) >= 1000 ? (Number(v) / 1000).toFixed(1) + 'k' : Number(v))
const fmtTime = (t: any) => (t ? new Date(t).toLocaleTimeString('zh-CN', { hour12: false }) : '-')
const fmtBps = (v: any) => fmtBytes(Math.round(Number(v) || 0)) + '/s'
</script>

<style scoped>
.sec-page { display: flex; flex-direction: column; gap: 12px; }
.banner { margin-bottom: 0; }
.stat { height: 96px; }
.stat-label { color: var(--color-slate); font-size: 12px; }
.stat-value { font-size: 26px; font-weight: 600; line-height: 1.25; color: var(--color-ink); }
.unit { font-size: 12px; font-weight: 400; color: var(--color-slate); margin-left: 4px; }
.stat-sub { color: var(--color-slate); font-size: 12px; }
.chart { height: 240px; }
.hint { color: var(--color-slate); font-size: 12px; margin-top: 4px; }
.dim { color: var(--color-slate); font-size: 12px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.cc { margin-left: 4px; }
.pct { color: var(--color-slate); font-size: 12px; }
.detail { margin-right: 8px; }
.mini-act { font-size: 12px; }
</style>
