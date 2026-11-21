<template>
  <a-spin :spinning="loading">
  <a-card size="small">
    <a-row :gutter="12">
      <a-col :span="12"><div ref="cpuBox" class="chart-box"></div></a-col>
      <a-col :span="12"><div ref="memBox" class="chart-box"></div></a-col>
    </a-row>
    <a-row :gutter="12" style="margin-top: 8px">
      <a-col :span="12"><div ref="netBox" class="chart-box"></div></a-col>
      <a-col :span="12"><div ref="loadBox" class="chart-box"></div></a-col>
    </a-row>

    <a-card title="磁盘" size="small" style="margin-top: 12px">
      <a-descriptions v-if="(snap.disks || []).length" :column="2" size="small">
        <a-descriptions-item v-for="d in snap.disks || []" :key="d.mount" :label="d.mount">
          {{ fmtBytes(d.used) }} / {{ fmtBytes(d.total) }}（{{ d.usedPct?.toFixed(1) }}%）
          <a-progress :percent="d.usedPct" size="small" :show-info="false" style="width: 160px" :stroke-color="d.usedPct > 90 ? '#ef4444' : '#10b981'" />
        </a-descriptions-item>
      </a-descriptions>
      <a-empty v-else :image-style="{ height: '48px' }" description="暂无磁盘数据" style="padding: 12px 0" />
    </a-card>
  </a-card>
  </a-spin>
</template>

<script setup lang="ts">
import { onActivated, onMounted, onUnmounted, ref } from 'vue'
import { get, wsTopic, fmtBytes } from '../api'
import { useVisibleConn } from '../composables/usePolling'

const snap = ref<any>({})
const loading = ref(true)
const cpuBox = ref<HTMLElement | null>(null)
const memBox = ref<HTMLElement | null>(null)
const netBox = ref<HTMLElement | null>(null)
const loadBox = ref<HTMLElement | null>(null)
let charts: any[] = []
let resizeObserver: ResizeObserver | null = null
const series = { cpu: [] as any[], mem: [] as any[], net: [] as any[], load: [] as any[] }

const mkOption = (title: string, data: any[], formatter?: (v: any) => string, max?: number) => ({
  title: { text: title, textStyle: { fontSize: 13 } },
  grid: { left: 55, right: 20, top: 35, bottom: 25 },
  xAxis: { type: 'time', axisLabel: { fontSize: 10, formatter: (v: number) => new Date(v).toLocaleTimeString('zh-CN', { hour12: false }) } },
  yAxis: { type: 'value', max, axisLabel: { formatter: (v: number) => (formatter ? formatter(v) : String(v)) } },
  series: [{ type: 'line', data, showSymbol: false, smooth: true, areaStyle: { opacity: 0.15 } }],
  tooltip: { trigger: 'axis' },
})

const push = (arr: any[], v: any) => { arr.push(v); if (arr.length > 120) arr.shift() }

const update = (m: any) => {
  push(series.cpu, [new Date(m.time).getTime(), +(m.cpu || 0).toFixed(1)])
  push(series.mem, [new Date(m.time).getTime(), +(m.memPct || 0).toFixed(1)])
  push(series.net, [new Date(m.time).getTime(), +( (m.netRateOut || 0) / 1024 ).toFixed(1)], )
  series.net[series.net.length - 1] = [new Date(m.time).getTime(), +((m.netRateOut || 0) / 1024).toFixed(1)]
  push(series.load, [new Date(m.time).getTime(), +(m.load1 || 0).toFixed(2)])
  snap.value = m
  if (charts.length === 4) {
    charts[0].setOption(mkOption('CPU %', series.cpu, undefined, 100))
    charts[1].setOption(mkOption('内存 %', series.mem, undefined, 100))
    charts[2].setOption(mkOption('网络 KB/s（上行）', series.net, undefined))
    charts[3].setOption(mkOption('负载 Load1', series.load, undefined))
  }
}

onMounted(async () => {
  const echarts = await import('echarts')
  charts = [cpuBox.value, memBox.value, netBox.value, loadBox.value].map(el => echarts.init(el!))
  // 容器真实尺寸变化才 resize（ResizeObserver），替代旧版每 3s 强制重排的定时器。
  // keep-alive 切走瞬间容器 0 尺寸：忽略（否则画布缩成 0×0），切回后由 onActivated 恢复
  resizeObserver = new ResizeObserver(() => {
    const box = cpuBox.value
    if (box && box.clientWidth > 0) charts.forEach(c => c.resize())
  })
  ;[cpuBox.value, memBox.value, netBox.value, loadBox.value].forEach(el => el && resizeObserver!.observe(el))
  // 历史采样回填（60s 一条 → 近 30 分钟）
  try {
    const hist = await get<any[]>('/api/metrics/history', { hours: 1 })
    for (const h of hist || []) {
      const t = new Date(h.time).getTime()
      series.cpu.push([t, +h.cpu?.toFixed?.(1) || h.cpu])
      series.mem.push([t, h.memTotal ? +((h.memUsed / h.memTotal) * 100).toFixed(1) : 0])
      series.net.push([t, 0])
      series.load.push([t, +h.load1?.toFixed?.(2) || h.load1])
    }
  } catch { /* ignore */ }
  const cur = await get('/api/metrics/current')
  update(cur)
  loading.value = false
})

// 实时流：keep-alive 切走或标签页隐藏时断开（后端订阅数归零即停采样），恢复时重连
useVisibleConn(() => wsTopic('metrics', (m) => update(m)))

// 保活切回：RO 在 DOM 脱离/回归文档期间可能不触发（离开时已按 0 尺寸收缩），
// 双 rAF 等布局完成后再恢复一次画布尺寸
onActivated(() => {
  requestAnimationFrame(() => requestAnimationFrame(() => {
    const box = cpuBox.value
    if (box && box.clientWidth > 0) charts.forEach(c => c.resize())
  }))
})
onUnmounted(() => {
  resizeObserver?.disconnect(); resizeObserver = null
  charts.forEach(c => c?.dispose()); charts = []
})
</script>

<style scoped>
.chart-box { height: min(260px, 30vh); }
</style>
