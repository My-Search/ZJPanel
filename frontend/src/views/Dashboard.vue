<template>
  <div>
    <a-row :gutter="16">
      <a-col :span="6"><a-card size="small"><a-statistic title="CPU" :value="snap.cpu ?? 0" :precision="1" suffix="%" /></a-card></a-col>
      <a-col :span="6"><a-card size="small"><a-statistic title="内存" :value="snap.memPct ?? 0" :precision="1" suffix="%" /><div class="sub">{{ fmtBytes(snap.memUsed) }} / {{ fmtBytes(snap.memTotal) }}</div></a-card></a-col>
      <a-col :span="6"><a-card size="small"><a-statistic title="网络速率" :value="fmtBytes(snap.netRateOut ?? 0) + ' ↑ / ' + fmtBytes(snap.netRateIn ?? 0) + ' ↓'" /></a-card></a-col>
      <a-col :span="6"><a-card size="small"><a-statistic title="磁盘" :value="mainDiskPct" :precision="1" suffix="%" /><div class="sub">{{ mainDiskLabel }}</div></a-card></a-col>
    </a-row>

    <a-row :gutter="16" style="margin-top: 16px">
      <a-col :span="24">
        <a-card title="请求地图" size="small">
          <template #extra><router-link to="/map" class="map-link">完整地图 →</router-link></template>
          <RequestMap compact :height="360" />
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="16" style="margin-top: 16px">
      <a-col :span="14">
        <a-card title="站点状态" size="small">
          <template #extra><a-tag :color="nginxRunning ? 'green' : 'red'">Nginx {{ nginxRunning ? '运行中' : '未运行' }}</a-tag></template>
          <a-table :data-source="sites" :columns="siteCols" size="small" :pagination="false" row-key="domain" :scroll="{ x: 520 }">
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
        <a-card title="系统信息" size="small" style="margin-bottom: 16px">
          <p><b>主机名：</b>{{ snap.hostname || '-' }}</p>
          <p><b>系统：</b>{{ snap.os || '-' }}</p>
          <p><b>负载：</b>{{ snap.load1?.toFixed(2) }} / {{ snap.load5?.toFixed(2) }} / {{ snap.load15?.toFixed(2) }}</p>
          <p><b>磁盘：</b>{{ disksSummary || '-' }}</p>
          <p><b>运行时间：</b>{{ uptimeText }}</p>
          <p><b>面板版本：</b>{{ version }}</p>
        </a-card>
        <a-card title="快捷入口" size="small">
          <a-space direction="vertical">
            <router-link to="/compose">从 docker-compose 目录导入网站 →</router-link>
            <router-link to="/certs">申请/管理证书（自动续期）→</router-link>
            <router-link to="/map">查看请求地图流向 →</router-link>
          </a-space>
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { get, wsTopic, fmtBytes } from '../api'
import RequestMap from '../components/RequestMap.vue'

const snap = ref<any>({})
const sites = ref<any[]>([])
const nginxRunning = ref(false)
const version = ref('')
let stopWs: (() => void) | null = null
let timer: any = null

const uptimeText = computed(() => {
  const s = snap.value?.uptime || 0
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return `${d}天 ${h}时 ${m}分`
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

const loadAll = async () => {
  try {
    sites.value = await get('/api/sites')
    nginxRunning.value = (await get<any>('/api/nginx/status')).running
    version.value = (await get<any>('/api/update/current')).version
  } catch { /* 拦截器统一提示，保留上次数据 */ }
}

onMounted(async () => {
  await loadAll()
})

// 保活生命周期：激活重连实时指标流；切走时暂停（周期数据由下次激活 loadAll 补偿）
onActivated(() => {
  loadAll()
  stopWs = wsTopic('metrics', (m) => { snap.value = m })
  timer = setInterval(loadAll, 15000)
})
onDeactivated(() => { stopWs?.(); stopWs = null; clearInterval(timer) })
onUnmounted(() => { stopWs?.(); clearInterval(timer) })
</script>

<style scoped>
.sub { color: #999; font-size: 12px; }
.map-link { font-size: 12px; }
</style>
