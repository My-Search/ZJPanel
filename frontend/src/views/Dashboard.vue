<template>
  <a-spin :spinning="loading">
  <div class="dash">
    <a-row :gutter="24">
      <a-col :span="6">
        <a-card size="small" class="stat-card">
          <div class="stat-top" :title="`CPU ${(snap.cpu ?? 0).toFixed(1)}%`">
            <div class="vbar" role="meter" :aria-valuenow="Math.round(snap.cpu ?? 0)" aria-valuemin="0" aria-valuemax="100" :aria-label="`CPU 占用 ${Math.round(snap.cpu ?? 0)}%`"><div class="vfill" :class="level(snap.cpu)" :style="{ height: (snap.cpu ?? 0) + '%' }"></div></div>
            <div>
              <div class="stat-label">CPU占用</div>
              <div class="stat-value" :class="level(snap.cpu)">{{ Math.round(snap.cpu ?? 0) }}<span class="unit">%</span></div>
            </div>
          </div>
          <div class="stat-sub">负载 {{ snap.load1?.toFixed(2) || '0.00' }}</div>
        </a-card>
      </a-col>
      <a-col :span="6">
        <a-card size="small" class="stat-card">
          <div class="stat-top" :title="`内存 ${(snap.memPct ?? 0).toFixed(1)}% ${snap.memTxt || ''}`">
            <div class="vbar" role="meter" :aria-valuenow="Math.round(snap.memPct ?? 0)" aria-valuemin="0" aria-valuemax="100" :aria-label="`内存占用 ${Math.round(snap.memPct ?? 0)}%`"><div class="vfill" :class="level(snap.memPct)" :style="{ height: (snap.memPct ?? 0) + '%' }"></div></div>
            <div>
              <div class="stat-label">系统内存占用</div>
              <div class="stat-value" :class="level(snap.memPct)">{{ Math.round(snap.memPct ?? 0) }}<span class="unit">%</span></div>
            </div>
          </div>
          <div class="stat-sub">{{ fmtBytes(snap.memUsed) }} / {{ fmtBytes(snap.memTotal) }}</div>
        </a-card>
      </a-col>
      <a-col :span="6">
        <a-card size="small" class="stat-card">
          <div class="stat-top">
            <div class="vbar"><div class="vfill net"></div></div>
            <div>
              <div class="stat-label">网络速率</div>
              <div class="stat-value">{{ netRate.v }}<span class="unit">{{ netRate.u }}</span></div>
            </div>
          </div>
          <div class="stat-sub">↑ {{ fmtBytes(snap.netRateOut ?? 0) }} ↓ {{ fmtBytes(snap.netRateIn ?? 0) }}</div>
        </a-card>
      </a-col>
      <a-col :span="6">
        <a-card size="small" class="stat-card">
          <div class="stat-top" :title="disksSummary">
            <div class="vbar" role="meter" :aria-valuenow="Math.round(mainDiskPct)" aria-valuemin="0" aria-valuemax="100" :aria-label="`磁盘占用 ${Math.round(mainDiskPct)}%`"><div class="vfill" :class="level(mainDiskPct)" :style="{ height: mainDiskPct + '%' }"></div></div>
            <div>
              <div class="stat-label">磁盘占用</div>
              <div class="stat-value" :class="level(mainDiskPct)">{{ Math.round(mainDiskPct) }}<span class="unit">%</span></div>
            </div>
          </div>
          <div class="stat-sub">{{ mainDiskLabel }}</div>
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="24" class="map-row">
      <a-col :span="24">
        <a-card title="请求地图" size="small" class="map-card">
          <template #extra><router-link to="/map" class="map-link">完整地图 →</router-link></template>
          <RequestMap compact fill :height="360" />
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="24" class="bottom-row">
      <a-col :span="14">
        <a-card title="站点状态" size="small">
          <template #extra><a-tag :color="nginxRunning ? 'green' : 'red'">Nginx {{ nginxRunning ? '运行中' : '未运行' }}</a-tag></template>
          <a-alert v-if="sitesErr" type="error" :message="sitesErr" show-icon banner style="margin-bottom: 8px" />
          <a-table :data-source="sortedSites" :columns="siteCols" size="small" :pagination="false" row-key="domain">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'status'">
                <a-tag :color="record.check?.status === 'online' ? 'green' : record.check?.status === 'offline' ? 'red' : 'orange'">
                  {{ record.check?.status || '检测中' }}
                </a-tag>
              </template>
              <template v-else-if="column.key === 'latency'">
                {{ record.check?.latencyMs != null ? record.check.latencyMs + ' ms' : '-' }}
              </template>
              <template v-else-if="column.key === 'cert'">
                {{ record.check?.certDaysLeft ? '剩 ' + record.check.certDaysLeft + ' 天' : '-' }}
              </template>
              <template v-else-if="column.key === 'maint'">
                <a-tag v-if="record.maintenance" color="orange">维护中</a-tag>
                <a-tag v-else :color="record.enabled === false ? 'default' : 'blue'">{{ record.enabled === false ? '已停用' : '运行' }}</a-tag>
              </template>
            </template>
          </a-table>
        </a-card>
      </a-col>
      <a-col :span="10">
        <a-card title="系统信息" size="small" class="sys-card">
          <a-descriptions :column="1" size="small">
            <a-descriptions-item label="主机名">{{ snap.hostname || '-' }}</a-descriptions-item>
            <a-descriptions-item label="系统">{{ snap.os || '-' }}</a-descriptions-item>
            <a-descriptions-item label="负载">{{ snap.load1?.toFixed(2) }} / {{ snap.load5?.toFixed(2) }} / {{ snap.load15?.toFixed(2) }}</a-descriptions-item>
            <a-descriptions-item label="磁盘">{{ disksSummary || '-' }}</a-descriptions-item>
            <a-descriptions-item label="运行时间">{{ uptimeText }}</a-descriptions-item>
            <a-descriptions-item label="面板版本">{{ version }}</a-descriptions-item>
          </a-descriptions>
        </a-card>
        <a-card title="快捷入口" size="small" class="quick-card">
          <a-space direction="vertical">
            <router-link to="/compose"><ClusterOutlined /> 从 docker-compose 目录导入网站 →</router-link>
            <router-link to="/certs"><SafetyCertificateOutlined /> 申请/管理证书（自动续期）→</router-link>
            <router-link to="/map"><GlobalOutlined /> 查看请求地图流向 →</router-link>
          </a-space>
        </a-card>
      </a-col>
    </a-row>
  </div>
  </a-spin>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onUnmounted, ref } from 'vue'
import { getQuiet, wsTopic, fmtBytes } from '../api'
import { sortSites } from '../utils/siteOrder'
import { usePolling } from '../composables/usePolling'
import RequestMap from '../components/RequestMap.vue'
import { ClusterOutlined, SafetyCertificateOutlined, GlobalOutlined } from '@ant-design/icons-vue'

const snap = ref<any>({})
const sites = ref<any[]>([])
const sitesErr = ref('')
const nginxRunning = ref(false)
const version = ref('')
const loading = ref(true)

const uptimeText = computed(() => {
  const s = snap.value?.uptime || 0
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return `${d}天 ${h}时 ${m}分`
})

// 竖条与数值同色分级：≥85% 红、≥70% 黄，其余绿
const level = (v?: number) => {
  const p = v ?? 0
  return p >= 85 ? 'high' : p >= 70 ? 'warn' : 'ok'
}
// 网络速率拆成 数字 + 单位，大号展示（上下行合计）
const netRate = computed(() => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = (snap.value.netRateOut ?? 0) + (snap.value.netRateIn ?? 0), i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return { v: v.toFixed(i === 0 ? 0 : 1), u: units[i] + '/s' }
})

// 磁盘：统计卡取第一个真实分区（通常即系统盘），明细进系统信息卡
const disks = computed<any[]>(() => snap.value?.disks || [])
const mainDiskPct = computed(() => disks.value[0]?.usedPct ?? 0)
const mainDiskLabel = computed(() => {
  const d = disks.value[0]
  if (!d) return '采集中'
  const mount = d.mount === '/' ? '系统盘' : d.mount
  return `${mount} ${fmtBytes(d.used)} / ${fmtBytes(d.total)}`
})
const disksSummary = computed(() =>
  disks.value.map(d => `${d.mount} ${(d.usedPct ?? 0).toFixed(1)}%`).join('，') || ''
)

const siteCols = [
  { title: '域名', dataIndex: 'domain', key: 'domain' },
  { title: '状态', key: 'status', width: 90 },
  { title: '延迟', key: 'latency', width: 90 },
  { title: '证书', key: 'cert', width: 100 },
  { title: '面板状态', key: 'maint', width: 100 },
]

// 站点状态排序：与「全部网站」共用 utils/siteOrder（维护中 > 异常 > online > 已停用）
const sortedSites = computed(() => sortSites(sites.value))

const loadAll = async () => {
  // 三个接口互不阻塞：站点列表失败（例如历史库脏值）时仍要拿到版本与 Nginx 状态，
  // 否则用户看不到当前面板版本，排查部署是否生效会被连带卡住。
  const [sitesRes, nginxRes, verRes] = await Promise.allSettled([
    getQuiet('/api/sites'),
    getQuiet<any>('/api/nginx/status'),
    getQuiet<any>('/api/update/current'),
  ])
  if (sitesRes.status === 'fulfilled') {
    sites.value = sitesRes.value
    sitesErr.value = ''
  } else {
    const e: any = sitesRes.reason
    sitesErr.value = '站点列表加载失败：' + (e?.response?.data?.error || e?.message || '未知错误')
  }
  if (nginxRes.status === 'fulfilled') nginxRunning.value = nginxRes.value.running
  if (verRes.status === 'fulfilled') version.value = verRes.value.version
  loading.value = false
}

// 轮询统一管理：keep-alive 切走暂停，切回/标签恢复时立即补一次加载。
// 首次加载由 onActivated 触发（Dashboard 在 MainLayout 的 keep-alive 缓存内，
// 挂载后必触发激活）——不再像旧版那样 mounted+activated 各拉一遍造成首屏双请求。
usePolling(loadAll, 15000, { immediate: false })

// 实时指标流：挂载/激活即连，切走断开（v1.0.6 语义）。
// 不做「标签页隐藏断开」——可见性时序不可控（冻结/丢弃/恢复），影响实时数据可靠性。
let stopWs: (() => void) | null = null
onActivated(() => { stopWs = wsTopic('metrics', (m) => { snap.value = m }) })
onDeactivated(() => { stopWs?.(); stopWs = null })
onUnmounted(() => { stopWs?.() })
</script>

<style scoped>
.map-link { font-size: 12px; }
/* 单屏布局：地图卡吃掉剩余空间，统计/底部行按内容取自然高。
   卡片一律不出内部滚动条（子滚动条多余）：空间不足时交给内容区整体滚动兜底。 */
.dash { height: 100%; display: flex; flex-direction: column; gap: 24px; }
.map-row { flex: 1 1 auto; min-height: min(420px, 55vh); }
.map-row > :deep(.ant-col) { display: flex; flex-direction: column; min-height: 0; }
.map-card { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
.map-card :deep(.ant-card-body) { flex: 1 1 auto; min-height: 0; }
/* 底部行不参与压缩（0 0 auto）：否则会被地图卡的保底高度挤瘪 */
.bottom-row { flex: 0 0 auto; }
.sys-card { margin-bottom: 16px; }
/* 统计卡：等高；左侧细竖条表示占用进度，右侧标签 + 大号加粗数值 */
.stat-card { height: 100%; }
.stat-card :deep(.ant-card-body) { height: 100%; box-sizing: border-box; }
.stat-top { display: flex; align-items: stretch; gap: 12px; }
.vbar {
  flex: 0 0 4px; width: 4px; border-radius: 2px;
  background: rgba(148, 163, 184, .25); position: relative; overflow: hidden;
}
.vfill { position: absolute; left: 0; bottom: 0; width: 100%; border-radius: 2px; transition: height .5s ease; }
.vfill.ok { background: #22c55e; }
.vfill.warn { background: #f59e0b; }
.vfill.high { background: #ef4444; }
.vfill.net { background: #0ea5e9; height: 100%; }
.stat-label { font-size: 13px; color: #475569; line-height: 1.5; white-space: nowrap; }
.stat-value {
  margin-top: 2px; font-size: 28px; font-weight: 700; line-height: 1.15;
  letter-spacing: -.5px; color: #0f172a; user-select: none;
}
.stat-value.warn { color: #f59e0b; }
.stat-value.high { color: #ef4444; }
.stat-value .unit { font-size: 13px; font-weight: 600; letter-spacing: 0; opacity: .55; margin-left: 3px; }
.stat-sub { margin-top: 6px; padding-left: 16px; color: #64748b; font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
html[data-theme='dark'] .stat-label { color: #9ca3af; }
html[data-theme='dark'] .stat-value { color: #f3f4f6; }
html[data-theme='dark'] .stat-sub { color: #9ca3af; }
</style>
