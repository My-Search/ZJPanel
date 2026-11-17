<template>
  <div class="request-map" :class="{ fill }">
    <a-space class="map-toolbar" wrap>
      <a-tag :color="geoLoaded ? 'green' : 'orange'">{{ geoLoaded ? 'GeoIP 已加载' : 'GeoIP 未加载' }}</a-tag>
      <span class="dim">实时流向</span>
      <a-switch v-model:checked="realtime" size="small" />
      <a-tag v-if="realtime" color="blue">实时 {{ rate }} req/s（近 5s 滑窗）</a-tag>
      <!-- compact（仪表盘卡片）不放统计窗口选择：紧挨「实时」标签会被误读为实时开关。
           仪表盘固定用最近 60 分钟聚合（流星本身是实时推送，不受窗口影响），完整地图页保留选择器 -->
      <template v-if="!compact">
        <a-button size="small" @click="downloadGeo">下载 GeoLite2 数据库</a-button>
        <a-upload :show-upload-list="false" :custom-request="uploadGeo"><a-button size="small">上传 mmdb</a-button></a-upload>
        <!-- 服务器位置：显示当前来源/精度，可手动指定坐标校准（GeoLite2 对服务器 IP 常只有国家级精度） -->
        <a-popover v-model:open="locPopOpen" trigger="click" placement="bottomLeft">
          <a-button size="small">服务器位置</a-button>
          <template #content>
            <div class="loc-pop">
              <div class="dim">当前生效：{{ serverLocText }}</div>
              <div class="loc-inputs">
                <a-input-number v-model:value="manualLon" placeholder="经度" :min="-180" :max="180" :step="0.0001" />
                <a-input-number v-model:value="manualLat" placeholder="纬度" :min="-90" :max="90" :step="0.0001" />
              </div>
              <a-space style="margin-top: 8px">
                <a-button size="small" type="primary" :disabled="manualLon == null || manualLat == null" @click="saveServerLoc">指定坐标</a-button>
                <a-button size="small" :disabled="!hasManual" @click="clearServerLoc">清除手动指定</a-button>
              </a-space>
              <div class="dim" style="margin-top: 8px">
                自动探测：出口 IP 反查（在线接口优先，GeoIP 兜底）。GeoLite2 对服务器 IP 常只有国家级精度（落到国家中心），手动指定可精确校准。
              </div>
            </div>
          </template>
        </a-popover>
        <span class="dim">日志由 nginx access log 增量解析（SSE 逐条实时推送，一次请求一条流星）</span>
      </template>
    </a-space>

    <div ref="mapBox" class="map-box" :style="{ '--map-height': height + 'px' }" @mouseleave="resetRoamView"></div>

    <!-- 统计时间范围放在地图下方（与详细统计同区），避免误读为地图实时数据的过滤条件；compact 模式已并入上方工具条 -->
    <div v-if="!compact" class="stats-range">
      <a-select v-model:value="minutes" style="width: 140px" :options="minuteOpts" @change="load" />
      <span class="dim">以下统计的时间范围</span>
    </div>

    <!-- 站点请求树：各网站 → 请求路径 → 次数。固定最近 24 小时窗口（与地图统计窗口无关），独立 60s 轮询 -->
    <a-collapse v-if="!compact" v-model:activeKey="treeOpen" :bordered="false" class="details-collapse">
      <a-collapse-panel key="tree">
        <template #header>
          <span class="details-title">站点请求树 · 各网站请求路径次数</span>
          <span class="dim details-hint">最近 24 小时 · {{ treeTotal }} 次请求</span>
        </template>
        <a-tree
          v-if="treeData.length"
          :tree-data="treeData"
          :expanded-keys="treeExpanded"
          block-node
          :selectable="false"
          :height="380"
          @expand="(keys: any) => (treeExpanded = keys)"
        />
        <a-empty v-else description="最近 24 小时暂无请求" :image-style="{ height: '48px' }" />
      </a-collapse-panel>
    </a-collapse>

    <!-- 地图下方详细信息：默认收纳，点击标题展开（完整地图页专属，仪表盘 compact 模式不渲染） -->
    <a-collapse v-if="!compact" v-model:activeKey="detailsOpen" :bordered="false" class="details-collapse">
      <a-collapse-panel key="details">
        <template #header>
          <span class="details-title">详细信息 · 城市分布 / 国家分布 / 站点分布 / 状态码</span>
          <span class="dim details-hint">{{ detailsOpen.length ? '点击收起' : '点击展开' }}</span>
        </template>
        <a-row :gutter="12" style="margin-bottom: 12px">
          <a-col :span="24">
            <a-card title="城市分布" size="small">
              <a-table :data-source="summary.cities" :columns="cityCols" size="small" :row-key="(r: any) => r.country + '|' + r.city + '|' + r.lat + ',' + r.lon" :pagination="false" :scroll="{ x: 520, y: 240 }">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'city'">
                    <template v-if="record.city">{{ record.city }}</template>
                    <span class="dim" v-else>（国家级：库中无城市记录）</span>
                  </template>
                  <template v-else-if="column.key === 'country'">{{ countryName(record.country) }} ({{ record.country }})</template>
                  <template v-else-if="column.key === 'bytes'">{{ fmtBytes(record.bytes) }}</template>
                </template>
              </a-table>
            </a-card>
          </a-col>
        </a-row>
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
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, sseTopic, fmtBytes, upload } from '../api'
import { useTheme } from '../store/theme'

/** 请求地图（地图 + 流向 + 详细统计）。仪表盘与「请求地图」页共用，避免两套逻辑。 */
const props = withDefaults(defineProps<{ compact?: boolean; height?: number; fill?: boolean }>(), { compact: false, height: 480 })

const ui = useTheme()
const minuteOpts = [
  { label: '最近 10 分钟', value: 10 }, { label: '最近 60 分钟', value: 60 },
  { label: '最近 6 小时', value: 360 }, { label: '最近 24 小时', value: 1440 },
]
const statusColor = (k: string | number) => (String(k) === '2xx' ? 'green' : String(k) === '5xx' ? 'red' : String(k) === '4xx' ? 'orange' : 'blue')

const minutes = ref(60)
const summary = ref<any>({ countries: [], sites: [], statuses: {}, recent: [] })

// 站点请求树：固定最近 24 小时窗口（与地图统计窗口无关），独立 60s 轮询（仅完整地图页）
const treeOpen = ref<string[]>(['tree'])
const treeSummary = ref<any>(null)
const treeExpanded = ref<(string | number)[]>([])
const treeTotal = computed(() => treeSummary.value?.total ?? 0)
// 树结构：站点（·总次数）→ 路径（·次数），站点默认全部展开
const treeData = computed(() =>
  (treeSummary.value?.sites || []).map((s: any) => ({
    key: 'site:' + s.site,
    title: `${s.site} · ${s.count} 次`,
    selectable: false,
    children: (s.paths || []).map((p: any) => ({
      key: `path:${s.site}:${p.path}`,
      title: `${p.path} · ${p.count} 次`,
      selectable: false,
      isLeaf: true,
    })),
  })),
)
watch(treeData, (d) => { treeExpanded.value = d.map((n: any) => n.key) })
const loadTree = async () => {
  try { treeSummary.value = await get('/api/logmap/summary', { minutes: 1440, recent: 0, paths: 1 }) } catch { /* 拦截器提示 */ }
}
let treeTimer: any = null

const geoLoaded = ref(false)
const mapBox = ref<HTMLElement | null>(null)
const detailsOpen = ref<string[]>([]) // 默认收纳详细信息
let chart: any = null
let initPromise: Promise<void> | null = null
let active = false
let stopStream: (() => void) | null = null
let timer: any = null
let sweepTimer: any = null
let resizeObserver: ResizeObserver | null = null

const countryCols = [
  { title: '国家', key: 'country' },
  { title: '请求数', dataIndex: 'count', key: 'count', width: 80 },
  { title: '占比', dataIndex: 'percent', key: 'percent', width: 70, customRender: (o: any) => o.value?.toFixed(1) + '%' },
  { title: '流量', key: 'bytes', width: 90 },
]
const cityCols = [
  { title: '城市', key: 'city' },
  { title: '国家', key: 'country', width: 150 },
  { title: '请求数', dataIndex: 'count', key: 'count', width: 80 },
  { title: '流量', key: 'bytes', width: 100 },
]
const siteCols = [
  { title: '站点', dataIndex: 'site', key: 'site' },
  { title: '请求数', dataIndex: 'count', key: 'count', width: 80 },
  { title: '流量', key: 'bytes', width: 100 },
]

const names: Record<string, string> = { CN: '中国', US: '美国', JP: '日本', KR: '韩国', SG: '新加坡', HK: '香港', TW: '台湾', DE: '德国', GB: '英国', FR: '法国', RU: '俄罗斯', IN: '印度', CA: '加拿大', AU: '澳大利亚', BR: '巴西', NL: '荷兰', unknown: '未知' }
const countryName = (c: string) => names[c] || (c || '未知')

// 国家质心（与后端 Centroids 一致的主要国家）：GeoIP 缺坐标时回退
const centroids: Record<string, [number, number]> = {
  CN: [104.19, 35.86], US: [-95.71, 37.09], JP: [138.25, 36.2], KR: [127.86, 36.5], SG: [103.82, 1.35],
  HK: [114.17, 22.32], TW: [120.96, 23.7], DE: [10.45, 51.16], GB: [-3.43, 55.37], FR: [46.22, 2.21],
  RU: [105.31, 61.52], IN: [78.96, 20.59], CA: [-106.34, 56.13], AU: [133.77, -25.27], BR: [-51.92, -10.8],
  NL: [5.29, 52.13], ID: [113.92, -0.79], TH: [100.99, 15.87], VN: [108.27, 14.05], MY: [101.97, 4.21],
  PH: [121.77, 12.87], IT: [12.56, 41.87], ES: [-3.74, 40.46], TR: [35.24, 38.96], UA: [31.16, 48.37],
}
// 服务器位置：后端探测的出口 IP 坐标优先（[lon,lat]），回退本地缓存；从未探测到则不渲染红点
// （兜底画一个假位置会被误读成访客/首都标记，探测失败时宁可不显示）
const cachedServerLoc: [number, number] | null = JSON.parse(localStorage.getItem('zjp_server_loc') || 'null')
const serverPoint = ref<[number, number]>(cachedServerLoc || [116.4, 39.9])
const serverLocKnown = ref<boolean>(!!cachedServerLoc)
// 位置来源与城市名（后端 serverLocSource：manual=手动指定 online=在线反查 geoip=GeoIP 反查）。
// geoip 且无城市 = 仅国家级精度（落点是国家质心），标注里说明，避免误读为机房真实位置
const serverLocSource = ref<string>('')
const serverLocCity = ref<string>('')
const serverLabel = computed(() => {
  if (serverLocCity.value) return `服务器 · ${serverLocCity.value}`
  if (serverLocSource.value === 'manual') return '服务器（手动指定）'
  if (serverLocSource.value === 'geoip') return '服务器（GeoIP 仅国家级精度，可在「服务器位置」中校准）'
  return '服务器'
})

const realtime = ref(true)
// 实时速率：记录每条请求的到达时间，近 5s 滑窗计数 ÷ 5（req/s）。
// 统计走全部请求（含无地理信息的），不用流星列表——它有 120 条封顶且寿命 4.3s，拿它算速率会低估。
const entryTimes = ref<number[]>([])
const rate = ref(0) // 近 5s 请求速率（req/s）
// 实时流向：一次请求 = 一条「来源黄点 → 服务器」的流星（飞一次后消散）。
// SSE 逐条推送，每个请求即刻生成一条流星；请求频率由流星出现的密集程度表达，
// 不做合并计数，也不按次数缩放线宽/光点大小。总量上限防高 QPS 爆量。
const liveFlows = ref<{ label: string; coords: [number, number][]; ts: number }[]>([])
const LIVE_FLOW_CAP = 120

const handleEntry = (e: any) => {
  if (!realtime.value) return
  entryTimes.value.push(Date.now())
  const c: [number, number] | undefined = (e.lat || e.lon) ? [e.lon, e.lat] : centroids[e.country]
  if (c) {
    const label = e.city ? `${e.city}, ${countryName(e.country)}` : countryName(e.country)
    liveFlows.value.push({ label, coords: [c, serverPoint.value] as [number, number][], ts: Date.now() })
    // 超出上限时丢弃最早的流星（高 QPS 下视觉密度封顶，避免渲染爆炸）
    if (liveFlows.value.length > LIVE_FLOW_CAP) {
      liveFlows.value = liveFlows.value.slice(-LIVE_FLOW_CAP)
    }
  }
  summary.value.recent = [e, ...(summary.value.recent || [])].slice(0, 100)
  scheduleRender()
}

// 高 QPS 下逐条 setOption 会卡顿：合并到动画帧，每帧最多渲染一次
let renderQueued = false
const scheduleRender = () => {
  if (renderQueued) return
  renderQueued = true
  requestAnimationFrame(() => {
    renderQueued = false
    if (active) renderMap()
  })
}

// 飞线寿命清扫：超过一个动画周期的线移除（一个请求只飞一次），顺带更新请求速率
const FLOW_LIFETIME = 4300
const startSweep = () => {
  sweepTimer = setInterval(() => {
    const now = Date.now()
    const len = liveFlows.value.length
    if (len) {
      const cut = now - FLOW_LIFETIME
      const keep = liveFlows.value.filter(f => f.ts > cut)
      if (keep.length !== len) { liveFlows.value = keep; renderMap() }
    }
    // 速率统计基于 entryTimes（全部请求），流星列表有封顶且寿命只有 4.3s，不能拿来算速率
    if (entryTimes.value.length) entryTimes.value = entryTimes.value.filter(t => t > now - 5000)
    rate.value = Math.round((entryTimes.value.length / 5) * 10) / 10
  }, 500)
}

const enNames: Record<string, string> = { CN: 'China', US: 'United States', JP: 'Japan', KR: 'Korea', SG: 'Singapore', HK: 'Hong Kong', TW: 'Taiwan', DE: 'Germany', GB: 'United Kingdom', FR: 'France', RU: 'Russia', IN: 'India', CA: 'Canada', AU: 'Australia', BR: 'Brazil', NL: 'Netherlands', ID: 'Indonesia', TH: 'Thailand', VN: 'Vietnam', MY: 'Malaysia', PH: 'Philippines', IT: 'Italy', ES: 'Spain', TR: 'Turkey', UA: 'Ukraine' }
const mapName = (c: string) => enNames[c] || c

// 预加载：echarts（1.1MB chunk）与世界地图数据（1MB）在模块加载时即并行拉起，
// 不等组件挂载——串行懒加载是「地图迟迟不出来」的另一根因。结果缓存复用。
let preloadEcharts: Promise<any> | null = null
let preloadGeo: Promise<any> | null = null
const preload = () => {
  if (!preloadEcharts) preloadEcharts = import('echarts')
  if (!preloadGeo) {
    preloadGeo = fetch('/map/world.json').then(r => r.json())
      .then((geo) => { return geo })
      .catch(() => { preloadGeo = null; return null }) // 失败允许重试
  }
}
preload()

const ensureChart = async () => {
  if (chart) return
  if (initPromise) return initPromise
  initPromise = (async () => {
    const [echarts, geo] = await Promise.all([preloadEcharts, preloadGeo])
    if (!mapBox.value || !active) return // 初始化期间页面已被切走
    if (geo) echarts.registerMap('world', geo as any)
    chart = echarts.init(mapBox.value)
  })().finally(() => { initPromise = null })
  return initPromise
}

const renderMap = async (resetView = false) => {
  if (!mapBox.value || !active) return
  await ensureChart()
  if (!chart || !active) return
  // 视角保留：setOption(notMerge) 会整体替换 geo 配置，导致缩放/拖拽后的视角被复位。
  // echarts 在滚轮/拖拽漫游后会把 center/zoom 同步回 model（View.js syncBackRoamOptionToRoamHostModel），
  // 这里取回当前视角并显式带回；光标停留在地图上时，任何一次渲染都不重置缩放/位置，
  // 鼠标移出地图后则由 resetRoamView 以 resetView=true 回归默认全局视角。
  const roamPatch: Record<string, any> = {}
  if (!resetView) {
    const prevGeo = (chart.getOption() as any)?.geo?.[0]
    if (prevGeo?.zoom != null) roamPatch.zoom = prevGeo.zoom
    if (prevGeo?.center?.length === 2) roamPatch.center = prevGeo.center
  }
  const countries = (summary.value.countries || []).filter((c: any) => c.country !== 'unknown')
  const maxCount = Math.max(1, ...countries.map((c: any) => c.count))
  const shades = ['#dbe9ff', '#9dbdff', '#5c8dff', '#2f5fe0', '#1c3fd6']
  const colorFor = (n: number) => shades[Math.min(shades.length - 1, Math.floor((n / maxCount) * shades.length))]
  // 实时流向：统一线宽/亮度，频率感来自「流星出现的密集程度」本身（一次请求一条流星）
  const flows = realtime.value
    ? liveFlows.value.map(f => ({ coords: f.coords }))
    : []
  const srcPoints = realtime.value
    ? liveFlows.value.map(f => ({
        value: f.coords[0],
        name: f.label,
        symbolSize: 6,
        itemStyle: { color: '#ffd54d' },
      }))
    : []
  // 城市级聚合点（统计窗口内）：统一尺寸的标记点（数量信息放 tooltip，不用大小表达）
  const cities = (summary.value.cities || []).filter((c: any) => (c.lat || c.lon))
  const cityLabel = (c: any) => c.city ? `${c.city}, ${countryName(c.country)}` : countryName(c.country)
  const cityPoints = cities.map((c: any) => ({
    value: [c.lon, c.lat],
    name: cityLabel(c),
    cityLabel: cityLabel(c),
    count: c.count,
    bytes: c.bytes,
    symbolSize: 7,
    itemStyle: { color: '#5c8dff', opacity: 0.85, borderColor: 'rgba(255,255,255,.7)', borderWidth: 1 },
  }))
  chart.setOption({
    tooltip: {
      show: true,
      formatter: (p: any) => {
        if (p.data?.cityLabel !== undefined) {
          return `${p.data.cityLabel}<br/>${p.data.count} 次请求 · ${fmtBytes(p.data.bytes)}`
        }
        return p.name || ''
      },
    },
    // 深浅主题各自适配的底图配色（zoom/center 回带用户漫游后的视角）
    geo: {
      map: 'world', roam: true,
      ...roamPatch,
      itemStyle: { areaColor: ui.isDark ? '#26303f' : '#eef2f7', borderColor: ui.isDark ? '#3b4757' : '#c0cdd8' },
      emphasis: { label: { show: false }, itemStyle: { areaColor: ui.isDark ? '#3d5680' : '#7c9cff' } },
      regions: countries.map((c: any) => ({ name: mapName(c.country), itemStyle: { areaColor: colorFor(c.count) } })),
    },
    series: [
      {
        // 数据流：沿线流动的圆形光点带短拖尾（数据流点效果）
        type: 'lines', coordinateSystem: 'geo', zlevel: 2,
        effect: { show: true, period: 4, trailLength: 0.2, symbol: 'circle', symbolSize: 6, color: '#ffd54d' },
        lineStyle: { color: '#3b82f6', width: 1.2, opacity: 0.35, curveness: 0.35 },
        data: flows,
      },
      {
        // 城市级聚合点：统计窗口内各城市请求量（静态蓝点，大小 = 请求量）
        type: 'scatter', coordinateSystem: 'geo', zlevel: 2,
        data: cityPoints,
      },
      {
        // 请求来源点：黄色小数据点（实时请求落点）
        type: 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
        rippleEffect: { scale: 2.2, brushType: 'stroke' },
        itemStyle: { color: '#ffd54d' },
        data: srcPoints,
      },
      {
        // 服务器位置：红色小点（缩小，避免喧宾夺主）；探测不到位置时不渲染
        type: 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
        symbolSize: 7, rippleEffect: { scale: 2 },
        itemStyle: { color: '#ff5722' },
        data: serverLocKnown.value ? [{ value: serverPoint.value, name: serverLabel.value }] : [],
      },
    ],
  }, true)
}

// 鼠标移出地图后视角复位：漫游出的缩放/位置只在光标停留在地图上时保留，
// 移出即回归默认全局视角，避免拖拽/缩放状态残留到下一次查看。
// 未漫游过（zoom 仍为默认 1 且未写入 center）则跳过，避免路过时的多余重渲染。
const resetRoamView = () => {
  if (!active || !chart) return
  const geo = (chart.getOption() as any)?.geo?.[0]
  const roamed = (geo?.zoom != null && Math.abs(geo.zoom - 1) > 1e-6) || geo?.center?.length === 2
  if (roamed) renderMap(true)
}

const load = async () => {
  summary.value = await get('/api/logmap/summary', { minutes: minutes.value, recent: 50 })
  geoLoaded.value = summary.value.geoLoaded
  if (summary.value.serverLoc?.length === 2) {
    const [lon, lat] = summary.value.serverLoc
    serverLocKnown.value = true
    serverLocSource.value = summary.value.serverLocSource || 'geoip'
    serverLocCity.value = summary.value.serverLocCity || ''
    const cur = serverPoint.value
    if (cur[0] !== lon || cur[1] !== lat) {
      serverPoint.value = [lon, lat]
      localStorage.setItem('zjp_server_loc', JSON.stringify(serverPoint.value))
      renderMap()
    }
  } else if (summary.value.serverLocSource === '') {
    // 后端明确说「未探测到」：清掉本地过期缓存并隐藏红点（后端探测失败宁可不显示）
    if (serverLocKnown.value) {
      serverLocKnown.value = false
      serverLocSource.value = ''
      serverLocCity.value = ''
      localStorage.removeItem('zjp_server_loc')
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

// 服务器位置手动指定：弹窗打开时拉取当前配置（手动值 + 实际生效值/来源），保存/清除后刷新摘要
const locPopOpen = ref(false)
const hasManual = ref(false)
const manualLon = ref<number | null>(null)
const manualLat = ref<number | null>(null)
const serverLocText = ref('探测中…')
const SOURCE_TEXT: Record<string, string> = { manual: '手动指定', online: '在线反查', geoip: 'GeoIP 反查' }
const loadServerLocInfo = async () => {
  try {
    const info = await get('/api/logmap/serverloc')
    hasManual.value = !!info.manual
    if (info.manual) { manualLon.value = info.manual.lon; manualLat.value = info.manual.lat }
    if (info.loc) {
      serverLocText.value = `(${info.loc.lat.toFixed(3)}, ${info.loc.lon.toFixed(3)}) · ${SOURCE_TEXT[info.source] || info.source}${info.city ? ' · ' + info.city : ''}`
    } else {
      serverLocText.value = '未探测到'
    }
  } catch { /* 拦截器提示 */ }
}
watch(locPopOpen, (v) => { if (v) loadServerLocInfo() })
const saveServerLoc = async () => {
  if (manualLon.value == null || manualLat.value == null) return
  try {
    await post('/api/logmap/serverloc', { lon: manualLon.value, lat: manualLat.value })
    message.success('已保存服务器位置'); hasManual.value = true; load()
  } catch { /* 拦截器提示 */ }
}
const clearServerLoc = async () => {
  try {
    await post('/api/logmap/serverloc', { clear: true })
    message.success('已清除，恢复自动探测'); hasManual.value = false; load()
  } catch { /* 拦截器提示 */ }
}
watch(() => ui.isDark, () => renderMap())
watch(() => props.height, () => chart?.resize())

/**
 * 生命周期统一收敛到幂等 start/stop：组件既可能挂在普通页面，也可能作为
 * keep-alive 缓存页（仪表盘/请求地图页）的后代，onMounted 与 onActivated 都会触发。
 */
// SSE 实时流：entry 逐条即时推送；summary 推送跟随所选统计窗口，窗口切换时重连
const onStreamMsg = (m: any) => {
  if (m.type === 'summary') { summary.value = m.data; renderMap() }
  else if (m.type === 'entry') handleEntry(m.data)
}
const connectStream = () => sseTopic(`/api/logmap/stream?minutes=${minutes.value}`, onStreamMsg)
watch(minutes, () => { if (active) { stopStream?.(); stopStream = connectStream() } })

const start = () => {
  if (active) return
  active = true
  load()
  stopStream = connectStream()
  timer = setInterval(load, 15000)
  if (!props.compact) { loadTree(); treeTimer = setInterval(loadTree, 60000) }
  startSweep()
  nextTick(() => {
    if (mapBox.value && !resizeObserver) {
      resizeObserver = new ResizeObserver((entries) => {
        // keep-alive 切走时组件 DOM 脱离文档、容器瞬间 0 尺寸，此时 resize 会把
        // echarts 画布缩到 0×0，返回页面后画布保持空白（canvas width=0）。
        // 只在容器恢复真实尺寸后才 resize，0 尺寸一律忽略。
        const r = entries[entries.length - 1]?.contentRect
        if (r && r.width > 0 && r.height > 0) chart?.resize()
      })
      resizeObserver.observe(mapBox.value)
    }
    // 返回 keep-alive 缓存页时 RO 可能不再触发（离开时已按 0 尺寸收缩），
    // 双 rAF 等布局完成后再手动恢复一次画布尺寸
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (active && mapBox.value && mapBox.value.clientWidth > 0 && mapBox.value.clientHeight > 0) chart?.resize()
    }))
  })
}
const stop = () => {
  active = false
  stopStream?.(); stopStream = null
  clearInterval(timer); timer = null
  clearInterval(sweepTimer); sweepTimer = null
  clearInterval(treeTimer); treeTimer = null
}
onMounted(start)
onActivated(start)
onDeactivated(stop)
onUnmounted(() => { stop(); resizeObserver?.disconnect(); resizeObserver = null; chart?.dispose(); chart = null })
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
/* !important 必须保留：antd Space 的 wrap 模式会给容器注入内联 margin-bottom:-8px
   （补偿换行行的间隙），内联样式会压过普通 CSS，导致地图盒上移 8px 盖住工具条 */
.map-toolbar { margin-bottom: 12px !important; }
.stats-range { display: flex; align-items: center; gap: 10px; margin: 8px 0 4px; }
.map-box { width: 100%; height: var(--map-height); }
/* fill 模式：组件撑满父容器（如仪表盘地图卡），地图区弹性伸缩，溢出交给父容器内部滚动 */
.request-map.fill { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.request-map.fill .map-box { flex: 1 1 auto; height: auto; min-height: 220px; }
@media (max-width: 767px) { .map-box { height: min(var(--map-height), 320px); } }
.details-collapse { margin-top: 8px; }
.details-title { font-weight: 600; }
.details-hint { margin-left: 12px; font-weight: 400; }
:deep(.ant-collapse-header) { align-items: center !important; }
:deep(.ant-collapse-content-box) { padding: 4px 0 0 !important; }
/* 服务器位置弹窗：固定宽度 + 经纬度输入并排 */
.loc-pop { width: 264px; }
.loc-inputs { display: flex; gap: 8px; margin-top: 8px; }
.loc-inputs .ant-input-number { flex: 1; min-width: 0; }
</style>
