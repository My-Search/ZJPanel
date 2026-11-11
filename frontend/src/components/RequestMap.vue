<template>
  <div class="request-map">
    <a-space class="map-toolbar" wrap>
      <a-tag :color="geoLoaded ? 'green' : 'orange'">{{ geoLoaded ? 'GeoIP 已加载' : 'GeoIP 未加载' }}</a-tag>
      <span class="dim">实时流向</span>
      <a-switch v-model:checked="realtime" size="small" />
      <a-tag v-if="realtime" color="blue">实时 {{ liveCount }} 请求</a-tag>
      <template v-if="!compact">
        <a-button size="small" @click="downloadGeo">下载 GeoLite2 数据库</a-button>
        <a-upload :show-upload-list="false" :custom-request="uploadGeo"><a-button size="small">上传 mmdb</a-button></a-upload>
        <span class="dim">日志由 nginx access log 增量解析（实时条目 1s 推送）</span>
      </template>
    </a-space>

    <div ref="mapBox" class="map-box" :style="{ '--map-height': height + 'px' }"></div>

    <!-- 统计时间范围放在地图下方（与详细统计同区），避免误读为地图实时数据的过滤条件 -->
    <div class="stats-range">
      <a-select v-model:value="minutes" style="width: 140px" :options="minuteOpts" @change="load" />
      <span class="dim">以下统计的时间范围</span>
    </div>

    <!-- 地图下方详细信息：默认收纳，点击标题展开 -->
    <a-collapse v-model:activeKey="detailsOpen" :bordered="false" class="details-collapse">
      <a-collapse-panel key="details">
        <template #header>
          <span class="details-title">详细信息 · 国家分布 / 站点分布 / 状态码</span>
          <span class="dim details-hint">{{ detailsOpen.length ? '点击收起' : '点击展开' }}</span>
        </template>
        <a-row :gutter="12">
          <a-col :span="8">
            <a-card title="国家分布" size="small">
              <a-table :data-source="summary.countries" :columns="countryCols" size="small" row-key="country" :pagination="false" :scroll="{ x: 420, y: 240 }">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'country'">{{ countryName(record.country) }} ({{ record.country }})</template>
                  <template v-else-if="column.key === 'bytes'">{{ fmtBytes(record.bytes) }}</template>
                </template>
              </a-table>
            </a-card>
          </a-col>
          <a-col :span="8">
            <a-card title="站点分布" size="small">
              <a-table :data-source="summary.sites" :columns="siteCols" size="small" row-key="site" :pagination="false" :scroll="{ x: 360, y: 240 }">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'bytes'">{{ fmtBytes(record.bytes) }}</template>
                </template>
              </a-table>
            </a-card>
          </a-col>
          <a-col :span="8">
            <a-card title="状态码" size="small">
              <a-space wrap>
                <a-tag v-for="(v, k) in summary.statuses" :key="k" :color="statusColor(k)">{{ k }}: {{ v }}</a-tag>
              </a-space>
              <a-descriptions v-if="summary.total != null" :column="1" size="small" style="margin-top: 12px">
                <a-descriptions-item label="总请求">{{ summary.total }}</a-descriptions-item>
                <a-descriptions-item label="错误(4xx/5xx)">{{ summary.errors }}</a-descriptions-item>
                <a-descriptions-item label="总流量">{{ fmtBytes(summary.bytes) }}</a-descriptions-item>
              </a-descriptions>
            </a-card>
          </a-col>
        </a-row>
      </a-collapse-panel>
    </a-collapse>
  </div>
</template>

<script setup lang="ts">
import { onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, wsTopic, fmtBytes, upload } from '../api'
import { useTheme } from '../store/theme'

/** 请求地图（地图 + 流向 + 详细统计）。仪表盘与「请求地图」页共用，避免两套逻辑。 */
const props = withDefaults(defineProps<{ compact?: boolean; height?: number }>(), { compact: false, height: 480 })

const ui = useTheme()
const minuteOpts = [
  { label: '最近 10 分钟', value: 10 }, { label: '最近 60 分钟', value: 60 },
  { label: '最近 6 小时', value: 360 }, { label: '最近 24 小时', value: 1440 },
]
const statusColor = (k: string | number) => (String(k) === '2xx' ? 'green' : String(k) === '5xx' ? 'red' : String(k) === '4xx' ? 'orange' : 'blue')

const minutes = ref(60)
const summary = ref<any>({ countries: [], sites: [], statuses: {}, recent: [] })
const geoLoaded = ref(false)
const mapBox = ref<HTMLElement | null>(null)
const detailsOpen = ref<string[]>([]) // 默认收纳详细信息
let chart: any = null
let initPromise: Promise<void> | null = null
let active = false
let stopWs: (() => void) | null = null
let timer: any = null
let sweepTimer: any = null
let resizeObserver: ResizeObserver | null = null

const countryCols = [
  { title: '国家', key: 'country' },
  { title: '请求数', dataIndex: 'count', key: 'count', width: 80 },
  { title: '占比', dataIndex: 'percent', key: 'percent', width: 70, customRender: (o: any) => o.value?.toFixed(1) + '%' },
  { title: '流量', key: 'bytes', width: 90 },
]
const siteCols = [
  { title: '站点', dataIndex: 'site', key: 'site' },
  { title: '请求数', dataIndex: 'count', key: 'count', width: 80 },
  { title: '流量', key: 'bytes', width: 100 },
]

const names: Record<string, string> = { CN: '中国', US: '美国', JP: '日本', KR: '韩国', SG: '新加坡', HK: '香港', TW: '台湾', DE: '德国', GB: '英国', FR: '法国', RU: '俄罗斯', IN: '印度', CA: '加拿大', AU: '澳大利亚', BR: '巴西', NL: '荷兰', unknown: '未知' }
const countryName = (c: string) => names[c] || c

// 国家质心（与后端 Centroids 一致的主要国家）：GeoIP 缺坐标时回退
const centroids: Record<string, [number, number]> = {
  CN: [104.19, 35.86], US: [-95.71, 37.09], JP: [138.25, 36.2], KR: [127.86, 36.5], SG: [103.82, 1.35],
  HK: [114.17, 22.32], TW: [120.96, 23.7], DE: [10.45, 51.16], GB: [-3.43, 55.37], FR: [46.22, 2.21],
  RU: [105.31, 61.52], IN: [78.96, 20.59], CA: [-106.34, 56.13], AU: [133.77, -25.27], BR: [-51.92, -10.8],
  NL: [5.29, 52.13], ID: [113.92, -0.79], TH: [100.99, 15.87], VN: [108.27, 14.05], MY: [101.97, 4.21],
  PH: [121.77, 12.87], IT: [12.56, 41.87], ES: [-3.74, 40.46], TR: [35.24, 38.96], UA: [31.16, 48.37],
}
// 服务器位置：后端探测的出口 IP 坐标优先（[lon,lat]），回退本地缓存
const serverPoint = ref<[number, number]>(
  JSON.parse(localStorage.getItem('zjp_server_loc') || 'null') || [116.4, 39.9]
)

const realtime = ref(true)
const liveCount = ref(0)
// 实时流向：一次请求 = 一条「来源黄点 → 服务器」的飞线。
// 记录国家、坐标与时间戳；同一国家短时间内多次请求合并计数，用线的宽度/亮度体现请求量。
const liveFlows = ref<{ country: string; coords: [number, number][]; ts: number; count: number }[]>([])

const handleEntries = (list: any[]) => {
  if (!realtime.value) return
  liveCount.value += list.length
  let changed = false
  for (const e of list) {
    const c: [number, number] | undefined = (e.lat || e.lon) ? [e.lon, e.lat] : centroids[e.country]
    if (!c) continue
    changed = true
    // 同一国家 5s 内的多次请求合并为一条飞线（count 累加 → 线更粗更亮）
    const cur = liveFlows.value.find(f => f.country === e.country)
    if (cur && Date.now() - cur.ts < 5000) {
      cur.count++
      cur.ts = Date.now()
    } else {
      liveFlows.value = liveFlows.value.filter(f => f.country !== e.country)
      liveFlows.value.push({ country: e.country, coords: [c, serverPoint.value] as [number, number][], ts: Date.now(), count: 1 })
    }
    summary.value.recent = [e, ...(summary.value.recent || [])].slice(0, 100)
  }
  if (changed) renderMap()
}

// 飞线寿命清扫：超过一个动画周期的线移除（一个请求只飞一次）
const FLOW_LIFETIME = 4300
const startSweep = () => {
  sweepTimer = setInterval(() => {
    if (!liveFlows.value.length) return
    const cut = Date.now() - FLOW_LIFETIME
    const keep = liveFlows.value.filter(f => f.ts > cut)
    if (keep.length !== liveFlows.value.length) { liveFlows.value = keep; renderMap() }
  }, 500)
}

const enNames: Record<string, string> = { CN: 'China', US: 'United States', JP: 'Japan', KR: 'Korea', SG: 'Singapore', HK: 'Hong Kong', TW: 'Taiwan', DE: 'Germany', GB: 'United Kingdom', FR: 'France', RU: 'Russia', IN: 'India', CA: 'Canada', AU: 'Australia', BR: 'Brazil', NL: 'Netherlands', ID: 'Indonesia', TH: 'Thailand', VN: 'Vietnam', MY: 'Malaysia', PH: 'Philippines', IT: 'Italy', ES: 'Spain', TR: 'Turkey', UA: 'Ukraine' }
const mapName = (c: string) => enNames[c] || c

const ensureChart = async () => {
  if (chart) return
  if (initPromise) return initPromise
  initPromise = (async () => {
    const echarts = await import('echarts')
    const res = await fetch('/map/world.json')
    const geo = await res.json()
    if (!mapBox.value || !active) return // 初始化期间页面已被切走
    echarts.registerMap('world', geo as any)
    chart = echarts.init(mapBox.value)
  })().finally(() => { initPromise = null })
  return initPromise
}

const renderMap = async () => {
  if (!mapBox.value || !active) return
  await ensureChart()
  if (!chart || !active) return
  const countries = (summary.value.countries || []).filter((c: any) => c.country !== 'unknown')
  const maxCount = Math.max(1, ...countries.map((c: any) => c.count))
  const shades = ['#dbe9ff', '#9dbdff', '#5c8dff', '#2f5fe0', '#1c3fd6']
  const colorFor = (n: number) => shades[Math.min(shades.length - 1, Math.floor((n / maxCount) * shades.length))]
  // 实时流向：来源黄点 + 飞线（宽度/透明度随请求量增强），方向 = 来源 → 服务器
  const maxFlow = Math.max(1, ...liveFlows.value.map(f => f.count))
  const flows = realtime.value
    ? liveFlows.value.map(f => ({
        coords: f.coords,
        lineStyle: { width: 1 + (f.count / maxFlow) * 2.5, opacity: 0.35 + (f.count / maxFlow) * 0.45 },
        effect: { symbolSize: 5 + (f.count / maxFlow) * 4 },
      }))
    : []
  const srcPoints = realtime.value
    ? liveFlows.value.map(f => ({
        value: f.coords[0],
        name: mapName(f.country),
        symbolSize: 7,
        itemStyle: { color: '#ffd54d' },
      }))
    : []
  chart.setOption({
    tooltip: { show: true, formatter: (p: any) => p.name || '' },
    // 深浅主题各自适配的底图配色
    geo: {
      map: 'world', roam: true,
      itemStyle: { areaColor: ui.isDark ? '#26303f' : '#eef2f7', borderColor: ui.isDark ? '#3b4757' : '#c0cdd8' },
      emphasis: { label: { show: false }, itemStyle: { areaColor: ui.isDark ? '#3d5680' : '#7c9cff' } },
      regions: countries.map((c: any) => ({ name: mapName(c.country), itemStyle: { areaColor: colorFor(c.count) } })),
    },
    series: [
      {
        type: 'lines', coordinateSystem: 'geo', zlevel: 2,
        effect: { show: true, period: 4, trailLength: 0.5, symbol: 'arrow', symbolSize: 6, color: '#ffd54d' },
        lineStyle: { color: '#4f7cff', width: 1.5, opacity: 0.4, curveness: 0.35 },
        data: flows,
      },
      {
        // 请求来源点：黄色小数据点（实时请求落点）
        type: 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
        rippleEffect: { scale: 2.2, brushType: 'stroke' },
        itemStyle: { color: '#ffd54d' },
        data: srcPoints,
      },
      {
        // 服务器位置：红色小点（缩小，避免喧宾夺主）
        type: 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
        symbolSize: 7, rippleEffect: { scale: 2 },
        itemStyle: { color: '#ff5722' },
        data: [{ value: serverPoint.value, name: '服务器' }],
      },
    ],
  }, true)
}

const load = async () => {
  summary.value = await get('/api/logmap/summary', { minutes: minutes.value, recent: 50 })
  geoLoaded.value = summary.value.geoLoaded
  if (summary.value.serverLoc?.length === 2) {
    const [lon, lat] = summary.value.serverLoc
    const cur = serverPoint.value
    if (cur[0] !== lon || cur[1] !== lat) {
      serverPoint.value = [lon, lat]
      localStorage.setItem('zjp_server_loc', JSON.stringify(serverPoint.value))
      renderMap()
    }
  }
  renderMap()
}
const downloadGeo = async () => {
  try { await post('/api/logmap/geoip/download', {}); message.success('已下载并加载'); load() } catch { /* 拦截器提示 */ }
}
const uploadGeo = async (opt: any) => {
  try { await upload('/api/logmap/geoip/upload', opt.file); message.success('已上传并加载'); load() } catch { /* ignore */ }
}
watch(() => ui.isDark, () => renderMap())
watch(() => props.height, () => chart?.resize())

/**
 * 生命周期统一收敛到幂等 start/stop：组件既可能挂在普通页面，也可能作为
 * keep-alive 缓存页（仪表盘/请求地图页）的后代，onMounted 与 onActivated 都会触发。
 */
const start = () => {
  if (active) return
  active = true
  load()
  stopWs = wsTopic('logmap', (m) => {
    if (m.type === 'summary') { summary.value = m.data; renderMap() }
    else if (m.type === 'entries') handleEntries(m.data || [])
  })
  timer = setInterval(load, 15000)
  startSweep()
  nextTick(() => {
    if (mapBox.value && !resizeObserver) {
      resizeObserver = new ResizeObserver(() => chart?.resize())
      resizeObserver.observe(mapBox.value)
    }
    chart?.resize()
  })
}
const stop = () => {
  active = false
  stopWs?.(); stopWs = null
  clearInterval(timer); timer = null
  clearInterval(sweepTimer); sweepTimer = null
}
onMounted(start)
onActivated(start)
onDeactivated(stop)
onUnmounted(() => { stop(); resizeObserver?.disconnect(); resizeObserver = null; chart?.dispose(); chart = null })
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
.map-toolbar { margin-bottom: 12px; }
.stats-range { display: flex; align-items: center; gap: 10px; margin: 8px 0 4px; }
.map-box { width: 100%; height: var(--map-height); }
@media (max-width: 767px) { .map-box { height: min(var(--map-height), 320px); } }
.details-collapse { margin-top: 8px; }
.details-title { font-weight: 600; }
.details-hint { margin-left: 12px; font-weight: 400; }
:deep(.ant-collapse-header) { align-items: center !important; }
:deep(.ant-collapse-content-box) { padding: 4px 0 0 !important; }
</style>
