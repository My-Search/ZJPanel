<template>
  <div class="waf-page">
    <a-alert
      v-if="cfg.settings.enabled && cfg.settings.blockMode === 'observe'"
      type="warning" show-icon banner class="banner"
      message="当前为观察模式：规则只记录不封禁"
      description="先在「攻击日志」确认没有误判，再切换到拦截模式。切到拦截后命中会按阶梯时长自动封禁来源 IP（下发 nginx geo 规则）。"
    >
      <template #action>
        <a-button size="small" type="primary" danger @click="setMode('block')">切换为拦截模式</a-button>
      </template>
    </a-alert>

    <a-row :gutter="12">
      <a-col :xs="12" :md="6">
        <a-card size="small" class="stat">
          <div class="stat-label">规则（生效 / 总数）</div>
          <div class="stat-value">{{ enabledRules }}<span class="unit">/{{ cfg.rules.length }}</span></div>
          <div class="stat-sub">{{ cfg.settings.enabled ? '总开关已开启' : '总开关已关闭' }}</div>
        </a-card>
      </a-col>
      <a-col :xs="12" :md="6">
        <a-card size="small" class="stat">
          <div class="stat-label">近 1 小时命中</div>
          <div class="stat-value">{{ fmtNum(stats.lastHour) }}</div>
          <div class="stat-sub">拦截 {{ fmtNum(stats.byAction?.block) }} / 观察 {{ fmtNum(stats.byAction?.observe) }}</div>
        </a-card>
      </a-col>
      <a-col :xs="12" :md="6">
        <a-card size="small" class="stat">
          <div class="stat-label">处置模式</div>
          <div class="stat-value" :style="{ color: cfg.settings.blockMode === 'block' ? '#ef4444' : '#f59e0b' }">
            {{ cfg.settings.blockMode === 'block' ? '拦截' : '观察' }}
          </div>
          <div class="stat-sub">自动封禁：{{ cfg.settings.autoBan ? '开' : '关' }} · 阶梯 {{ ladderText }}</div>
        </a-card>
      </a-col>
      <a-col :xs="12" :md="6">
        <a-card size="small" class="stat">
          <div class="stat-label">命中来源 Top</div>
          <div class="stat-value mono">{{ stats.topIps?.[0]?.name || '-' }}</div>
          <div class="stat-sub">{{ stats.topIps?.[0]?.count ? fmtNum(stats.topIps[0].count) + ' 次' : '暂无命中' }}</div>
        </a-card>
      </a-col>
    </a-row>

    <a-tabs v-model:activeKey="tab" class="tabs">
      <a-tab-pane key="rules" tab="规则与策略">
        <a-card size="small" title="全局设置" :body-style="{ paddingTop: '12px' }">
          <a-form layout="vertical" :model="form">
            <a-row :gutter="16">
              <a-col :xs="24" :md="5">
                <a-form-item label="WAF 总开关">
                  <a-switch v-model:checked="form.enabled" checked-children="开" un-checked-children="关" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="6">
                <a-form-item label="处置模式">
                  <a-radio-group v-model:value="form.blockMode">
                    <a-radio-button value="observe">观察</a-radio-button>
                    <a-radio-button value="block">拦截</a-radio-button>
                  </a-radio-group>
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="5">
                <a-form-item label="自动封禁">
                  <a-switch v-model:checked="form.autoBan" checked-children="开" un-checked-children="关" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="4">
                <a-form-item label="累犯清零（小时）">
                  <a-input-number v-model:value="form.banResetHours" :min="1" :max="720" style="width: 100%" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="4">
                <a-form-item label="封禁阶梯（分钟，逗号分隔）">
                  <a-input v-model:value="ladderText" placeholder="10,60,1440" />
                </a-form-item>
              </a-col>
            </a-row>
            <a-row :gutter="16">
              <a-col :xs="24" :md="18">
                <a-form-item label="豁免地址（每行一个 IP / CIDR，命中一律不判定不封禁）">
                  <a-textarea v-model:value="exemptText" :rows="3" placeholder="127.0.0.1&#10;::1&#10;10.0.0.0/8" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="6">
                <a-form-item label=" ">
                  <a-space>
                    <a-button type="primary" @click="saveSettings" :loading="saving">保存并生效</a-button>
                  </a-space>
                  <div class="hint">打开面板前建议保持观察模式跑一天。</div>
                  <div class="hint">封禁走安全中心统一下发：若「WAF」页的安全策略处于观察模式，封禁只记录不生效。</div>
                </a-form-item>
              </a-col>
            </a-row>
          </a-form>
        </a-card>

        <a-card size="small" :title="`规则列表（${cfg.rules.length}）`" :body-style="{ padding: '0 8px 8px' }">
          <template #extra>
            <a-button size="small" type="primary" @click="openAdd">新增自定义规则</a-button>
          </template>
          <a-table
            :data-source="cfg.rules" :columns="ruleCols" row-key="id"
            size="small" :pagination="false" :scroll="{ y: 420 }"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'name'">
                <div class="rule-name">{{ record.name }}</div>
                <div class="rule-desc">{{ record.builtin ? record.desc : record.pattern }}</div>
              </template>
              <template v-else-if="column.key === 'category'">
                <a-tag :color="catColor(record.category)">{{ catText(record.category) }}</a-tag>
              </template>
              <template v-else-if="column.key === 'severity'">
                <a-tag :color="record.severity === 'high' ? 'red' : record.severity === 'medium' ? 'orange' : 'default'">
                  {{ record.severity === 'high' ? '高' : record.severity === 'medium' ? '中' : '低' }}
                </a-tag>
              </template>
              <template v-else-if="column.key === 'action'">
                <a-select
                  :value="record.action" size="small" style="width: 92px"
                  :options="[{ value: 'observe', label: '观察' }, { value: 'block', label: '拦截' }]"
                  @change="(v: any) => updateRule(record, record.enabled, v)"
                />
              </template>
              <template v-else-if="column.key === 'enabled'">
                <a-switch size="small" :checked="record.enabled" @change="(v: any) => updateRule(record, !!v, record.action)" />
              </template>
              <template v-else-if="column.key === 'act'">
                <a-button v-if="!record.builtin" size="small" danger type="text" @click="removeRule(record)">删除</a-button>
                <span v-else class="dim">内置</span>
              </template>
            </template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="hits" :tab="`攻击日志（${hits.length}）`">
        <a-card size="small" :body-style="{ padding: '8px 8px 0' }">
          <a-space wrap>
            <a-select v-model:value="filter.category" :options="catOptions" style="width: 150px" allow-clear placeholder="全部类别" @change="loadHits" />
            <a-input v-model:value="filter.ip" placeholder="按 IP 过滤" style="width: 180px" allow-clear @press-enter="loadHits" />
            <a-button @click="loadHits">查询</a-button>
            <span class="dim">命中证据含解码后的攻击载荷片段，可直接用于回溯；日志落盘 data/waf/hits.jsonl</span>
          </a-space>
        </a-card>
        <a-card size="small" :body-style="{ padding: '0 8px 8px' }" style="margin-top: 8px">
          <a-table
            :data-source="hits" :columns="hitCols" row-key="_k"
            size="small" :pagination="{ pageSize: 20, size: 'small' }" :scroll="{ y: 460 }"
            :locale="{ emptyText: '暂无命中，一切正常' }"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'time'">{{ fmtTime(record.time) }}</template>
              <template v-else-if="column.key === 'rule'">
                <div>{{ record.ruleName }}</div>
                <a-tag :color="catColor(record.category)">{{ catText(record.category) }}</a-tag>
                <a-tag v-if="record.count > 1" color="purple">×{{ record.count }}</a-tag>
              </template>
              <template v-else-if="column.key === 'source'">
                <div class="mono">{{ record.ip }}</div>
                <div class="dim">{{ record.site }}{{ record.country ? ' · ' + record.country : '' }}</div>
              </template>
              <template v-else-if="column.key === 'path'">
                <div class="mono ellipsis" :title="record.path">{{ record.method }} {{ record.path }}</div>
                <div v-if="record.snippet" class="snippet" :title="record.snippet">{{ record.snippet }}</div>
              </template>
              <template v-else-if="column.key === 'action'">
                <a-tag :color="record.action === 'block' ? 'red' : 'orange'">{{ record.action === 'block' ? '拦截' : '观察' }}</a-tag>
                <a v-if="record.ip" class="mini-act" @click="askBlock(record.ip)">封禁</a>
              </template>
            </template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="test" tab="规则测试">
        <a-card size="small" title="构造一条请求，看会命中哪些规则（不会封禁）" :body-style="{ paddingTop: '12px' }">
          <a-form layout="vertical">
            <a-row :gutter="12">
              <a-col :xs="24" :md="3">
                <a-form-item label="方法">
                  <a-input v-model:value="test.method" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="12">
                <a-form-item label="路径 + 查询串（保持 URL 编码，与 access log 一致）">
                  <a-input v-model:value="test.path" placeholder="/search?q=1%27%20OR%201%3D1--" />
                </a-form-item>
              </a-col>
              <a-col :xs="24" :md="9">
                <a-form-item label="User-Agent">
                  <a-input v-model:value="test.ua" placeholder="sqlmap/1.7.2" />
                </a-form-item>
              </a-col>
            </a-row>
            <a-space>
              <a-button type="primary" :loading="testing" @click="runTest">检测</a-button>
              <a-button @click="fillSample('sqli')">SQLi 样例</a-button>
              <a-button @click="fillSample('xss')">XSS 样例</a-button>
              <a-button @click="fillSample('lfi')">穿越样例</a-button>
              <a-button @click="fillSample('scanner')">扫描器样例</a-button>
            </a-space>
          </a-form>
          <a-alert
            v-if="testDone"
            class="test-result"
            :type="testHits.length ? 'error' : 'success'"
            show-icon
            :message="testHits.length ? `命中 ${testHits.length} 条规则` : '未命中任何规则'"
          >
            <div v-for="h in testHits" :key="h.ruleId" class="test-hit">
              <b>{{ h.ruleName }}</b>
              <a-tag :color="h.action === 'block' ? 'red' : 'orange'">{{ h.action === 'block' ? '拦截' : '观察' }}</a-tag>
              <span class="mono">{{ h.snippet }}</span>
            </div>
          </a-alert>
        </a-card>
      </a-tab-pane>
    </a-tabs>

    <a-modal v-model:open="addOpen" title="新增自定义规则" :confirm-loading="adding" @ok="submitRule">
      <a-form layout="vertical">
        <a-form-item label="规则名">
          <a-input v-model:value="draft.name" placeholder="例如：拦截内部调试接口外网访问" :maxlength="40" />
        </a-form-item>
        <a-form-item label="正则（默认忽略大小写，作用于解码后的内容）">
          <a-input v-model:value="draft.pattern" placeholder="例如：/internal/debug\?token=" />
        </a-form-item>
        <a-form-item label="作用范围">
          <a-checkbox-group v-model:value="draft.scope" :options="scopeOptions" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="动作">
              <a-select v-model:value="draft.action" :options="[{ value: 'observe', label: '观察' }, { value: 'block', label: '拦截' }]" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="严重级别">
              <a-select v-model:value="draft.severity" :options="[{ value: 'high', label: '高' }, { value: 'medium', label: '中' }, { value: 'low', label: '低' }]" />
            </a-form-item>
          </a-col>
        </a-row>
        <div class="hint">保存前会用正则引擎编译校验，坏正则直接拒绝，不会写进配置。</div>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { del, getQuiet, post, put } from '../api'

interface Rule { id: string; name: string; category: string; severity: string; action: string; scope?: string[]; pattern?: string; enabled: boolean; builtin: boolean; desc?: string }
interface Settings { enabled: boolean; blockMode: string; autoBan: boolean; banLadder: number[]; banResetHours: number; exempt: string[] }
interface Hit { time: string; ruleId: string; ruleName: string; category: string; severity: string; action: string; site?: string; ip: string; country?: string; method?: string; path: string; snippet?: string; ua?: string; count: number }

const cfg = reactive<{ settings: Settings; rules: Rule[] }>({
  settings: { enabled: true, blockMode: 'observe', autoBan: true, banLadder: [10, 60, 1440], banResetHours: 24, exempt: [] },
  rules: [],
})
const stats = reactive<any>({ lastHour: 0, byAction: {}, topIps: [] })
const form = reactive<Settings>({ enabled: true, blockMode: 'observe', autoBan: true, banLadder: [10, 60, 1440], banResetHours: 24, exempt: [] })
const ladderText = ref('10,60,1440')
const exemptText = ref('')
const tab = ref('rules')
const saving = ref(false)
const hits = ref<Hit[]>([])
const filter = reactive({ category: undefined as string | undefined, ip: '' })
const testing = ref(false)
const testDone = ref(false)
const testHits = ref<Hit[]>([])
const test = reactive({ method: 'GET', path: '', ua: '' })
const addOpen = ref(false)
const adding = ref(false)
const draft = reactive<{ name: string; pattern: string; scope: string[]; action: string; severity: string }>({
  name: '', pattern: '', scope: ['query'], action: 'observe', severity: 'medium',
})

const catOptions = [
  { value: 'sqli', label: 'SQL 注入' },
  { value: 'xss', label: 'XSS' },
  { value: 'rce', label: '命令执行' },
  { value: 'lfi', label: '路径穿越' },
  { value: 'webshell', label: 'Webshell' },
  { value: 'scanner', label: '扫描器' },
  { value: 'sensitive', label: '敏感路径' },
  { value: 'custom', label: '自定义' },
]
const scopeOptions = [
  { value: 'url', label: '路径' },
  { value: 'query', label: '查询串' },
  { value: 'ua', label: 'UA' },
  { value: 'referer', label: 'Referer' },
  { value: 'any', label: '全部' },
]

const ruleCols = [
  { title: '规则', key: 'name', ellipsis: true },
  { title: '类别', key: 'category', width: 108 },
  { title: '级别', key: 'severity', width: 68 },
  { title: '动作', key: 'action', width: 104 },
  { title: '启用', key: 'enabled', width: 68 },
  { title: '', key: 'act', width: 68 },
]
const hitCols = [
  { title: '时间', key: 'time', width: 150 },
  { title: '命中规则', key: 'rule', width: 200 },
  { title: '来源', key: 'source', width: 150 },
  { title: '请求 / 证据', key: 'path', ellipsis: true },
  { title: '处置', key: 'action', width: 100 },
]

const enabledRules = computed(() => cfg.rules.filter(r => r.enabled).length)

const catText = (c: string) => catOptions.find(o => o.value === c)?.label || c
const catColor = (c: string) => ({ sqli: 'red', xss: 'volcano', rce: 'magenta', lfi: 'orange', webshell: 'red', scanner: 'blue', sensitive: 'gold', custom: 'purple' } as any)[c] || 'default'
const fmtNum = (n: number | undefined) => (n || 0).toLocaleString('zh-CN')
const fmtTime = (t: string) => (t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '-')

let timer: any = null

const loadAll = async () => {
  const r = await getQuiet<any>('/api/waf/view')
  if (!r) return
  Object.assign(cfg.settings, r.config.settings)
  cfg.rules = r.config.rules || []
  Object.assign(stats, r.stats || {})
  syncForm()
}

const syncForm = () => {
  Object.assign(form, cfg.settings)
  ladderText.value = (cfg.settings.banLadder || []).join(',')
  exemptText.value = (cfg.settings.exempt || []).join('\n')
}

const loadHits = async () => {
  const q: any = { limit: 200 }
  if (filter.category) q.category = filter.category
  if (filter.ip) q.ip = filter.ip.trim()
  const r = await getQuiet<any>('/api/waf/hits', q)
  hits.value = (r || []).map((h: Hit, i: number) => ({ ...h, _k: h.time + '-' + h.ruleId + '-' + i } as any))
}

const saveSettings = async () => {
  const ladder = ladderText.value.split(',').map(s => parseInt(s.trim(), 10)).filter(n => !isNaN(n))
  const payload: Settings = {
    ...form,
    banLadder: ladder.length ? ladder : [10, 60, 1440],
    exempt: exemptText.value.split(/[\n,]/).map(s => s.trim()).filter(Boolean),
  }
  saving.value = true
  try {
    await put('/api/waf/settings', payload)
    message.success('已保存并热生效')
    await loadAll()
  } catch { /* 拦截器已提示 */ } finally { saving.value = false }
}

const setMode = async (mode: string) => {
  Modal.confirm({
    title: mode === 'block' ? '切换到拦截模式？' : '切换到观察模式？',
    content: mode === 'block'
      ? '命中拦截级规则后会自动封禁来源 IP（阶梯时长），建议先确认攻击日志里没有误判。'
      : '所有规则降级为只记录，不再封禁任何 IP。',
    okText: '确认', cancelText: '取消',
    onOk: async () => {
      const payload: Settings = { ...cfg.settings, blockMode: mode }
      await put('/api/waf/settings', payload)
      message.success('已切换为' + (mode === 'block' ? '拦截' : '观察') + '模式')
      await loadAll()
    },
  })
}

const updateRule = async (r: Rule, enabled: boolean, action: string) => {
  try {
    await put('/api/waf/rule', { id: r.id, enabled, action })
    await loadAll()
  } catch { /* 已提示 */ }
}

const openAdd = () => {
  draft.name = ''
  draft.pattern = ''
  draft.scope = ['query']
  draft.action = 'observe'
  draft.severity = 'medium'
  addOpen.value = true
}

const submitRule = async () => {
  if (!draft.name.trim() || !draft.pattern.trim()) { message.warning('规则名和正则都要填'); return }
  if (!draft.scope.length) { message.warning('至少选一个作用范围'); return }
  adding.value = true
  try {
    await post('/api/waf/rule', draft)
    message.success('规则已添加并生效')
    addOpen.value = false
    await loadAll()
  } catch { /* 已提示 */ } finally { adding.value = false }
}

const removeRule = (r: Rule) => {
  Modal.confirm({
    title: `删除规则「${r.name}」？`, okType: 'danger', okText: '删除', cancelText: '取消',
    onOk: async () => {
      await del('/api/waf/rule/' + encodeURIComponent(r.id))
      message.success('已删除')
      await loadAll()
    },
  })
}

const askBlock = (ip: string) => {
  Modal.confirm({
    title: `封禁 ${ip}？`,
    content: '封禁 24 小时，可在 WAF 页「当前封禁」一键解封（安全中心统一管理）。',
    okText: '确认封禁', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      await post('/api/security/block/ip', { value: ip, minutes: 1440, reason: 'WAF 手动封禁' })
      message.success('已封禁并下发 nginx')
    },
  })
}

const fillSample = (kind: string) => {
  const samples: any = {
    sqli: { method: 'GET', path: "/search?q=1%27%20OR%201%3D1--", ua: 'Mozilla/5.0' },
    xss: { method: 'GET', path: '/search?q=%3Cscript%3Ealert(1)%3C/script%3E', ua: 'Mozilla/5.0' },
    lfi: { method: 'GET', path: '/download?file=../../../../etc/passwd', ua: 'Mozilla/5.0' },
    scanner: { method: 'GET', path: '/', ua: 'sqlmap/1.7.2#stable (https://sqlmap.org)' },
  }
  Object.assign(test, samples[kind])
}

const runTest = async () => {
  if (!test.path.trim()) { message.warning('请填写路径'); return }
  testing.value = true
  try {
    const r = await post<any>('/api/waf/test', test)
    testHits.value = r.hits || []
    testDone.value = true
  } catch { /* 已提示 */ } finally { testing.value = false }
}

onMounted(async () => {
  await loadAll().catch(() => { /* 首屏失败保持默认 */ })
  await loadHits().catch(() => { /* ignore */ })
  timer = setInterval(() => {
    loadAll().catch(() => { /* ignore */ })
    if (tab.value === 'hits') loadHits().catch(() => { /* ignore */ })
  }, 10000)
})
onUnmounted(() => { clearInterval(timer) })
</script>

<style scoped>
.waf-page { display: flex; flex-direction: column; gap: 12px; }
.banner { margin-bottom: 0; }
.stat-label { font-size: 13px; color: var(--zjp-text-2, #64748b); }
.stat-value { font-size: 24px; font-weight: 600; line-height: 1.5; }
.unit { font-size: 13px; font-weight: 400; opacity: 0.6; margin-left: 2px; }
.stat-sub { font-size: 12px; color: var(--zjp-text-2, #94a3b8); }
.rule-name { font-size: 13px; }
.rule-desc { font-size: 12px; opacity: 0.6; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 420px; }
.snippet { font-size: 12px; color: #ef4444; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 460px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dim { opacity: 0.55; font-size: 12px; }
.mini-act { margin-left: 8px; font-size: 12px; }
.hint { font-size: 12px; opacity: 0.6; }
.test-result { margin-top: 16px; }
.test-hit { margin-top: 6px; font-size: 12px; display: flex; gap: 6px; align-items: center; }
.tabs :deep(.ant-tabs-nav) { margin-bottom: 8px; }
@media (max-width: 767px) {
  /* 观察模式横幅在窄屏把操作按钮放到正文下方，避免挤压说明文字 */
  .banner { flex-wrap: wrap; }
  .banner :deep(.ant-alert-action) { margin-inline-start: 0; width: 100%; margin-top: 8px; }
  .banner :deep(.ant-alert-action .ant-btn) { width: 100%; }
  .stat-value { font-size: 20px; }
}
</style>
