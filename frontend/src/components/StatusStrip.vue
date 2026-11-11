<template>
  <div class="status-strip" :class="{ dark: ui.isDark }">
    <template v-if="stats">
      <span class="item" :title="`CPU ${stats.cpu.toFixed(1)}%`">
        <ThunderboltOutlined class="ico" /><span class="val">{{ stats.cpu.toFixed(0) }}%</span>
        <span class="bar"><span class="fill cpu" :style="{ width: stats.cpu + '%' }"></span></span>
      </span>
      <span class="item" :title="`内存 ${stats.mem.toFixed(1)}% ${stats.memTxt}`">
        <DatabaseOutlined class="ico" /><span class="val">{{ stats.mem.toFixed(0) }}%</span>
        <span class="bar"><span class="fill mem" :style="{ width: stats.mem + '%' }"></span></span>
      </span>
      <span class="item" :title="`磁盘 ${stats.disk.toFixed(1)}% ${stats.diskTxt}`">
        <HddOutlined class="ico" /><span class="val">{{ stats.disk.toFixed(0) }}%</span>
        <span class="bar"><span class="fill disk" :style="{ width: stats.disk + '%' }"></span></span>
      </span>
      <span class="item net" :title="`↓ ${fmtNet(stats.netIn)} / ↑ ${fmtNet(stats.netOut)}`">
        <GlobalOutlined class="ico" /><span class="val">↑ {{ fmtNet(stats.netOut) }} ↓ {{ fmtNet(stats.netIn) }}</span>
      </span>
      <span v-if="stats.load1" class="item" :title="`1 分钟负载 ${stats.load1.toFixed(2)}`">
        <DashboardOutlined class="ico" /><span class="val">{{ stats.load1.toFixed(2) }}</span>
      </span>
    </template>
    <span v-else class="waiting">资源采集中…</span>
  </div>
</template>

<script setup lang="ts">
/**
 * 终端底部资源状态栏：显示当前会话所连机器的 CPU/内存/磁盘/网络。
 * 数据由终端 WS 的 stats 消息推送（父组件转发），5s 刷新；采集未就绪时显示等待态。
 */
import { ThunderboltOutlined, DatabaseOutlined, HddOutlined, GlobalOutlined, DashboardOutlined } from '@ant-design/icons-vue'
import { useTheme } from '../store/theme'

const ui = useTheme()

defineProps<{
  /** 最近一次机器资源采样；null=尚未采到 */
  stats: {
    cpu: number; mem: number; memTxt: string
    disk: number; diskTxt: string
    netIn: number; netOut: number; load1: number
  } | null
}>()

const fmtNet = (v: number) => {
  if (!v || v <= 0) return '0KB/s'
  if (v < 1024 * 1024) return (v / 1024).toFixed(0) + 'KB/s'
  return (v / 1024 / 1024).toFixed(1) + 'MB/s'
}
</script>

<style scoped>
.status-strip {
  display: flex; align-items: center; gap: 4px 18px; flex-wrap: wrap;
  border-top: 1px solid #e5e7eb; margin-top: 6px; padding: 6px 10px 0;
  color: #475569; font-size: 12px; min-height: 30px; user-select: none;
}
.status-strip.dark { border-top-color: #2b2b2b; color: #9ca3af; }
.item { display: inline-flex; align-items: center; gap: 6px; }
.ico { font-size: 12px; }
.val { min-width: 44px; }
.bar { width: 64px; height: 5px; border-radius: 3px; background: rgba(148, 163, 184, .25); overflow: hidden; }
.fill { display: block; height: 100%; border-radius: 3px; transition: width .5s ease; }
.fill.cpu { background: #6366f1; }
.fill.mem { background: #0ea5e9; }
.fill.disk { background: #10b981; }
.net .val { min-width: 110px; }
.waiting { color: #94a3b8; font-size: 12px; }
@media (max-width: 767px) {
  .status-strip { gap: 4px 12px; }
  .bar { width: 44px; }
}
</style>
