<template>
  <a-card size="small">
    <a-tabs>
      <a-tab-pane key="general" tab="常规">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="面板版本">{{ version }}</a-descriptions-item>
          <a-descriptions-item label="面板端口">{{ settings.port }}</a-descriptions-item>
          <a-descriptions-item label="数据目录">{{ settings.dataDir }}</a-descriptions-item>
          <a-descriptions-item label="Nginx 配置目录">{{ settings.nginxPrefix }}</a-descriptions-item>
          <a-descriptions-item label="Nginx 日志目录">{{ settings.nginxLogsDir }}</a-descriptions-item>
          <a-descriptions-item label="Nginx HTML 目录">{{ settings.nginxHtmlDir }}</a-descriptions-item>
          <a-descriptions-item label="Compose 扫描根目录">{{ settings.composeRoot }}</a-descriptions-item>
          <a-descriptions-item label="ACME 挑战端口">{{ settings.challengePort }}</a-descriptions-item>
          <a-descriptions-item label="更新清单 URL" :span="2">{{ settings.updateUrl }}</a-descriptions-item>
        </a-descriptions>
      </a-tab-pane>

      <a-tab-pane key="update" tab="检查更新">
        <a-space direction="vertical" style="width: 100%">
          <a-space>
            <a-button type="primary" :loading="checking" @click="checkUpdate">检查更新</a-button>
            <a-button v-if="info?.updateAvailable && !isDevBuild" type="primary" danger :loading="applying" @click="applyUpdate">一键更新（下载并重启）</a-button>
          </a-space>
          <a-alert v-if="restarting" type="info" show-icon
            message="新版本已就位，面板正在重启，等待恢复后自动刷新页面 ..." />
          <a-alert v-if="info" :type="info.updateAvailable ? 'warning' : 'success'" show-icon
            :message="info.updateAvailable ? `发现新版本 ${info.latest}（当前 ${info.current}）` : `已是最新版本 ${info.current}`" />
          <a-alert v-if="isDevBuild" type="warning" show-icon
            message="当前为开发构建（本地直接运行）：一键更新不可用，本地升级请重新构建二进制；服务器部署可在面板一键更新或宿主机执行 ./update.sh。" />
          <p v-if="info?.notes" class="dim">{{ info.notes }}</p>
          <a-alert type="info" show-icon
            message="一键更新会下载新面板二进制并原地替换重启（前端随二进制一起更新），站点与 nginx 不中断；容器镜像版本不变，需同步镜像时在宿主机执行 ./update.sh。" />
          <a-alert v-if="settings.updateUrl?.includes('github.com')" type="warning" show-icon
            message="服务器访问 GitHub 缓慢时，可在部署环境变量中设置 ZJPANEL_GH_MIRROR（如 https://ghfast.top）加速下载。" />
        </a-space>
      </a-tab-pane>

      <a-tab-pane key="maintenance" tab="全局维护页模板">
        <div style="height: 360px; border: 1px solid #eee">
          <AceEditor v-model="maintTemplate" mode="html" />
        </div>
        <a-button type="primary" style="margin-top: 10px" @click="saveMaint">保存模板</a-button>
        <span class="dim" style="margin-left: 10px">{{ varHint }}；站点级可在网站编辑中覆盖</span>
      </a-tab-pane>

      <a-tab-pane key="password" tab="账号安全">
        <a-form layout="vertical" style="max-width: 380px">
          <a-form-item label="当前用户名"><a-input :value="authName" disabled /></a-form-item>
          <a-form-item label="新用户名（2-64位）"><a-input v-model:value="newU" /></a-form-item>
          <a-form-item label="当前密码"><a-input-password v-model:value="unPw" /></a-form-item>
          <a-button type="primary" @click="changeUn">修改用户名</a-button>
        </a-form>
        <a-divider style="max-width: 380px" />
        <a-form layout="vertical" style="max-width: 380px">
          <a-form-item label="原密码"><a-input-password v-model:value="oldP" /></a-form-item>
          <a-form-item label="新密码（≥8位）"><a-input-password v-model:value="newP" /></a-form-item>
          <a-form-item label="确认新密码"><a-input-password v-model:value="newP2" @pressEnter="changePw" /></a-form-item>
          <a-button type="primary" @click="changePw">修改</a-button>
        </a-form>
      </a-tab-pane>

      <a-tab-pane key="audit" tab="审计日志">
        <a-table :data-source="audit" :columns="auditCols" size="small" row-key="id" :pagination="{ pageSize: 15 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'time'">{{ fmtTime(record.time) }}</template>
          </template>
        </a-table>
      </a-tab-pane>
    </a-tabs>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, put, post, fmtTime } from '../api'
import { useAuth } from '../store/auth'
import AceEditor from '../components/AceEditor.vue'

const auth = useAuth()
const authName = computed(() => auth.username)
const settings = ref<any>({})
const version = ref('')
const info = ref<any>(null)
const checking = ref(false)
const applying = ref(false)
const restarting = ref(false)
// dev 构建（本地 exe 直接运行）不支持容器式一键更新
const isDevBuild = computed(() => /dev|^0\.0\.0/.test(version.value || '') || /dev|^0\.0\.0/.test(info.value?.current || ''))
const maintTemplate = ref('')
const varHint = '支持变量 {domain} {time} {message}（渲染为域名/时间/提示语）'
const oldP = ref('')
const newP = ref('')
const newP2 = ref('')
const newU = ref('')
const unPw = ref('')
const audit = ref<any[]>([])

const auditCols: any[] = [
  { title: "时间", dataIndex: "time", key: "time", width: 170 },
  { title: '操作', dataIndex: 'action', key: 'action', width: 200 },
  { title: '详情', dataIndex: 'detail', key: 'detail' },
  { title: 'IP', dataIndex: 'ip', key: 'ip', width: 130 },
]

const load = async () => {
  settings.value = await get('/api/settings')
  version.value = (await get<any>('/api/update/current')).version
  audit.value = await get('/api/settings/audit')
  const d = await get('/api/sites/maintenance-template').catch(() => null)
  if (d) maintTemplate.value = d.content
}
const checkUpdate = async () => {
  checking.value = true
  try { info.value = await post('/api/update/check') } catch { /* ignore */ }
  checking.value = false
}
const applyUpdate = async () => {
  applying.value = true
  try {
    const r = await post('/api/update/apply', {})
    if (!r.ok) { message.error(r.error); return }
    message.success(r.message || '新版本已就位，面板重启中')
    await waitRestart()
  } finally { applying.value = false }
}
// 等待面板重启完成：进程替换期间请求会失败（属预期），版本号变化即成功。
// 用原生 fetch 轮询，避免 axios 拦截器在重启期间弹全局错误提示。
const waitRestart = async () => {
  const oldVersion = version.value || info.value?.current || ''
  restarting.value = true
  for (let i = 0; i < 90; i++) {
    await new Promise(r => setTimeout(r, 2000))
    try {
      const resp = await fetch('/api/update/current', { credentials: 'same-origin' })
      if (!resp.ok) continue
      const v = (await resp.json())?.version
      if (v && v !== oldVersion) {
        message.success(`面板已更新到 ${v}，即将刷新页面`)
        setTimeout(() => location.reload(), 1200)
        return
      }
    } catch { /* 重启中，稍后重试 */ }
  }
  message.warning('未在预期时间内检测到版本变化，请手动刷新页面确认')
  restarting.value = false
}
const saveMaint = async () => {
  await put('/api/sites/maintenance-template', { content: maintTemplate.value })
  message.success('已保存')
}
const changePw = async () => {
  if (newP.value.length < 8) { message.warning('新密码至少 8 位'); return }
  if (newP.value !== newP2.value) { message.warning('两次输入的新密码不一致'); return }
  await post('/api/auth/change-password', { old: oldP.value, new: newP.value })
  message.success('密码已修改')
  oldP.value = newP.value = newP2.value = ''
}
const changeUn = async () => {
  await post('/api/auth/change-username', { username: newU.value, password: unPw.value })
  message.success('用户名已修改')
  newU.value = unPw.value = ''
  await auth.refresh()
}

onMounted(load)
onActivated(load)
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
</style>
