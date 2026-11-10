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
            <a-button v-if="info?.updateAvailable" type="primary" danger :loading="applying" @click="applyUpdate">一键更新（重建容器）</a-button>
          </a-space>
          <a-alert v-if="info" :type="info.updateAvailable ? 'warning' : 'success'" show-icon
            :message="info.updateAvailable ? `发现新版本 ${info.latest}（当前 ${info.current}）` : `已是最新版本 ${info.current}`" />
          <p v-if="info?.notes" class="dim">{{ info.notes }}</p>
          <a-alert type="info" show-icon message="也可在宿主机执行 ./update.sh 升级（同效）" />
        </a-space>
      </a-tab-pane>

      <a-tab-pane key="maintenance" tab="全局维护页模板">
        <div style="height: 360px; border: 1px solid #eee">
          <AceEditor v-model="maintTemplate" mode="html" />
        </div>
        <a-button type="primary" style="margin-top: 10px" @click="saveMaint">保存模板</a-button>
        <span class="dim" style="margin-left: 10px">{{ varHint }}；站点级可在网站编辑中覆盖</span>
      </a-tab-pane>

      <a-tab-pane key="password" tab="修改密码">
        <a-form layout="vertical" style="max-width: 380px">
          <a-form-item label="原密码"><a-input-password v-model:value="oldP" /></a-form-item>
          <a-form-item label="新密码（≥8位）"><a-input-password v-model:value="newP" /></a-form-item>
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
import { onActivated, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, put, post, fmtTime } from '../api'
import AceEditor from '../components/AceEditor.vue'

const settings = ref<any>({})
const version = ref('')
const info = ref<any>(null)
const checking = ref(false)
const applying = ref(false)
const maintTemplate = ref('')
const varHint = '支持变量 {domain} {time} {message}（渲染为域名/时间/提示语）'
const oldP = ref('')
const newP = ref('')
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
    if (r.ok) message.success(r.message || '更新流程已启动')
    else message.error(r.error)
  } finally { applying.value = false }
}
const saveMaint = async () => {
  await put('/api/sites/maintenance-template', { content: maintTemplate.value })
  message.success('已保存')
}
const changePw = async () => {
  await post('/api/auth/change-password', { old: oldP.value, new: newP.value })
  message.success('密码已修改')
  oldP.value = newP.value = ''
}

onMounted(load)
onActivated(load)
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
</style>
