<template>
  <div class="status-strip">
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
  display: flex; align-items: center; gap: 4px 12px; flex-wrap: nowrap;
  overflow-x: auto; scrollbar-width: none;
  border-top: 1px solid var(--border-card); margin-top: 6px; padding: 5px 2px 0;
  color: var(--color-slate); font-size: 12px; min-height: 26px; user-select: none;
}
.status-strip::-webkit-scrollbar { display: none; }
.item { display: inline-flex; align-items: center; gap: 4px; flex: none; }
.ico { font-size: 12px; }
.val { min-width: 30px; }
.bar { width: 44px; height: 5px; border-radius: 3px; background: rgba(148, 163, 184, .25); overflow: hidden; }
.fill { display: block; height: 100%; border-radius: 3px; transition: width .5s ease; }
.fill.cpu { background: #6366f1; }
.fill.mem { background: #0ea5e9; }
.fill.disk { background: #10b981; }
.net .val { min-width: 92px; }
.waiting { color: var(--color-slate); font-size: 12px; flex: none; }
@media (max-width: 767px) {
  .status-strip { gap: 4px 10px; }
  .bar { width: 32px; }
  .net .val { min-width: 80px; }
}
/* 文件面板展开后终端区可能只有 300px 左右：去掉进度条与固定宽度，保证单行 */
@container (max-width: 520px) {
  .status-strip { gap: 4px 8px; }
  .bar { display: none; }
  .val { min-width: 0; }
  .net .val { min-width: 0; }
}
</style>
