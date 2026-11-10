<template>
  <a-card size="small">
    <a-row :gutter="12">
      <a-col :span="12"><div ref="cpuBox" style="height: 260px"></div></a-col>
      <a-col :span="12"><div ref="memBox" style="height: 260px"></div></a-col>
    </a-row>
    <a-row :gutter="12" style="margin-top: 8px">
      <a-col :span="12"><div ref="netBox" style="height: 260px"></div></a-col>
      <a-col :span="12"><div ref="loadBox" style="height: 260px"></div></a-col>
    </a-row>

    <a-card title="磁盘" size="small" style="margin-top: 12px">
      <a-descriptions :column="2" size="small">
        <a-descriptions-item v-for="d in snap.disks || []" :key="d.mount" :label="d.mount">
          {{ fmtBytes(d.used) }} / {{ fmtBytes(d.total) }}（{{ d.usedPct?.toFixed(1) }}%）
          <a-progress :percent="d.usedPct" size="small" :show-info="false" style="width: 160px" :stroke-color="d.usedPct > 90 ? '#f5222d' : '#52c41a'" />
        </a-descriptions-item>
      </a-descriptions>
    </a-card>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { get, wsTopic, fmtBytes } from '../api'

const snap = ref<any>({})
const cpuBox = ref<HTMLElement | null>(null)
const memBox = ref<HTMLElement | null>(null)
const netBox = ref<HTMLElement | null>(null)
const loadBox = ref<HTMLElement | null>(null)
let charts: any[] = []
let stopWs: (() => void) | null = null
let resizeTimer: any = null
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
})

// 保活生命周期：激活重连实时流并恢复图表尺寸；切走时断开实时流（历史数据保留）
onActivated(() => {
  stopWs = wsTopic('metrics', (m) => update(m))
  resizeTimer = setInterval(() => charts.forEach(c => c.resize()), 3000)
  charts.forEach(c => c.resize())
})
onDeactivated(() => { stopWs?.(); stopWs = null; clearInterval(resizeTimer) })
onUnmounted(() => { stopWs?.(); clearInterval(resizeTimer); charts.forEach(c => c?.dispose()) })
</script>
