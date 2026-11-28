<template>
  <a-spin :spinning="loading">
  <a-card size="small">
    <a-tabs v-model:value="activeTab">
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

      <a-tab-pane key="ai" tab="运维Agent">
        <SettingsAiTab />
      </a-tab-pane>

      <a-tab-pane key="backup" tab="备份与恢复">
        <a-spin :spinning="backupBusy">
          <a-row :gutter="16">
            <!-- 左：定时备份 + 备份范围 + 操作 -->
            <a-col :xs="24" :md="10">
              <a-card size="small" title="定时备份" class="backup-card">
                <a-form layout="vertical">
                  <a-form-item>
                    <a-space>
                      <a-switch v-model:checked="schedule.enabled" />
                      <span>按计划自动备份</span>
                    </a-space>
                  </a-form-item>
                  <template v-if="schedule.enabled">
                    <a-form-item label="执行频率">
                      <a-select v-model:value="cronPreset" :options="cronPresets" style="width: 180px" @change="onCronPresetChange" />
                      <a-input v-if="cronPreset === 'custom'" v-model:value="schedule.cron"
                        placeholder="cron 表达式（分 时 日 月 周），如 30 2 * * 1-5" style="margin-top: 8px" />
                      <div class="dim" style="margin-top: 6px">下次执行：{{ schedule.nextRun ? fmtTime(schedule.nextRun) : '-' }}</div>
                    </a-form-item>
                    <a-form-item label="备份范围">
                      <a-select v-model:value="schedule.items" mode="multiple" :options="scheduleItemOptions"
                        placeholder="留空 = 全部项目" allow-clear />
                    </a-form-item>
                    <a-form-item label="打包完成后">
                      <a-space>
                        <a-checkbox v-model:checked="schedule.uploadRemote" :disabled="!remoteReady">自动上传到远程存储</a-checkbox>
                        <span v-if="!remoteReady" class="dim">需先在下方配置并连接远程存储</span>
                      </a-space>
                    </a-form-item>
                    <a-form-item label="本地保留份数">
                      <a-space>
                        <a-input-number v-model:value="schedule.keep" :min="0" :max="999" style="width: 100px" />
                        <span class="dim">0 = 上传成功后立即删除本地（需开启自动上传）；超出自动删最旧</span>
                      </a-space>
                    </a-form-item>
                    <a-form-item label="云端保留份数">
                      <a-space>
                        <a-input-number v-model:value="schedule.remoteKeep" :min="0" :max="999" style="width: 100px" />
                        <span class="dim">0 = 不清理云端；超出自动删除云端最旧备份</span>
                      </a-space>
                    </a-form-item>
                  </template>
                  <div v-if="schedule.lastRun" class="dim" style="margin-bottom: 8px">
                    上次定时备份：{{ fmtTime(schedule.lastRun.time) }} ·
                    {{ schedule.lastRun.ok ? '成功' : '失败' + (schedule.lastRun.error ? '：' + schedule.lastRun.error : '') }}
                  </div>
                  <a-button type="primary" :loading="schedSaving" @click="saveSchedule">保存定时备份</a-button>
                </a-form>
              </a-card>

              <a-card size="small" title="备份包加密" class="backup-card">
                <a-form layout="vertical">
                  <a-form-item>
                    <a-space>
                      <a-switch v-model:checked="crypt.enabled" />
                      <span>上传云端前加密备份包（age 格式）</span>
                    </a-space>
                    <div class="dim" style="margin-top: 6px">
                      避免云盘侧泄露明文数据；恢复时面板自动解密。灾难恢复可在无面板环境用 age 命令行 + 该密码离线解包：
                      <code>age -d -o 备份.tar.gz 备份.tar.gz.enc</code>
                    </div>
                  </a-form-item>
                  <a-form-item v-if="crypt.enabled" label="加密密码">
                    <a-input-password v-model:value="crypt.password" :placeholder="crypt.passwordSet ? '已设置，留空沿用' : ''" />
                  </a-form-item>
                  <a-button :loading="cryptSaving" @click="saveCrypt">保存加密设置</a-button>
                </a-form>
              </a-card>

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
                  配置级 + 应用目录迁移：打包面板数据库、证书、安全/WAF 策略、插件数据、Nginx 站点配置，以及 docker-compose.yml 所在的应用目录整体（编排文件、配置与挂载的本地数据）；不含 Docker 命名卷与站点上传文件。
                </div>
              </a-card>

              <a-card size="small" title="远程存储" class="backup-card">
                <a-form layout="vertical">
                  <a-form-item label="存储类型">
                    <a-select v-model:value="remote.kind" :options="remoteKindOptions" @change="onKindChange" />
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
                  <template v-if="remote.kind === 'googledrive' || remote.kind === 'onedrive'">
                    <a-alert type="info" show-icon style="margin-bottom: 12px"
                      :message="remote.kind === 'googledrive' ? '需自建 Google OAuth 应用' : '需自建 Azure 应用注册'"
                      :description="remote.kind === 'googledrive'
                        ? '在 Google Cloud Console 创建「Web 应用」类型的 OAuth 客户端并启用 Google Drive API，把下方回调地址加入「已获授权的重定向 URI」。面板仅申请 drive.file 权限，只能读写由面板创建的文件。'
                        : '在 Azure 门户注册应用，添加「Web」平台并填入下方回调地址，API 权限勾选 Microsoft Graph 的 Files.ReadWrite 与 offline_access。备份会写入 OneDrive 的 ZJPanel 目录。'" />
                    <a-form-item label="Client ID">
                      <a-input v-model:value="remote.clientId" placeholder="OAuth 应用的客户端 ID" />
                    </a-form-item>
                    <a-form-item label="Client Secret">
                      <a-input-password v-model:value="remote.clientSecret"
                        :placeholder="remote.clientSecretSet ? '已设置，留空沿用' : 'OAuth 应用的客户端密钥'" />
                    </a-form-item>
                    <a-form-item label="回调地址（需加入厂商白名单）">
                      <a-input-group compact>
                        <a-input :value="oauthRedirect" readonly style="width: calc(100% - 68px)" />
                        <a-button style="width: 68px" @click="copyRedirect">复制</a-button>
                      </a-input-group>
                    </a-form-item>
                    <a-form-item label="远程目录">
                      <a-input v-model:value="remote.remoteDir" placeholder="留空存到 ZJPanel 目录" />
                    </a-form-item>
                    <a-form-item label="授权状态">
                      <a-space wrap>
                        <a-tag :color="remote.authorized ? 'green' : 'default'">
                          {{ remote.authorized ? `已连接${remote.account ? ' · ' + remote.account : ''}` : '未授权' }}
                        </a-tag>
                        <a-button size="small" type="primary" ghost :loading="authorizing" @click="startOAuth">
                          {{ remote.authorized ? '重新授权' : '授权并连接' }}
                        </a-button>
                        <a-popconfirm v-if="remote.authorized" title="断开后需重新授权才能上传到该云盘，确定断开？"
                          ok-text="断开" cancel-text="取消" @confirm="clearOAuth">
                          <a-button size="small" danger>断开</a-button>
                        </a-popconfirm>
                      </a-space>
                      <div v-if="authorizing" class="dim" style="margin-top: 6px">已打开授权窗口，完成授权后会自动回填状态…</div>
                    </a-form-item>
                  </template>
                  <a-space>
                    <a-button @click="testRemote">测试连接</a-button>
                    <a-button type="primary" @click="saveRemote">保存远程配置</a-button>
                  </a-space>
                </a-form>
              </a-card>
            </a-col>

            <!-- 右：任务进度 + 备份记录 -->
            <a-col :xs="24" :md="14">
              <a-card size="small" title="备份任务" class="backup-card">
                <div v-if="!job" class="dim">暂无任务。创建备份、上传远程或恢复都会在这里实时显示进度。</div>
                <template v-else>
                  <div style="margin-bottom: 8px">
                    <a-space wrap :size="8">
                      <a-tag :color="jobStatus === 'success' ? 'green' : jobStatus === 'error' ? 'red' : 'blue'">
                        {{ jobStatusText }}
                      </a-tag>
                      <span>{{ jobKindText(job.kind) }}</span>
                      <span class="dim">{{ job.target }}</span>
                      <span v-if="job.trigger === 'schedule'" class="dim">定时触发</span>
                      <span v-if="jobStatus === 'running'" class="dim">已运行 {{ jobElapsed }}</span>
                      <span v-if="jobStatus === 'error'" style="color: #ef4444">{{ job.error }}</span>
                    </a-space>
                  </div>
                  <pre ref="jobLogEl" class="runlog">{{ jobLines.join('\n') || '等待输出…' }}</pre>
                </template>
                <template v-if="jobHistory.length">
                  <a-divider style="margin: 10px 0" />
                  <a-table :data-source="jobHistory" :columns="jobHistCols" size="small" row-key="id" :pagination="false">
                    <template #bodyCell="{ column, record }">
                      <template v-if="column.key === 'startedAt'">{{ fmtTime(record.startedAt) }}</template>
                      <template v-if="column.key === 'status'">
                        <a-tag :color="record.status === 'success' ? 'green' : record.status === 'error' ? 'red' : 'blue'">
                          {{ record.status === 'success' ? '成功' : record.status === 'error' ? '失败' : '进行中' }}
                        </a-tag>
                      </template>
                      <template v-if="column.key === 'duration'">{{ jobDuration(record) }}</template>
                    </template>
                  </a-table>
                </template>
              </a-card>

              <a-card size="small" title="备份记录">
                <a-table :data-source="backupList" :columns="backupCols" size="small" row-key="name" :pagination="{ pageSize: 10 }">
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'createdAt'">{{ fmtTime(record.createdAt) }}</template>
                    <template v-if="column.key === 'size'">{{ fmtBytes(record.size) }}</template>
                    <template v-if="column.key === 'items'">{{ (record.items || []).length }} 项{{ record.encrypted ? ' 🔒' : '' }}</template>
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
import { computed, nextTick, onActivated, onDeactivated, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { getQuiet, put, post, del, fmtTime, fmtBytes, http, wsTopic } from '../api'
import { useAuth } from '../store/auth'
import AceEditor from '../components/AceEditor.vue'
import SettingsAiTab from '../components/SettingsAiTab.vue'


const auth = useAuth()
const authName = computed(() => auth.username)
// 设置页 Tab：从授权回调返回时带 ?tab=backup，直接落到备份页
const activeTab = ref('general')
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
const remote = ref<any>({
  enabled: false, kind: 'local', url: '', username: '', password: '', passwordSet: false, remoteDir: '',
  clientId: '', clientSecret: '', clientSecretSet: false, refreshToken: '', account: '', authorized: false,
})
const remoteKindOptions = [
  { value: 'local', label: '仅本地' },
  { value: 'webdav', label: 'WebDAV' },
  { value: 'googledrive', label: 'Google Drive' },
  { value: 'onedrive', label: 'OneDrive' },
]
const oauthRedirect = ref('')
const authorizing = ref(false)
let oauthTimer: number | undefined

// 回调地址按浏览器实际访问地址生成（反代/自定义域名下最可靠），并原样展示给用户去厂商后台加白名单。
const computeRedirect = () => {
  oauthRedirect.value = `${location.origin}/api/backup/oauth/callback`
}
const backupCols = [
  { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 160 },
  { title: '大小', dataIndex: 'size', key: 'size', width: 90 },
  { title: '范围', dataIndex: 'items', key: 'items', width: 80 },
  { title: '操作', key: 'action' },
]
const allItemsChecked = computed(() => backupItemMetas.value.length > 0 && backupItems.value.length === backupItemMetas.value.length)

const loadBackup = async () => {
  computeRedirect()
  const [itemsR, listR, remoteR, schedR, cryptR] = await Promise.allSettled([
    getQuiet('/api/backup/items'),
    getQuiet('/api/backup/list'),
    getQuiet('/api/backup/remote'),
    getQuiet('/api/backup/schedule'),
    getQuiet('/api/backup/crypt'),
  ])
  if (itemsR.status === 'fulfilled') backupItemMetas.value = itemsR.value?.items || []
  if (listR.status === 'fulfilled') backupList.value = listR.value?.list || []
  if (remoteR.status === 'fulfilled' && remoteR.value) {
    remote.value = { ...remote.value, ...remoteR.value, password: '', clientSecret: '' }
  }
  if (schedR.status === 'fulfilled' && schedR.value) {
    schedule.value = { ...schedule.value, ...schedR.value }
    syncCronPreset()
  }
  if (cryptR.status === 'fulfilled' && cryptR.value) {
    crypt.value = { ...crypt.value, ...cryptR.value, password: '' }
  }
  if (backupItems.value.length === 0 && backupItemMetas.value.length > 0) {
    backupItems.value = backupItemMetas.value.map((i: any) => i.key)
  }
  await fetchJob()
  await loadJobHistory()
  if (job.value?.status === 'running') startJobWatch()
}

const toggleAllItems = (e: any) => {
  backupItems.value = e.target.checked ? backupItemMetas.value.map((i: any) => i.key) : []
}

// ---- 备份任务进度（WS 实时 + 2s 快照轮询兜底，模式同 AppsPage 的 compose 任务）----
const job = ref<any>(null)
const jobLines = ref<string[]>([])
const jobTotal = ref(0)
const jobHistory = ref<any[]>([])
const jobLogEl = ref<HTMLElement | null>(null)
const nowTick = ref(Date.now())
let jobWsOff: (() => void) | null = null
let jobTimer: number | undefined
let tickTimer: number | undefined
const jobStatus = computed(() => job.value?.status || '')
const jobStatusText = computed(() => jobStatus.value === 'success' ? '完成' : jobStatus.value === 'error' ? '失败' : '进行中')
const jobElapsed = computed(() => {
  const st = job.value?.startedAt ? new Date(job.value.startedAt).getTime() : 0
  if (!st) return '-'
  const sec = Math.max(0, Math.floor((nowTick.value - st) / 1000))
  const m = Math.floor(sec / 60), s = sec % 60
  return m > 0 ? `${m} 分 ${s} 秒` : `${s} 秒`
})
const jobHistCols = [
  { title: '时间', dataIndex: 'startedAt', key: 'startedAt', width: 160 },
  { title: '类型', key: 'kind', width: 90 },
  { title: '对象', dataIndex: 'target', ellipsis: true },
  { title: '结果', key: 'status', width: 70 },
  { title: '耗时', key: 'duration', width: 90 },
]
const jobKindText = (k: string) => ({
  create: '创建备份', upload: '上传远程', download: '远程下载', restore: '恢复',
} as any)[k] || k
const jobDuration = (r: any) => {
  if (!r.startedAt || !r.endedAt) return '-'
  const sec = Math.max(1, Math.round((new Date(r.endedAt).getTime() - new Date(r.startedAt).getTime()) / 1000))
  return sec >= 60 ? `${Math.floor(sec / 60)} 分 ${sec % 60} 秒` : `${sec} 秒`
}

const mergeJob = (v: any) => {
  if (!v) return
  const wasRunning = job.value?.status === 'running' && job.value?.id === v.id
  if (!job.value || v.id !== job.value.id) jobLines.value = []
  job.value = v
  jobTotal.value = v.total || 0
  if (v.lines?.length) jobLines.value.push(...v.lines)
  if (jobLines.value.length > 2000) jobLines.value.splice(0, jobLines.value.length - 2000)
  nextTick(() => { const el = jobLogEl.value; if (el) el.scrollTop = el.scrollHeight })
  if (wasRunning && v.status && v.status !== 'running') {
    if (v.status === 'success') message.success('备份任务完成')
    else message.error('备份任务失败：' + (v.error || '未知错误'))
    loadBackup()
  }
}

const fetchJob = async () => {
  try {
    const r = await getQuiet('/api/backup/job', { since: jobTotal.value })
    if (r?.job) mergeJob(r.job)
  } catch { /* 忽略：轮询失败下次再试 */ }
}

const loadJobHistory = async () => {
  try {
    const r = await getQuiet('/api/backup/jobs')
    jobHistory.value = r?.jobs || []
  } catch { /* ignore */ }
}

const stopJobWatch = () => {
  if (jobWsOff) { jobWsOff(); jobWsOff = null }
  window.clearInterval(jobTimer)
  window.clearInterval(tickTimer)
  jobTimer = tickTimer = undefined
}

const startJobWatch = () => {
  if (jobWsOff) return
  jobWsOff = wsTopic('backup-job', (m: any) => {
    if (m.type === 'log') {
      // 按全局行号去重：跳号说明漏帧，定向补拉快照
      if (m.idx === jobTotal.value) {
        jobLines.value.push(m.line)
        jobTotal.value = m.idx + 1
        nextTick(() => { const el = jobLogEl.value; if (el) el.scrollTop = el.scrollHeight })
      } else fetchJob()
    } else if (m.type === 'done') {
      fetchJob().then(() => loadJobHistory())
    }
  })
  jobTimer = window.setInterval(async () => {
    if (job.value?.status !== 'running') { window.clearInterval(jobTimer); jobTimer = undefined; return }
    await fetchJob()
  }, 2000)
  tickTimer = window.setInterval(() => { nowTick.value = Date.now() }, 1000)
}

// 任务化触发统一收尾：立即拉一次任务并开始监听。
const followJob = async () => {
  await fetchJob()
  startJobWatch()
  await loadJobHistory()
}

const createBackup = async () => {
  creating.value = true
  try {
    const r = await post('/api/backup/create', { items: backupItems.value })
    if (r.ok) {
      message.info('备份任务已开始')
      await followJob()
    }
  } finally { creating.value = false }
}

const downloadBackup = (record: any) => {
  window.open(`/api/backup/download?name=${encodeURIComponent(record.name)}`, '_blank')
}

const uploadRemote = async (record: any) => {
  const r = await post(`/api/backup/${encodeURIComponent(record.name)}/upload-remote`)
  if (r.ok) {
    message.info('上传任务已开始')
    await followJob()
  }
}

// ---- 定时备份 ----
const schedule = ref<any>({
  enabled: false, cron: '0 3 * * *', items: [], uploadRemote: false, keep: 10, remoteKeep: 0, lastRun: null, nextRun: '',
})
const schedSaving = ref(false)
const cronPresets = [
  { value: '0 3 * * *', label: '每天 03:00' },
  { value: '0 */6 * * *', label: '每 6 小时' },
  { value: '0 4 * * 0', label: '每周日 04:00' },
  { value: 'custom', label: '自定义' },
]
const cronPreset = ref('0 3 * * *')
const scheduleItemOptions = computed(() => backupItemMetas.value.map((i: any) => ({ value: i.key, label: i.label })))
const remoteReady = computed(() => {
  const k = remote.value.kind
  if (!k || k === 'local') return false
  if (k === 'googledrive' || k === 'onedrive') return !!remote.value.authorized
  return true // WebDAV 凭据在配置里即视为可用
})
const onCronPresetChange = () => {
  if (cronPreset.value !== 'custom') schedule.value.cron = cronPreset.value
}
const syncCronPreset = () => {
  const hit = cronPresets.find((p: any) => p.value === schedule.value.cron)
  cronPreset.value = hit ? hit.value : 'custom'
}
// ---- 备份包加密 ----
const crypt = ref<any>({ enabled: false, password: '', passwordSet: false })
const cryptSaving = ref(false)
const saveCrypt = async () => {
  cryptSaving.value = true
  try {
    const r = await put('/api/backup/crypt', crypt.value)
    if (r.ok) {
      // 回拉脱敏视图，避免把明文密码留在前端状态里。
      const v = await getQuiet('/api/backup/crypt')
      crypt.value = { ...crypt.value, ...(v || {}), password: '' }
      message.success('备份加密设置已保存')
    } else {
      message.error(r.error || '保存失败')
    }
  } finally { cryptSaving.value = false }
}

const saveSchedule = async () => {
  if (!String(schedule.value.cron || '').trim()) {
    message.warning('请填写 cron 表达式')
    return
  }
  if (Number(schedule.value.keep) === 0 && !schedule.value.uploadRemote) {
    message.warning('本地保留 0 份必须开启「自动上传到远程存储」，否则面板将没有任何本地备份')
    return
  }
  schedSaving.value = true
  try {
    const r = await put('/api/backup/schedule', schedule.value)
    if (r.ok) {
      schedule.value = { ...schedule.value, ...r.schedule }
      syncCronPreset()
      message.success('定时备份已保存')
    } else {
      message.error(r.error || '保存失败')
    }
  } finally { schedSaving.value = false }
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
  // 大备份包上传可能远超全局 30s：timeout 0 不限时，仅当真失败（网络/状态码）才报错。
  // 恢复本身是后台任务，上传完成后立即返回 jobId，进度在「备份任务」卡片展示。
  http.post('/api/backup/restore', fd, { headers: { 'Content-Type': 'multipart/form-data' }, timeout: 0 } as any)
    .then((resp: any) => {
      const r = resp.data
      if (r.ok) {
        message.info('恢复任务已开始，完成后建议重启面板并重载 nginx')
        followJob()
      } else message.error(r.error || '恢复失败')
    })
    .catch(() => message.error('恢复失败：备份包上传中断'))
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
  return !!r.ok
}

// 切换通道后旧通道的授权不算数，等重新授权再显示已连接。
const onKindChange = () => {
  remote.value.authorized = false
  remote.value.account = ''
}

// ---- 云盘 OAuth ----
const copyRedirect = async () => {
  try {
    await navigator.clipboard.writeText(oauthRedirect.value)
    message.success('回调地址已复制')
  } catch {
    message.warning('复制失败，请手动选择复制')
  }
}

// 先落库客户端凭据（授权回调要用），再打开厂商授权页，轮询授权状态。
const startOAuth = async () => {
  if (!remote.value.clientId.trim()) {
    message.warning('请先填写 Client ID')
    return
  }
  if (!remote.value.clientSecret.trim() && !remote.value.clientSecretSet) {
    message.warning('请先填写 Client Secret')
    return
  }
  if (!(await saveRemote())) return
  const r = await post('/api/backup/oauth/start', {
    kind: remote.value.kind,
    return: '/settings?tab=backup',
    redirectUri: oauthRedirect.value,
  })
  // 以授权地址为准：后端成功时返回 { ok, url }，个别代理/旧版本可能不带 ok。
  if (!r?.url) {
    message.error(r?.error || '无法发起授权')
    return
  }
  const win = window.open(r.url, '_blank')
  if (!win) {
    // 弹窗被拦截：直接当前页跳转，回调页会自动关回设置页
    location.href = r.url
    return
  }
  authorizing.value = true
  window.clearInterval(oauthTimer)
  let tries = 0
  oauthTimer = window.setInterval(async () => {
    tries += 1
    if (tries > 150) { // 约 5 分钟未完成则停止轮询
      window.clearInterval(oauthTimer)
      authorizing.value = false
      return
    }
    const cur = await getQuiet('/api/backup/remote')
    if (cur?.authorized && cur?.kind === remote.value.kind) {
      window.clearInterval(oauthTimer)
      authorizing.value = false
      message.success(`云盘授权成功${cur.account ? '：' + cur.account : ''}`)
      await loadBackup()
    }
  }, 2000)
}

const clearOAuth = async () => {
  const r = await post('/api/backup/oauth/clear', {})
  if (r.ok) {
    message.success('已断开云盘授权')
    await loadBackup()
  }
}

const auditCols: any[] = [
  { title: "时间", dataIndex: "time", key: "time", width: 170 },
  { title: '操作', dataIndex: 'action', key: 'action', width: 200 },
  { title: '详情', dataIndex: 'detail', key: 'detail' },
  { title: 'IP', dataIndex: 'ip', key: 'ip', width: 130 },
]

const loading = ref(false)

const load = async () => {
  const tab = new URLSearchParams(location.search).get('tab')
  if (tab) activeTab.value = tab
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
// 离开设置页停掉备份任务监听（回来 onActivated 会自动恢复），OAuth 轮询一并清理。
onDeactivated(() => {
  stopJobWatch()
  window.clearInterval(oauthTimer)
})
onUnmounted(() => {
  stopJobWatch()
  window.clearInterval(oauthTimer)
})
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
.backup-card { margin-bottom: 16px; }
.item-group { margin-top: 12px; }
.item-row { padding: 6px 0; }
.item-label { font-size: 13px; }
.item-desc { font-size: 12px; color: var(--color-slate); }
/* 备份任务实时日志：与 AppsPage 的 compose 输出窗同款 */
.runlog { max-height: 260px; overflow: auto; background: var(--surface-card); color: var(--color-ink); padding: 10px 12px; font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-all; border: 1px solid var(--border-card); border-radius: 8px; font-family: ui-monospace, Consolas, monospace; margin: 0; }
</style>
