<template>
  <a-spin :spinning="loading">
  <a-card size="small">
    <a-tabs>
      <a-tab-pane key="general" tab="常规">
        <a-card size="small" title="访问安全入口" style="margin-bottom: 16px">
          <a-alert type="warning" show-icon style="margin-bottom: 12px"
            message="启用后，访问域名根 / 或其他未知路径将返回 404，只有输入下方秘密入口路径才能打开登录页。请牢记入口地址；入口仅隐藏页面，接口仍需登录鉴权。若忘记入口，可在部署环境设置环境变量 ZJPANEL_ENTRY_OFF=1 后重启面板临时关闭。" />
          <a-form layout="inline">
            <a-form-item label="入口路径">
              <a-input v-model:value="entryInput" style="width: 260px" placeholder="如 /admin-x7k2（留空=关闭）" />
            </a-form-item>
            <a-form-item>
              <a-button type="primary" :loading="entrySaving" @click="saveEntry">保存入口</a-button>
            </a-form-item>
          </a-form>
          <div class="dim" style="margin-top: 8px">
            当前状态：{{ entryEnabled ? `已启用，访问地址 ${origin}${entryPath}` : '未启用（根路径可直接打开登录页）' }}
          </div>
        </a-card>
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
            <a-button v-if="info?.updateAvailable && !isDevBuild" type="primary" danger :loading="applying" @click="applyUpdate">
              {{ info?.mode === 'container' ? '一键更新（重建容器）' : '一键更新（下载并重启）' }}
            </a-button>
          </a-space>
          <a-alert v-if="restarting" type="info" show-icon
            :message="info?.mode === 'container'
              ? '已固定 compose 镜像版本并清理本地旧镜像，正在重建容器并拉取新镜像（站点会短暂中断），等待恢复后自动刷新页面 ...'
              : '新版本已就位，面板正在重启，等待恢复后自动刷新页面 ...'" />
          <a-alert v-if="info" :type="info.updateAvailable ? 'warning' : 'success'" show-icon
            :message="info.updateAvailable ? `发现新版本 ${info.latest}（当前 ${info.current}）` : `已是最新版本 ${info.current}`" />
          <a-alert v-if="isDevBuild" type="warning" show-icon
            message="当前为开发构建（本地直接运行）：一键更新不可用，本地升级请重新构建二进制；服务器部署可在面板一键更新或宿主机执行 ./update.sh。" />
          <p v-if="info?.notes" class="dim">{{ info.notes }}</p>
          <a-alert type="info" show-icon
            :message="info?.mode === 'container'
              ? '容器部署：一键更新会更新 compose 镜像标签并重建容器，重建时自动拉取新镜像，更新后 down/up 不再回退；重建期间站点与面板会短暂中断（约数秒）。'
              : '一键更新会下载新面板二进制并原地替换重启（前端随二进制一起更新），站点与 nginx 不中断；如需同步容器镜像，请在宿主机执行 ./update.sh。'" />
          <a-alert v-if="settings.updateUrl?.includes('github.com')" type="warning" show-icon
            message="服务器访问 GitHub 缓慢时，可在部署环境变量中设置 ZJPANEL_GH_MIRROR（如 https://ghfast.top）加速下载。" />
        </a-space>
      </a-tab-pane>

      <a-tab-pane key="maintenance" tab="全局维护页模板">
        <div style="height: 360px; border: 1px solid var(--border-card)">
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

      <a-tab-pane key="backup" tab="备份与恢复">
        <a-spin :spinning="backupBusy">
          <a-row :gutter="16">
            <!-- 左：备份范围 + 操作 -->
            <a-col :xs="24" :md="10">
              <a-card size="small" title="备份范围" class="backup-card">
                <a-checkbox :checked="allItemsChecked" @change="toggleAllItems">全选（完整配置迁移）</a-checkbox>
                <a-checkbox-group v-model:value="backupItems" class="item-group">
                  <div v-for="it in backupItemMetas" :key="it.key" class="item-row">
                    <a-checkbox :value="it.key">
                      <div class="item-label">{{ it.label }}</div>
                      <div class="item-desc">{{ it.desc }}</div>
                    </a-checkbox>
                  </div>
                </a-checkbox-group>
                <a-divider style="margin: 12px 0" />
                <a-space>
                  <a-button type="primary" :loading="creating" @click="createBackup">立即备份</a-button>
                  <a-upload :show-upload-list="false" :before-upload="restoreBefore">
                    <a-button :loading="restoring">从备份包恢复</a-button>
                  </a-upload>
                </a-space>
                <div class="dim" style="margin-top: 8px">
                  配置级迁移：打包面板数据库、证书、安全/WAF 策略、插件数据、Nginx 站点配置与 Compose 项目声明；不含站点上传文件、Docker 数据卷与业务数据库。
                </div>
              </a-card>

              <a-card size="small" title="远程存储" class="backup-card">
                <a-form layout="vertical">
                  <a-form-item label="存储类型">
                    <a-select v-model:value="remote.kind" :options="remoteKindOptions" />
                  </a-form-item>
                  <template v-if="remote.kind === 'webdav'">
                    <a-form-item label="WebDAV 地址">
                      <a-input v-model:value="remote.url" placeholder="https://dav.example.com/remote.php/dav/files/user" />
                    </a-form-item>
                    <a-row :gutter="12">
                      <a-col :span="12">
                        <a-form-item label="用户名">
                          <a-input v-model:value="remote.username" />
                        </a-form-item>
                      </a-col>
                      <a-col :span="12">
                        <a-form-item label="密码">
                          <a-input-password v-model:value="remote.password" :placeholder="remote.passwordSet ? '已设置，留空沿用' : ''" />
                        </a-form-item>
                      </a-col>
                    </a-row>
                    <a-form-item label="远程目录">
                      <a-input v-model:value="remote.remoteDir" placeholder="留空存到根目录" />
                    </a-form-item>
                  </template>
                  <a-alert v-if="remote.kind === 'googledrive' || remote.kind === 'onedrive'" type="info" show-icon
                    message="该云盘通道（OAuth 授权）尚未接入，当前仅支持 WebDAV。" />
                  <a-space>
                    <a-button @click="testRemote">测试连接</a-button>
                    <a-button type="primary" @click="saveRemote">保存远程配置</a-button>
                  </a-space>
                </a-form>
              </a-card>
            </a-col>

            <!-- 右：备份记录 -->
            <a-col :xs="24" :md="14">
              <a-card size="small" title="备份记录">
                <a-table :data-source="backupList" :columns="backupCols" size="small" row-key="name" :pagination="{ pageSize: 10 }">
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'createdAt'">{{ fmtTime(record.createdAt) }}</template>
                    <template v-if="column.key === 'size'">{{ fmtBytes(record.size) }}</template>
                    <template v-if="column.key === 'items'">{{ (record.items || []).length }} 项</template>
                    <template v-if="column.key === 'action'">
                      <a-space :size="4">
                        <a-button size="small" @click="downloadBackup(record)">下载</a-button>
                        <a-button size="small" @click="uploadRemote(record)">上传远程</a-button>
                        <a-button size="small" danger @click="removeBackup(record)">删除</a-button>
                      </a-space>
                    </template>
                  </template>
                </a-table>
              </a-card>
            </a-col>
          </a-row>
        </a-spin>
      </a-tab-pane>
    </a-tabs>
  </a-card>
  </a-spin>
</template>

<script setup lang="ts">
import { computed, onActivated, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getQuiet, put, post, del, fmtTime, fmtBytes, http } from '../api'
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

// ---- 安全入口 ----
const entryInput = ref('')
const entryPath = ref('')
const entryEnabled = ref(false)
const entrySaving = ref(false)
const origin = window.location.origin

const loadEntry = async () => {
  try {
    const r = await getQuiet<any>('/api/settings/entry')
    entryPath.value = r?.entryPath || ''
    entryEnabled.value = !!r?.enabled
    entryInput.value = entryPath.value
  } catch { /* 忽略：未配置时保持关闭态 */ }
}

const saveEntry = async () => {
  entrySaving.value = true
  try {
    const r = await put('/api/settings/entry', { entryPath: entryInput.value.trim() })
    if (!r.ok) return
    entryPath.value = r.entryPath || ''
    entryEnabled.value = !!r.enabled
    entryInput.value = entryPath.value
    if (r.enabled) {
      message.success(`入口已启用，即将跳转到 ${r.entryPath}`)
      // 启用后旧路径会 404，跳到新入口下的设置页（后端注入 base，前端路由正常）
      setTimeout(() => { window.location.href = r.entryPath + '/settings' }, 800)
    } else {
      message.success('安全入口已关闭，根路径可直接访问')
      setTimeout(() => { window.location.href = '/settings' }, 800)
    }
  } finally { entrySaving.value = false }
}

// ---- 备份与恢复 ----
const backupBusy = ref(false)
const creating = ref(false)
const restoring = ref(false)
const backupItems = ref<string[]>([])
const backupItemMetas = ref<any[]>([])
const backupList = ref<any[]>([])
const remote = ref<any>({ enabled: false, kind: 'local', url: '', username: '', password: '', passwordSet: false, remoteDir: '' })
const remoteKindOptions = [
  { value: 'local', label: '仅本地' },
  { value: 'webdav', label: 'WebDAV' },
  { value: 'googledrive', label: 'Google Drive（待接入）' },
  { value: 'onedrive', label: 'OneDrive（待接入）' },
]
const backupCols = [
  { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 160 },
  { title: '大小', dataIndex: 'size', key: 'size', width: 90 },
  { title: '范围', dataIndex: 'items', key: 'items', width: 80 },
  { title: '操作', key: 'action' },
]
const allItemsChecked = computed(() => backupItemMetas.value.length > 0 && backupItems.value.length === backupItemMetas.value.length)

const loadBackup = async () => {
  const [itemsR, listR, remoteR] = await Promise.allSettled([
    getQuiet('/api/backup/items'),
    getQuiet('/api/backup/list'),
    getQuiet('/api/backup/remote'),
  ])
  if (itemsR.status === 'fulfilled') backupItemMetas.value = itemsR.value?.items || []
  if (listR.status === 'fulfilled') backupList.value = listR.value?.list || []
  if (remoteR.status === 'fulfilled' && remoteR.value) {
    remote.value = { ...remote.value, ...remoteR.value, password: '' }
  }
  if (backupItems.value.length === 0 && backupItemMetas.value.length > 0) {
    backupItems.value = backupItemMetas.value.map((i: any) => i.key)
  }
}

const toggleAllItems = (e: any) => {
  backupItems.value = e.target.checked ? backupItemMetas.value.map((i: any) => i.key) : []
}

const createBackup = async () => {
  creating.value = true
  try {
    const r = await post('/api/backup/create', { items: backupItems.value })
    if (r.ok) {
      message.success('备份已生成')
      await loadBackup()
    }
  } finally { creating.value = false }
}

const downloadBackup = (record: any) => {
  window.open(`/api/backup/download?name=${encodeURIComponent(record.name)}`, '_blank')
}

const uploadRemote = async (record: any) => {
  backupBusy.value = true
  try {
    const r = await post(`/api/backup/${encodeURIComponent(record.name)}/upload-remote`)
    if (r.ok) message.success('已上传到远程存储')
  } finally { backupBusy.value = false }
}

const removeBackup = async (record: any) => {
  await del(`/api/backup/${encodeURIComponent(record.name)}`)
  message.success('已删除')
  await loadBackup()
}

const restoreBefore = (file: File) => {
  restoring.value = true
  const fd = new FormData()
  fd.append('file', file)
  fd.append('items', backupItems.value.join(','))
  http.post('/api/backup/restore', fd, { headers: { 'Content-Type': 'multipart/form-data' } })
    .then((resp: any) => {
      const r = resp.data
      if (r.ok) message.success(`恢复完成：还原 ${r.result?.restored?.length || 0} 项`)
      else message.error(r.error || '恢复失败')
    })
    .catch(() => message.error('恢复失败'))
    .finally(() => { restoring.value = false })
  return false
}

const testRemote = async () => {
  const r = await post('/api/backup/remote/test', remote.value)
  if (r.ok) message.success('连接成功')
  else message.error(r.error || '连接失败')
}

const saveRemote = async () => {
  const r = await put('/api/backup/remote', remote.value)
  if (r.ok) { message.success('远程配置已保存'); await loadBackup() }
}

const auditCols: any[] = [
  { title: "时间", dataIndex: "time", key: "time", width: 170 },
  { title: '操作', dataIndex: 'action', key: 'action', width: 200 },
  { title: '详情', dataIndex: 'detail', key: 'detail' },
  { title: 'IP', dataIndex: 'ip', key: 'ip', width: 130 },
]

const loading = ref(false)

const load = async () => {
  loading.value = true
  try {
    // 四个接口互不依赖：并发拉取，避免版本/审计/维护模板逐个等前面返回。
    // quiet 拉取：单项失败只在设置主体失败时提示，避免一次刷多条 toast。
    const [setR, verR, auditR, tplR] = await Promise.allSettled([
      getQuiet<any>('/api/settings'),
      getQuiet<any>('/api/update/current'),
      getQuiet<any[]>('/api/settings/audit'),
      getQuiet<any>('/api/sites/maintenance-template'),
    ])
    if (setR.status === 'fulfilled') settings.value = setR.value || {}
    if (verR.status === 'fulfilled') version.value = verR.value?.version || ''
    if (auditR.status === 'fulfilled') audit.value = auditR.value || []
    if (tplR.status === 'fulfilled' && tplR.value) maintTemplate.value = tplR.value.content
    if (setR.status === 'rejected') message.error('设置加载失败，请稍后重试')
    loadBackup()
    loadEntry()
  } finally { loading.value = false }
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

// 首次加载交给 onActivated（设置页在 keep-alive 白名单内），避免 mounted+activated 双请求
onActivated(load)
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
.backup-card { margin-bottom: 16px; }
.item-group { margin-top: 12px; }
.item-row { padding: 6px 0; }
.item-label { font-size: 13px; }
.item-desc { font-size: 12px; color: var(--color-slate); }
</style>
