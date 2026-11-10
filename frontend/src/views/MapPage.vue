<template>
  <a-card size="small">
    <a-space style="margin-bottom: 12px" wrap>
      <a-select v-model:value="minutes" style="width: 140px" :options="[{ label: '最近 10 分钟', value: 10 }, { label: '最近 60 分钟', value: 60 }, { label: '最近 6 小时', value: 360 }, { label: '最近 24 小时', value: 1440 }]" @change="load" />
      <a-tag :color="geoLoaded ? 'green' : 'orange'">{{ geoLoaded ? 'GeoIP 已加载' : 'GeoIP 未加载' }}</a-tag>
      <span class="dim">实时流向</span>
      <a-switch v-model:checked="realtime" size="small" />
      <a-tag v-if="realtime" color="blue">实时 {{ liveCount }} 请求</a-tag>
      <a-button size="small" @click="downloadGeo">下载 GeoLite2 数据库</a-button>
      <a-upload :show-upload-list="false" :custom-request="uploadGeo"><a-button size="small">上传 mmdb</a-button></a-upload>
      <span class="dim">日志由 nginx access log 增量解析（实时条目 1s 推送）</span>
    </a-space>

    <a-row :gutter="12">
      <a-col :span="24">
        <div ref="mapBox" style="height: 480px"></div>
      </a-col>
    </a-row>

    <a-row :gutter="12" style="margin-top: 12px">
      <a-col :span="8">
        <a-card title="国家分布" size="small">
          <a-table :data-source="summary.countries" :columns="countryCols" size="small" row-key="country" :pagination="false" :scroll="{ y: 240 }">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'country'">
                {{ countryName(record.country) }} ({{ record.country }})
              </template>
              <template v-else-if="column.key === 'bytes'">{{ fmtBytes(record.bytes) }}</template>
            </template>
          </a-table>
        </a-card>
      </a-col>
      <a-col :span="8">
        <a-card title="站点分布" size="small">
          <a-table :data-source="summary.sites" :columns="siteCols" size="small" row-key="site" :pagination="false" :scroll="{ y: 240 }">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'bytes'">{{ fmtBytes(record.bytes) }}</template>
            </template>
          </a-table>
        </a-card>
      </a-col>
      <a-col :span="8">
        <a-card title="状态码" size="small">
          <a-space wrap>
            <a-tag v-for="(v, k) in summary.statuses" :key="k" :color="String(k) === '2xx' ? 'green' : String(k) === '5xx' ? 'red' : String(k) === '4xx' ? 'orange' : 'blue'">{{ k }}: {{ v }}</a-tag>
          </a-space>
          <a-descriptions v-if="summary.total != null" :column="1" size="small" style="margin-top: 12px">
            <a-descriptions-item label="总请求">{{ summary.total }}</a-descriptions-item>
            <a-descriptions-item label="错误(4xx/5xx)">{{ summary.errors }}</a-descriptions-item>
            <a-descriptions-item label="总流量">{{ fmtBytes(summary.bytes) }}</a-descriptions-item>
          </a-descriptions>
        </a-card>
      </a-col>
    </a-row>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onDeactivated, onUnmounted, ref, nextTick } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, wsTopic, fmtBytes, upload } from '../api'

const minutes = ref(60)
const summary = ref<any>({ countries: [], sites: [], statuses: {}, recent: [] })
const geoLoaded = ref(false)
const mapBox = ref<HTMLElement | null>(null)
let chart: any = null
let stopWs: (() => void) | null = null
let timer: any = null

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

// 常用国家中文名
const names: Record<string, string> = { CN: '中国', US: '美国', JP: '日本', KR: '韩国', SG: '新加坡', HK: '香港', TW: '台湾', DE: '德国', GB: '英国', FR: '法国', RU: '俄罗斯', IN: '印度', CA: '加拿大', AU: '澳大利亚', BR: '巴西', NL: '荷兰', unknown: '未知' }
const countryName = (c: string) => names[c] || c

// 国家质心（与后端 Centroids 一致的主要国家）
const centroids: Record<string, [number, number]> = {
  CN: [104.19, 35.86], US: [-95.71, 37.09], JP: [138.25, 36.2], KR: [127.86, 36.5], SG: [103.82, 1.35],
  HK: [114.17, 22.32], TW: [120.96, 23.7], DE: [10.45, 51.16], GB: [-3.43, 55.37], FR: [46.22, 2.21],
  RU: [105.31, 61.52], IN: [78.96, 20.59], CA: [-106.34, 56.13], AU: [133.77, -25.27], BR: [-51.92, -10.8],
  NL: [5.29, 52.13], ID: [113.92, -0.79], TH: [100.99, 15.87], VN: [108.27, 14.05], MY: [101.97, 4.21],
  PH: [121.77, 12.87], IT: [12.56, 41.87], ES: [-3.74, 40.46], TR: [35.24, 38.96], UA: [31.16, 48.37],
}
// 服务器位置：优先后端探测的真实出口位置，回退本地缓存；初始近似北京
const serverPoint = ref<[number, number]>(
  JSON.parse(localStorage.getItem('zjp_server_loc') || 'null') || [116.4, 39.9]
)

// ---- 实时流向 ----
const realtime = ref(true)
const liveCount = ref(0)
const liveFlows = ref<{ country: string; coords: [number, number][]; ts: number }[]>([])

const handleEntries = (list: any[]) => {
  if (!realtime.value) return
  liveCount.value += list.length
  let changed = false
  for (const e of list) {
    // 飞线起点：请求方真实坐标（后端 GeoIP），缺失时回退国家质心
    const c: [number, number] | undefined = (e.lat || e.lon)
      ? [e.lon, e.lat]
      : centroids[e.country]
    if (!c) continue
    changed = true
    // 同一国家仅保留最新一条飞线（避免多条叠加杂乱）
    liveFlows.value = liveFlows.value.filter(f => f.country !== e.country)
    liveFlows.value.push({ country: e.country, coords: [c, serverPoint.value] as [number, number][], ts: Date.now() })
    // 实时条目插入最近请求展示（限制长度，summary 会周期覆盖）
    summary.value.recent = [e, ...(summary.value.recent || [])].slice(0, 100)
  }
  if (changed) renderMap()
}

// 飞线寿命清扫：超过 4.3s（一个动画周期）的线移除——一个请求只飞一次
const FLOW_LIFETIME = 4300
let sweepTimer: any = null
const startSweep = () => {
  sweepTimer = setInterval(() => {
    if (!liveFlows.value.length) return
    const cut = Date.now() - FLOW_LIFETIME
    const keep = liveFlows.value.filter(f => f.ts > cut)
    if (keep.length !== liveFlows.value.length) {
      liveFlows.value = keep
      renderMap()
    }
  }, 500)
}

const renderMap = async () => {
  if (!mapBox.value) return
  const echarts = await import('echarts')
  if (!chart) {
    const res = await fetch('/map/world.json')
    const geo = await res.json()
    echarts.registerMap('world', geo as any)
    chart = echarts.init(mapBox.value)
  }
  const countries = (summary.value.countries || []).filter((c: any) => c.country !== 'unknown')
  const maxCount = Math.max(1, ...countries.map((c: any) => c.count))
  // 分档着色（请求越多越深）
  const shades = ['#dbe9ff', '#9dbdff', '#5c8dff', '#2f5fe0', '#1c3fd6']
  const colorFor = (n: number) => {
    const r = n / maxCount
    return shades[Math.min(shades.length - 1, Math.floor(r * shades.length))]
  }
  // 飞线只在实时请求到达时出现（一次请求飞一次）；无请求时地图安静
  const flows = realtime.value
    ? liveFlows.value.map(f => ({ coords: f.coords }))
    : []
  chart.setOption({
    tooltip: { show: true, formatter: (p: any) => p.name || '' },
    // geo 组件：底图 + 数据着色 + 飞线坐标系（lines/effectScatter 必须挂在 geo 上）
    geo: {
      map: 'world', roam: true,
      itemStyle: { areaColor: '#eef2f7', borderColor: '#c0cdd8' },
      emphasis: { label: { show: false }, itemStyle: { areaColor: '#7c9cff' } },
      regions: countries.map((c: any) => ({
        name: mapName(c.country),
        itemStyle: { areaColor: colorFor(c.count) },
      })),
    },
    series: [
      {
        type: 'lines', coordinateSystem: 'geo', zlevel: 2,
        effect: { show: true, period: 4, trailLength: 0.6, symbol: 'arrow', symbolSize: 9, color: '#ffb54d' },
        lineStyle: { color: '#4f7cff', width: 2, opacity: 0.5, curveness: 0.35 },
        data: flows,
      },
      {
        type: 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
        symbolSize: 12, rippleEffect: { scale: 3 },
        itemStyle: { color: '#ff5722' },
        data: [{ value: serverPoint.value, name: '服务器' }],
      },
    ],
  }, true)
}

// geojson 里的 name 属性是英文国家名，做一层映射
const enNames: Record<string, string> = { CN: 'China', US: 'United States', JP: 'Japan', KR: 'Korea', SG: 'Singapore', HK: 'Hong Kong', TW: 'Taiwan', DE: 'Germany', GB: 'United Kingdom', FR: 'France', RU: 'Russia', IN: 'India', CA: 'Canada', AU: 'Australia', BR: 'Brazil', NL: 'Netherlands', ID: 'Indonesia', TH: 'Thailand', VN: 'Vietnam', MY: 'Malaysia', PH: 'Philippines', IT: 'Italy', ES: 'Spain', TR: 'Turkey', UA: 'Ukraine' }
const mapName = (c: string) => enNames[c] || c

const load = async () => {
  summary.value = await get('/api/logmap/summary', { minutes: minutes.value, recent: 50 })
  geoLoaded.value = summary.value.geoLoaded
  // 服务器真实位置（出口 IP 经 GeoIP 反查）：有则缓存并更新地图标记
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

// 保活生命周期：激活时重连 WS/轮询并恢复图表；切走时全部暂停。
// keep-alive 下首次挂载也会触发 onActivated，启动逻辑统一放这里避免重复定时器
onActivated(() => {
  load()
  nextTick(() => {
    stopWs = wsTopic('logmap', (m) => {
      if (m.type === 'summary') { summary.value = m.data; renderMap() }
      else if (m.type === 'entries') handleEntries(m.data || [])
    })
  })
  timer = setInterval(load, 15000)
  startSweep()
})
onDeactivated(() => {
  stopWs?.(); stopWs = null
  clearInterval(timer); clearInterval(sweepTimer)
})
onUnmounted(() => { stopWs?.(); clearInterval(timer); clearInterval(sweepTimer); chart?.dispose() })
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
</style>
