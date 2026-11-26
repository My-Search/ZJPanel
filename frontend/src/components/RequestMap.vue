<template>
  <div class="request-map" :class="{ fill }">
    <a-space class="map-toolbar" wrap>
      <a-tag :color="geoLoaded ? 'green' : 'orange'">{{ geoLoaded ? 'GeoIP 已加载' : 'GeoIP 未加载' }}</a-tag>
      <a-tag color="blue">实时 {{ rate }} req/s</a-tag>
      <!-- compact（仪表盘卡片）只保留 GeoIP 状态与实时请求数；完整地图页保留管理工具 -->
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

    <div class="map-wrap" @mouseleave="onMapLeave">
      <div ref="mapBox" class="map-box" :style="{ '--map-height': height + 'px' }"></div>

      <!-- 悬停流向 → 冻结画面 + 实时请求树。数据只来自本页 SSE 内存缓冲，不落库 -->
      <div v-if="inspectRoute" class="flow-inspect">
        <div class="fi-head">
          <div class="fi-title">
            <span class="fi-pulse"></span>
            <span class="fi-city">{{ inspectRoute.label }}</span>
            <a-tag color="gold" class="fi-tag">流向已暂停</a-tag>
          </div>
          <a-button type="text" size="small" @click="closeInspect">继续流动</a-button>
        </div>
        <div class="fi-meta">
          站点 {{ inspectSiteCount }} · 路径 {{ inspectPathCount }} · 请求 {{ inspectEntries.length }}
          <span v-if="inspectLiveTotal" class="fi-live">悬停后 +{{ inspectLiveTotal }}</span>
        </div>
        <div class="fi-tip">仅本页内存缓冲（最近 {{ captureCap }} 条 SSE），不落库；新请求实时追加</div>
        <div class="fi-tree">
          <a-tree
            v-if="inspectTree.length"
            :tree-data="inspectTree"
            :expanded-keys="inspectExpanded"
            :selectable="true"
            block-node
            @expand="(keys: any) => (inspectExpanded = keys)"
            @select="onInspectSelect"
          />
          <a-empty v-else description="等待该流向的请求…" :image-style="{ height: '40px' }" />
        </div>
      </div>
    </div>

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

    <!-- 请求详情：字段全部来自 SSE 推送，不查库 -->
    <a-drawer v-model:open="detailOpen" title="请求详情" placement="right" :width="460">
      <template v-if="detailEntry">
        <div class="det-line">
          <a-tag :color="statusColor(Math.floor(detailEntry.status / 100) + 'xx')">{{ detailEntry.status }}</a-tag>
          <span class="det-method">{{ detailEntry.method || '-' }}</span>
          <span class="det-path">{{ detailEntry.path || '-' }}</span>
        </div>
        <a-descriptions :column="1" size="small" bordered class="det-desc">
          <a-descriptions-item label="时间">{{ fmtTime(detailEntry.time) }}</a-descriptions-item>
          <a-descriptions-item label="来源 IP">{{ detailEntry.ip || '-' }}</a-descriptions-item>
          <a-descriptions-item label="地区">{{ detailEntry.city ? detailEntry.city + ', ' : '' }}{{ countryName(detailEntry.country) }}</a-descriptions-item>
          <a-descriptions-item label="站点">{{ detailEntry.site }}</a-descriptions-item>
          <a-descriptions-item label="方法">{{ detailEntry.method || '-' }}</a-descriptions-item>
          <a-descriptions-item label="路径">
            <a-typography-text copyable class="det-path-full">{{ detailEntry.path || '-' }}</a-typography-text>
          </a-descriptions-item>
          <a-descriptions-item label="状态码">{{ detailEntry.status }}</a-descriptions-item>
          <a-descriptions-item label="响应大小">{{ fmtBytes(detailEntry.bytes) }}</a-descriptions-item>
          <a-descriptions-item label="流向">{{ inspectRoute ? inspectRoute.label : '-' }}</a-descriptions-item>
        </a-descriptions>
        <div class="det-block">
          <div class="det-label">Referer</div>
          <a-typography-text v-if="detailEntry.referer" copyable class="det-value">{{ detailEntry.referer }}</a-typography-text>
          <span v-else class="dim">（日志未记录）</span>
        </div>
        <div class="det-block">
          <div class="det-label">User-Agent</div>
          <a-typography-text v-if="detailEntry.ua" copyable class="det-value">{{ detailEntry.ua }}</a-typography-text>
          <span v-else class="dim">（日志未记录）</span>
        </div>
      </template>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, sseTopic, fmtBytes, fmtTime, upload } from '../api'
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
const chartReady = ref(false)
const mapBox = ref<HTMLElement | null>(null)
const detailsOpen = ref<string[]>([]) // 默认收纳详细信息
let chart: any = null
let initPromise: Promise<void> | null = null
let active = false
let stopStream: (() => void) | null = null
let timer: any = null
let sweepTimer: any = null
let resizeObserver: ResizeObserver | null = null
// 增量渲染用：只在底图/暂停态变化时重建，其余帧只换 series 数据。
// 这样缩放/拖拽后的视角和点线锚点不会被 setOption(notMerge) 冲掉。
let lastPaused: boolean | null = null
let lastGeoKey = ''

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

/**
 * 悬停明细的数据链路（全部只在前端内存，不写数据库）：
 * SSE entry → captureRing（去重环形缓冲，含 UA/Referer 明细）→ 悬停某条流向时按 routeKey
 * 截取该来源的历史请求 + 悬停后新请求，按「站点 → 路径 → 请求」组装成树；点叶子看详情。
 * routeKey 用来源坐标 2 位小数的 bucket（与后端城市聚合一致），同一城市的多条流星归为一条流向。
 */
type LiveEntry = {
  id: number
  ip: string
  country: string
  city: string
  lat: number
  lon: number
  site: string
  status: number
  bytes: number
  method: string
  path: string
  time: string
  ua: string
  referer: string
  ts: number
  key: string
  point: [number, number] | null
  dedupe: string
}

type LiveFlow = {
  id: number
  key: string
  label: string
  coords: [number, number][]
  ts: number
}

type InspectNode = {
  key: string
  title: string
  selectable?: boolean
  isLeaf?: boolean
  reqId?: number
  children?: InspectNode[]
}

const CAPTURE_MAX = 2000
const INSPECT_SEED_CAP = 300 // 悬停瞬间回溯的请求条数上限（新→旧取最近）
const INSPECT_MAX = 500 // 悬停期间面板最多保留的请求条数（防长时间悬停无上限增长）
const captureCap = CAPTURE_MAX

let nextEntryId = 1
const pointOf = (e: any): [number, number] | null => (e.lat || e.lon) ? [e.lon, e.lat] : (centroids[e.country] || null)
const routeKeyOf = (e: any, pt: [number, number] | null) =>
  pt ? `${e.country || 'unknown'}|${e.city || ''}|${pt[0].toFixed(2)},${pt[1].toFixed(2)}` : ''
const labelOf = (e: any) => e.city ? `${e.city}, ${countryName(e.country)}` : countryName(e.country)
const clockOf = (t: string) => {
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleTimeString('zh-CN', { hour12: false })
}
// live=true：实时 SSE 条目，ts 用接收时刻驱动流星寿命；false：摘要回填，ts 用日志时间。
const normalizeEntry = (raw: any, live: boolean): LiveEntry => {
  const pt = pointOf(raw)
  const parsed = raw.time ? new Date(raw.time).getTime() : NaN
  return {
    id: nextEntryId++,
    ip: raw.ip || '',
    country: raw.country || 'unknown',
    city: raw.city || '',
    lat: raw.lat || 0,
    lon: raw.lon || 0,
    site: raw.site || 'default',
    status: raw.status || 0,
    bytes: raw.bytes || 0,
    method: raw.method || '',
    path: raw.path || '',
    time: raw.time || new Date().toISOString(),
    ua: raw.ua || '',
    referer: raw.referer || '',
    ts: live || !Number.isFinite(parsed) ? Date.now() : parsed,
    key: routeKeyOf(raw, pt),
    point: pt,
    dedupe: `${raw.ip}|${raw.time}|${raw.status}|${raw.method}|${raw.path}|${raw.bytes}`,
  }
}

// 捕获环形缓冲：页面打开期间收到的全部请求（含摘要回填），按 ip+时间+路径去重。
const captureRing: (LiveEntry | null)[] = new Array(CAPTURE_MAX).fill(null)
const captureKeys = new Set<string>()
let captureHead = 0
let captureCount = 0
const pushCapture = (e: LiveEntry) => {
  if (!e.key || captureKeys.has(e.dedupe)) return
  const old = captureRing[captureHead]
  if (old) captureKeys.delete(old.dedupe)
  captureRing[captureHead] = e
  captureHead = (captureHead + 1) % CAPTURE_MAX
  if (captureCount < CAPTURE_MAX) captureCount++
}
// 新→旧
const captureSnapshot = (): LiveEntry[] => {
  const out: LiveEntry[] = []
  for (let i = 0; i < captureCount; i++) {
    out.push(captureRing[(captureHead - 1 - i + CAPTURE_MAX) % CAPTURE_MAX]!)
  }
  return out
}
// 摘要回填：页面刚打开/SSE 断线期间的历史请求也能被悬停面板看到（按去重键合并）
const seedCapture = (list: any[] | undefined) => {
  if (!list?.length) return
  for (let i = list.length - 1; i >= 0; i--) {
    const e = normalizeEntry(list[i], false)
    if (e.key) pushCapture(e)
  }
}

const liveFlows = ref<LiveFlow[]>([])
const inspectRoute = ref<{ key: string; label: string; coords: [number, number][] } | null>(null)
const inspectEntries = ref<LiveEntry[]>([])
const frozenFlows = ref<LiveFlow[]>([])
const inspectFocusId = ref(0)
const inspectLiveTotal = ref(0)
const inspectExpanded = ref<(string | number)[]>([])
const detailOpen = ref(false)
const detailEntry = ref<LiveEntry | null>(null)
let inspectPending: LiveEntry[] = []
const expandedSeen = new Set<string>()

const flowFromEntry = (e: LiveEntry, ts = Date.now()): LiveFlow | null =>
  e.point ? { id: e.id, key: e.key, label: labelOf(e), coords: [e.point, serverPoint.value], ts } : null

const inspectTree = computed<InspectNode[]>(() => {
  const sites = new Map<string, { count: number; paths: Map<string, { method: string; path: string; count: number; reqs: LiveEntry[] }> }>()
  for (const e of inspectEntries.value) {
    let s = sites.get(e.site)
    if (!s) { s = { count: 0, paths: new Map() }; sites.set(e.site, s) }
    s.count++
    const clean = (e.path || '-').split('?')[0] || '/'
    const pk = `${e.method} ${clean}`
    let p = s.paths.get(pk)
    if (!p) { p = { method: e.method || '-', path: clean, count: 0, reqs: [] }; s.paths.set(pk, p) }
    p.count++
    p.reqs.push(e)
  }
  const focus = inspectEntries.value.find((e) => e.id === inspectFocusId.value)
  const focusSite = focus?.site || ''
  const focusPathKey = focus
    ? `path:${focus.site}:${focus.method || '-'}:${(focus.path || '-').split('?')[0] || '/'}`
    : ''
  return [...sites.entries()]
    // 悬停请求所属站点置顶：先让用户看到“这条线对应的请求”，其余站点按量排序
    .sort((a, b) => {
      if (focusSite) {
        if (a[0] === focusSite && b[0] !== focusSite) return -1
        if (b[0] === focusSite && a[0] !== focusSite) return 1
      }
      return b[1].count - a[1].count
    })
    .map(([site, s]) => ({
      key: 'site:' + site,
      title: `${site} · ${s.count} 次`,
      selectable: false,
      children: [...s.paths.values()]
        // 悬停请求所在路径置顶，保证首屏直接看到悬停那一刻的请求
        .sort((a, b) => {
          if (focusPathKey) {
            const ak = `path:${site}:${a.method}:${a.path}`
            const bk = `path:${site}:${b.method}:${b.path}`
            if (ak === focusPathKey && bk !== focusPathKey) return -1
            if (bk === focusPathKey && ak !== focusPathKey) return 1
          }
          return b.count - a.count
        })
        .map((p) => ({
          key: `path:${site}:${p.method}:${p.path}`,
          title: `${p.method} ${p.path} · ${p.count} 次`,
          selectable: false,
          children: (() => {
            const pathReqs = [...p.reqs].reverse()
            const shown = pathReqs.slice(0, 50)
            // 悬停那一条如果被单路径 50 条上限挤出，补回末尾，保证首屏能看到「悬停请求」
            const focusReq = pathReqs.find((e) => e.id === inspectFocusId.value)
            if (focusReq && !shown.some((e) => e.id === focusReq.id)) {
              if (shown.length >= 50) shown[shown.length - 1] = focusReq
              else shown.push(focusReq)
            }
            return shown.map((e) => ({
              key: 'req:' + e.id,
              title: `${clockOf(e.time)} · ${e.status} · ${e.ip}${e.id === inspectFocusId.value ? ' · 悬停请求' : ''}`,
              isLeaf: true,
              selectable: true,
              reqId: e.id,
            }))
          })(),
        })),
    }))
})
const inspectSiteCount = computed(() => inspectTree.value.length)
const inspectPathCount = computed(() => inspectTree.value.reduce((n, s) => n + (s.children?.length || 0), 0))

// 默认只展开站点和「悬停请求所在路径」，其余路径折叠；用户手动折叠后不反复弹开。
// 这样首屏就能看到悬停那一刻的请求，同时保留站点/路径的总次数作为分类索引。
watch(inspectTree, (nodes) => {
  const focus = inspectEntries.value.find((e) => e.id === inspectFocusId.value)
  const focusPathKey = focus
    ? `path:${focus.site}:${focus.method || '-'}:${(focus.path || '-').split('?')[0] || '/'}`
    : ''
  const added: string[] = []
  let focusFound = false
  const walk = (list: InspectNode[], depth: number) => {
    for (const n of list) {
      if (!n.children?.length) continue
      if (n.key === focusPathKey) focusFound = true
      if (!expandedSeen.has(n.key)) {
        expandedSeen.add(n.key)
        // 站点层全展开；路径层只弹悬停请求所在的那一条
        if (depth === 0 || (depth === 1 && n.key === focusPathKey)) added.push(n.key)
      }
      walk(n.children, depth + 1)
    }
  }
  walk(nodes, 0)
  // 悬停请求被单路径 50 条上限截掉时，至少展开第一条路径，避免首屏空树
  if (!focusFound) {
    const fallback = nodes[0]?.children?.[0]?.key
    if (fallback && !inspectExpanded.value.includes(fallback)) added.push(fallback)
  }
  if (added.length) inspectExpanded.value = [...new Set([...inspectExpanded.value, ...added])]
})

const onInspectSelect = (_keys: any, info: any) => {
  const id = info?.node?.reqId
  if (!id) return
  const e = inspectEntries.value.find((x) => x.id === id)
  if (e) { detailEntry.value = e; detailOpen.value = true }
}

// 实时速率：记录每条请求的到达时间，近 5s 滑窗计数 ÷ 5（req/s）。
// 统计走全部请求（含无地理信息的），不用流星列表——它有 120 条封顶且寿命 4.3s，拿它算速率会低估。
const entryTimes = ref<number[]>([])
const rate = ref(0) // 近 5s 请求速率（req/s）
// 实时流向：一次请求 = 一条「来源黄点 → 服务器」的流星（飞一次后消散）。
// SSE 逐条推送，每个请求即刻生成一条流星；请求频率由流星出现的密集程度表达，
// 不做合并计数，也不按次数缩放线宽/光点大小。总量上限防高 QPS 爆量。
const LIVE_FLOW_CAP = 120

const handleEntry = (e: any) => {
  const entry = normalizeEntry(e, true)
  pushCapture(entry) // SSE 到达即进本地缓冲（悬停明细的数据源）
  entryTimes.value.push(Date.now())
  if (inspectRoute.value) {
    // 悬停期间画面冻结：不新增流星，保证悬停目标稳定；该流向的新请求实时追加进树
    if (entry.key && entry.key === inspectRoute.value.key) {
      inspectPending.push(entry)
      inspectLiveTotal.value++
    }
  } else {
    const f = flowFromEntry(entry)
    if (f) {
      liveFlows.value.push(f)
      // 超出上限时丢弃最早的流星（高 QPS 下视觉密度封顶，避免渲染爆炸）
      if (liveFlows.value.length > LIVE_FLOW_CAP) {
        liveFlows.value = liveFlows.value.slice(-LIVE_FLOW_CAP)
      }
    }
  }
  summary.value.recent = [e, ...(summary.value.recent || [])].slice(0, 100)
  if (!inspectRoute.value) scheduleRender()
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

// 飞线寿命清扫：超过一个动画周期的线移除（一个请求只飞一次），顺带更新请求速率；
// 悬停冻结期间不清理流星、不重绘地图，只把新请求批量灌进明细树（500ms 一帧，防高 QPS 重渲染）。
const FLOW_LIFETIME = 4300
const startSweep = () => {
  sweepTimer = setInterval(() => {
    const now = Date.now()
    if (inspectRoute.value) {
      if (inspectPending.length) {
        const merged = inspectEntries.value.concat(inspectPending)
        inspectPending = []
        if (merged.length > INSPECT_MAX) merged.splice(0, merged.length - INSPECT_MAX)
        inspectEntries.value = merged
      }
    } else {
      const len = liveFlows.value.length
      if (len) {
        const cut = now - FLOW_LIFETIME
        const keep = liveFlows.value.filter(f => f.ts > cut)
        if (keep.length !== len) { liveFlows.value = keep; renderMap() }
      }
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

// 当前视角是否已偏离默认全球视角：zoom 不等于 1 或设置了 center。默认 center 为 null。
const isGeoRoamed = () => {
  const g = (chart?.getOption() as any)?.geo?.[0]
  const z = Number(g?.zoom)
  return (Number.isFinite(z) && Math.abs(z - 1) > 0.001) || (Array.isArray(g?.center) && g.center.length === 2)
}

const ensureChart = async () => {
  if (chart) return
  if (initPromise) return initPromise
  initPromise = (async () => {
    const [echarts, geo] = await Promise.all([preloadEcharts, preloadGeo])
    if (!mapBox.value || !active) return // 初始化期间页面已被切走
    if (geo) echarts.registerMap('world', geo as any)
    chart = echarts.init(mapBox.value)
    // 悬停流星线 → 暂停流向并展开该来源的请求树（点击空处/继续按钮恢复）
    chart.on('mouseover', onChartOver)
  })().finally(() => { initPromise = null })
  return initPromise
}

const renderMap = async (force = false, reset = false) => {
  if (!mapBox.value || !active) return
  if (inspectRoute.value && !force) return // 冻结期间其余刷新（摘要/定时 load/主题）一律不重绘，悬停目标不丢
  await ensureChart()
  if (!chart || !active) return
  const countries = (summary.value.countries || []).filter((c: any) => c.country !== 'unknown')
  const maxCount = Math.max(1, ...countries.map((c: any) => c.count))
  const shades = ['#dbe9ff', '#9dbdff', '#5c8dff', '#2f5fe0', '#1c3fd6']
  const colorFor = (n: number) => shades[Math.min(shades.length - 1, Math.floor((n / maxCount) * shades.length))]
  // 实时流向：统一线宽/亮度，频率感来自「流星出现的密集程度」本身（一次请求一条流星）。
  // 悬停冻结时切到静态快照：目标流速高亮、其余压暗，effect 关掉 = 画面真正停住。
  const paused = !!inspectRoute.value
  const flowSrc = paused ? frozenFlows.value : liveFlows.value
  const flowKey = inspectRoute.value?.key
  const flows = flowSrc.map(f => paused
    ? {
        name: f.label,
        coords: f.coords,
        lineStyle: f.key === flowKey
          ? { color: '#fbbf24', width: 2.6, opacity: 1, shadowBlur: 10, shadowColor: 'rgba(251,191,36,.5)' }
          : { color: '#3b82f6', width: 1, opacity: 0.07 },
      }
    : { name: f.label, coords: f.coords })
  // 来源点按「国家|城市|坐标桶」聚合：同一来源城市的一批请求只留一个黄点/一圈涟漪。
  // 之前一次请求一个 effectScatter 点，同城几十条请求的涟漪叠在一起就成了黄色光斑。
  const srcMap = new Map<string, { value: [number, number]; name: string; flowCount: number; focus: boolean }>()
  for (const f of flowSrc) {
    let p = srcMap.get(f.key)
    if (!p) {
      p = { value: f.coords[0], name: f.label, flowCount: 0, focus: false }
      srcMap.set(f.key, p)
    }
    p.flowCount++
    if (f.key === flowKey) p.focus = true
  }
  const srcPoints = [...srcMap.values()].map(p => ({
    value: p.value,
    name: p.name,
    flowCount: p.flowCount,
    symbolSize: p.focus ? 8 : 6,
    itemStyle: { color: p.focus ? '#fbbf24' : '#ffd54d' },
  }))
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
  const tooltip = {
    show: true,
    formatter: (p: any) => {
      if (p.data?.cityLabel !== undefined) {
        return `${p.data.cityLabel}<br/>${p.data.count} 次请求 · ${fmtBytes(p.data.bytes)}`
      }
      if (p.data?.flowCount != null) {
        return `${p.name}<br/>${p.data.flowCount} 条实时流向`
      }
      return p.name || ''
    },
  }
  const geo = {
    map: 'world', roam: true,
    itemStyle: { areaColor: ui.isDark ? '#26303f' : '#eef2f7', borderColor: ui.isDark ? '#3b4757' : '#c0cdd8' },
    emphasis: { label: { show: false }, itemStyle: { areaColor: ui.isDark ? '#3d5680' : '#7c9cff' } },
    regions: countries.map((c: any) => ({ name: mapName(c.country), itemStyle: { areaColor: colorFor(c.count) } })),
  }
  const series = [
    {
      id: 'flows',
      // 数据流：沿线流动的圆形光点带短拖尾（数据流点效果）；悬停冻结时关闭 effect，留下静态高亮线
      type: 'lines', coordinateSystem: 'geo', zlevel: 2, silent: paused,
      effect: { show: !paused, period: 4, trailLength: 0.2, symbol: 'circle', symbolSize: 6, color: '#ffd54d' },
      lineStyle: { color: '#3b82f6', width: 1.2, opacity: 0.35, curveness: 0.35 },
      emphasis: { disabled: true }, // 高亮由数据项 lineStyle 控制，避免 hover 过渡与冻结渲染互相打架
      data: flows,
    },
    {
      id: 'cities',
      // 城市级聚合点：统计窗口内各城市请求量（静态蓝点，大小 = 请求量）
      type: 'scatter', coordinateSystem: 'geo', zlevel: 2,
      data: cityPoints,
    },
    {
      id: 'sources',
      // 请求来源点：同一来源城市只画一个；涟漪收敛到 1.7 屏，避免同城叠成光斑
      type: paused ? 'scatter' : 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
      ...(paused ? {} : { rippleEffect: { scale: 1.7, brushType: 'stroke', number: 3 } }),
      itemStyle: { color: '#ffd54d' },
      data: srcPoints,
    },
    {
      id: 'server',
      // 服务器位置：红色小点（缩小，避免喧宾夺主）；探测不到位置时不渲染
      type: paused ? 'scatter' : 'effectScatter', coordinateSystem: 'geo', zlevel: 3,
      symbolSize: 7,
      ...(paused ? {} : { rippleEffect: { scale: 1.8, brushType: 'stroke', number: 3 } }),
      itemStyle: { color: '#ff5722' },
      data: serverLocKnown.value ? [{ value: serverPoint.value, name: serverLabel.value }] : [],
    },
  ]
  const geoKey = `${ui.isDark ? 'd' : 'l'}|${countries.map((c: any) => `${c.country}:${c.count}`).join(',')}`
  if (!chartReady.value || reset) {
    // 首次/手动复位：整图替换（不带 center/zoom）回到默认全球视角
    chart.setOption({ tooltip, geo, series }, { notMerge: true })
    chartReady.value = true
  } else {
    if (paused !== lastPaused) {
      // 冻结态要切换 series 类型（effectScatter → scatter），只替换 series，不动 geo/视角
      chart.setOption({ series }, { replaceMerge: ['series'] })
    } else {
      // 常规刷新只换数据：ECharts 合并更新，保留当前缩放/拖拽视角，点线锚点不漂移
      chart.setOption({ series })
    }
    if (geoKey !== lastGeoKey) {
      // 只更新底图配色/地区热度，不带 map/roam/center/zoom，避免把当前视角顶回默认
      chart.setOption({ geo: { itemStyle: geo.itemStyle, emphasis: geo.emphasis, regions: geo.regions } })
    }
  }
  lastPaused = paused
  lastGeoKey = geoKey
}

// 鼠标移出地图后自动回到全球视角；没有偏移时不重绘，避免无谓闪烁
const resetView = () => {
  if (!chartReady.value) return
  renderMap(true, true)
}
const onMapLeave = () => {
  if (!chartReady.value) return
  if (isGeoRoamed()) resetView()
}

// 悬停一条流星线：冻结当前画面（快照），回溯该来源的历史请求，并开始接收后续新请求。
const openInspect = (f: LiveFlow) => {
  if (inspectRoute.value?.key === f.key) return
  if (!inspectRoute.value) frozenFlows.value = liveFlows.value.slice()
  const matching = captureSnapshot().filter(e => e.key === f.key).slice(0, INSPECT_SEED_CAP).reverse()
  inspectEntries.value = matching
  inspectPending = []
  inspectLiveTotal.value = 0
  inspectFocusId.value = f.id
  inspectRoute.value = { key: f.key, label: f.label, coords: f.coords }
  inspectExpanded.value = []
  expandedSeen.clear()
  renderMap(true)
}

const resetInspect = () => {
  inspectRoute.value = null
  inspectEntries.value = []
  inspectPending = []
  inspectLiveTotal.value = 0
  inspectFocusId.value = 0
  frozenFlows.value = []
  detailOpen.value = false
  detailEntry.value = null
}

// 恢复流动：用本地缓冲里仍应「活着」的请求重建流星，暂停期间到达的请求不会凭空消失。
const closeInspect = () => {
  if (!inspectRoute.value) return
  resetInspect()
  const now = Date.now()
  liveFlows.value = captureSnapshot()
    .filter(e => e.point && now - e.ts < FLOW_LIFETIME)
    .slice(0, LIVE_FLOW_CAP)
    .map(e => flowFromEntry(e, e.ts))
    .filter((f): f is LiveFlow => !!f)
  renderMap()
}

// ECharts 的 lines 系列可命中（默认非 silent）：params.dataIndex 对应当前渲染数组（实时/冻结快照二选一）
const onChartOver = (p: any) => {
  if (p?.seriesType !== 'lines') return
  const list = inspectRoute.value ? frozenFlows.value : liveFlows.value
  const f = list[p.dataIndex]
  if (f?.key) openInspect(f)
}

const load = async () => {
  summary.value = await get('/api/logmap/summary', {
    minutes: minutes.value,
    recent: 50,
    detail: 1, // 仪表盘与地图页都要能悬停查看请求明细（UA/Referer 随 recent 返回）
  })
  seedCapture(summary.value.recent)
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
// SSE 实时流：entry 逐条即时推送；summary 推送跟随所选统计窗口，窗口切换时重连。
// 仪表盘与地图页都带 detail=1：悬停流向后的请求树/详情抽屉需要 UA/Referer。
const onStreamMsg = (m: any) => {
  if (m.type === 'summary') {
    summary.value = m.data
    seedCapture(m.data.recent) // 断线重连/页面刚打开时补齐历史请求
    renderMap()
  } else if (m.type === 'entry') handleEntry(m.data)
}
const connectStream = () => sseTopic(
  `/api/logmap/stream?minutes=${minutes.value}&detail=1`,
  onStreamMsg,
)
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
  resetInspect() // 页面切走时收起明细，避免 keep-alive 回来后还挂着旧冻结画面
}
onMounted(start)
onActivated(start)
onDeactivated(stop)
onUnmounted(() => { stop(); resizeObserver?.disconnect(); resizeObserver = null; chart?.dispose(); chart = null; chartReady.value = false })
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
/* !important 必须保留：antd Space 的 wrap 模式会给容器注入内联 margin-bottom:-8px
   （补偿换行行的间隙），内联样式会压过普通 CSS，导致地图盒上移 8px 盖住工具条 */
.map-toolbar { margin-bottom: 12px !important; }
.stats-range { display: flex; align-items: center; gap: 10px; margin: 8px 0 4px; }
.map-wrap { position: relative; width: 100%; }
.map-box { width: 100%; height: var(--map-height); }
/* fill 模式：组件撑满父容器（如仪表盘地图卡），地图区弹性伸缩，溢出交给父容器内部滚动 */
.request-map.fill { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.request-map.fill .map-wrap { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
.request-map.fill .map-box { flex: 1 1 auto; height: auto; min-height: 220px; }
@media (max-width: 767px) { .map-box { height: min(var(--map-height), 320px); } }

/* 悬停流向明细：覆盖在地图右侧，鼠标可移入面板继续操作（不关闭、不复位视角） */
.flow-inspect {
  position: absolute; top: 10px; right: 10px; bottom: 10px;
  width: min(380px, calc(100% - 20px));
  z-index: 10;
  display: flex; flex-direction: column;
  padding: 10px 12px 8px;
  background: var(--surface-card);
  border: 1px solid var(--border-card);
  border-radius: var(--radius-cards);
  box-shadow: var(--shadow-hover);
  overflow: hidden;
}
.fi-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.fi-title { display: flex; align-items: center; gap: 6px; min-width: 0; }
.fi-city { font-weight: 600; color: var(--color-ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fi-tag { margin-inline-end: 0; flex: none; }
.fi-pulse {
  width: 7px; height: 7px; border-radius: 50%; flex: none;
  background: #f59e0b; animation: fi-pulse 1.6s infinite;
}
@keyframes fi-pulse {
  0% { box-shadow: 0 0 0 0 rgba(245, 158, 11, .55); }
  70% { box-shadow: 0 0 0 8px rgba(245, 158, 11, 0); }
  100% { box-shadow: 0 0 0 0 rgba(245, 158, 11, 0); }
}
.fi-meta { margin-top: 6px; font-size: 12px; color: var(--color-slate); }
.fi-live { margin-left: 8px; color: #d97706; font-weight: 600; }
.fi-tip { margin: 2px 0 6px; font-size: 11px; color: var(--color-slate); }
.fi-tree { flex: 1 1 auto; min-height: 0; overflow: auto; }
.fi-tree :deep(.ant-tree) { background: transparent; }
.fi-tree :deep(.ant-tree-title) {
  display: inline-block; max-width: 100%;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: top;
}
@media (max-width: 767px) {
  .flow-inspect { left: 8px; right: 8px; top: 8px; bottom: 8px; width: auto; }
}

/* 请求详情抽屉 */
.det-line { display: flex; align-items: flex-start; gap: 8px; flex-wrap: wrap; margin-bottom: 12px; }
.det-method { font-weight: 600; color: var(--color-ink); }
.det-path { font-family: ui-monospace, Consolas, monospace; font-size: 12px; color: var(--color-slate); word-break: break-all; }
.det-desc { margin-bottom: 16px; }
.det-block { margin-top: 14px; }
.det-label { font-size: 12px; font-weight: 600; color: var(--color-slate); margin-bottom: 4px; }
.det-value, .det-path-full { font-size: 12px; word-break: break-all; }
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
