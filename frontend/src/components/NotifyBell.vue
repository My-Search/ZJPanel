<template>
  <a-popover
    v-model:open="popOpen"
    trigger="click"
    placement="bottomRight"
    :overlay-style="{ maxWidth: '420px' }"
    @open-change="onPopChange"
  >
    <template #content>
      <div class="nb-panel">
        <div class="nb-head">
          <b>通知中心</b>
          <a @click.prevent="doMarkAll" class="nb-link">全部已读</a>
          <a @click.prevent="openSettings" class="nb-link">通知设置</a>
        </div>
        <a-spin :spinning="loading">
          <div class="nb-list">
            <div v-if="!notify.list.length" class="nb-empty">暂无通知</div>
            <div
              v-for="it in notify.list"
              :key="it.id"
              class="nb-item"
              :class="{ unread: !it.readAt }"
              @click="readOne(it)"
            >
              <span class="nb-dot" :class="it.level" :title="levelText(it.level)"></span>
              <div class="nb-main">
                <div class="nb-title">
                  <span class="nb-type">{{ it.type }}</span>
                  {{ it.title }}
                </div>
                <div v-if="it.body" class="nb-body">{{ it.body }}</div>
                <div class="nb-time">{{ fmtTime(it.createdAt) }} · {{ it.source }}</div>
              </div>
            </div>
          </div>
        </a-spin>
      </div>
    </template>
    <a-badge :count="notify.unread" :offset="[-2, 4]" size="small">
      <a-button type="text" class="bell-btn" title="通知中心">
        <BellOutlined />
      </a-button>
    </a-badge>
  </a-popover>

  <!-- 通知设置：SMTP + 类型订阅 -->
  <a-drawer v-model:open="setOpen" title="通知设置" :width="isMobile ? '92%' : 460" placement="right">
    <template v-if="st">
      <a-alert
        type="info"
        show-icon
        message="站内信始终开启；勾选某消息类型后，该类通知会同时发送到收件邮箱。"
        style="margin-bottom: 14px"
      />
      <a-form layout="vertical" style="margin-bottom: 4px">
        <a-row :gutter="8">
          <a-col :span="16">
            <a-form-item label="SMTP 服务器">
              <a-input v-model:value="st.smtpHost" placeholder="smtp.gmail.com" />
            </a-form-item>
          </a-col>
          <a-col :span="8">
            <a-form-item label="端口">
              <a-input-number v-model:value="st.smtpPort" :min="1" :max="65535" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="8">
          <a-col :span="12">
            <a-form-item label="SMTP 用户">
              <a-input v-model:value="st.smtpUser" placeholder="留空则不认证" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item :label="st.hasPass ? 'SMTP 密码（留空沿用）' : 'SMTP 密码'">
              <a-input-password v-model:value="passInput" :placeholder="st.hasPass ? '••••••' : ''" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="8">
          <a-col :span="12">
            <a-form-item label="发件人别名（收件箱里显示的发件人名）">
              <a-input v-model:value="st.smtpFromAlias" placeholder="ZJPanel" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="发件邮箱（留空用 SMTP 用户）">
              <a-input v-model:value="st.smtpFrom" placeholder="默认用 SMTP 用户" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="收件邮箱（订阅通知发送到）">
          <a-input v-model:value="st.rcpt" placeholder="me@example.com" />
        </a-form-item>
        <a-space>
          <a-button :loading="testing" @click="doTest">发送测试邮件</a-button>
          <a-button type="primary" :loading="saving" @click="doSave">保存设置</a-button>
        </a-space>
      </a-form>

      <a-divider style="margin: 16px 0 8px">邮件订阅（按消息类型）</a-divider>
      <div class="sub-list">
        <div v-for="t in st.types" :key="t" class="sub-row">
          <span class="sub-type">{{ t }}</span>
          <a-switch
            :checked="!!st.subs[t]"
            size="small"
            @change="(v: any) => (st!.subs[t] = !!v)"
          />
        </div>
      </div>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { BellOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useNotify, type NotifyItem, type NotifySettings } from '../store/notify'
import { wsTopic } from '../api'

const notify = useNotify()
const popOpen = ref(false)
const setOpen = ref(false)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const passInput = ref('')
const st = computed(() => notify.settings as NotifySettings | null)
const isMobile = ref(window.innerWidth < 768)

const levelText = (l: string) => ({ info: '信息', warn: '警告', error: '错误' }[l] || l)

const fmtTime = (t: string) => {
  const d = new Date(t)
  const today = new Date()
  const sameDay = d.toDateString() === today.toDateString()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  return sameDay ? `今天 ${hm}` : `${d.getMonth() + 1}-${d.getDate()} ${hm}`
}

async function onPopChange(open: boolean) {
  if (open) {
    loading.value = true
    try { await notify.fetchList(30) } finally { loading.value = false }
  }
}
const readOne = (it: NotifyItem) => {
  if (!it.readAt) notify.markRead([it.id])
}
const doMarkAll = () => notify.markAll()

const openSettings = async () => {
  popOpen.value = false
  setOpen.value = true
  passInput.value = ''
  await notify.loadSettings()
  // 默认值：Gmail SMTP / ZJPanel 别名（仅在未保存过配置时预填，保存后以用户数据为准）
  const s = notify.settings
  if (s) {
    if (!s.smtpHost && !s.hasPass) {
      s.smtpHost = 'smtp.gmail.com'
      s.smtpPort = s.smtpPort || 465
    }
    if (!s.smtpFromAlias) s.smtpFromAlias = 'ZJPanel'
  }
}
const doSave = async () => {
  if (!st.value) return
  saving.value = true
  try {
    await notify.saveSettings({
      smtpHost: st.value.smtpHost, smtpPort: st.value.smtpPort, smtpUser: st.value.smtpUser,
      smtpPass: passInput.value, smtpFrom: st.value.smtpFrom, smtpFromAlias: st.value.smtpFromAlias,
      rcpt: st.value.rcpt, subs: st.value.subs,
    })
    passInput.value = ''
    await notify.loadSettings()
    if (!notify.settings?.smtpFromAlias) notify.settings!.smtpFromAlias = 'ZJPanel'
    message.success('已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally {
    saving.value = false
  }
}
const doTest = async () => {
  testing.value = true
  try {
    const r = await notify.testMail()
    r.ok ? message.success('测试邮件已发送，请查收') : message.error(r.error || '发送失败')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '发送失败')
  } finally {
    testing.value = false
  }
}

// 实时：WS 推送新通知 → 刷新未读；弹窗开着时顺带刷新列表
let stopWs: (() => void) | null = null
const startWs = () => {
  stopWs = wsTopic('notify', () => {
    notify.fetchUnread()
    if (popOpen.value) notify.fetchList(30)
  })
}
startWs()
// 兜底轮询
const timer = window.setInterval(() => notify.fetchUnread(), 60000)
const onResize = () => { isMobile.value = window.innerWidth < 768 }
window.addEventListener('resize', onResize)
onUnmounted(() => { stopWs?.(); window.clearInterval(timer); window.removeEventListener('resize', onResize) })
</script>

<style scoped>
.nb-panel { width: 380px; max-width: calc(100vw - 32px); }
.nb-head { display: flex; align-items: center; gap: 12px; padding-bottom: 8px; border-bottom: 1px solid rgba(128,128,128,.2); }
.nb-head b { margin-right: auto; font-size: 13.5px; }
.nb-link { font-size: 12px; }
.nb-list { max-height: 380px; overflow: auto; margin-top: 4px; }
.nb-empty { text-align: center; color: #999; padding: 28px 0; font-size: 12.5px; }
.nb-item { display: flex; gap: 8px; padding: 8px 4px; border-bottom: 1px solid rgba(128,128,128,.12); cursor: pointer; }
.nb-item:last-child { border-bottom: none; }
.nb-item:hover { background: rgba(128,128,128,.08); }
.nb-item.unread .nb-title { font-weight: 600; }
.nb-dot { width: 8px; height: 8px; border-radius: 50%; margin-top: 6px; flex-shrink: 0; background: #999; }
.nb-dot.info { background: #3d7fd9; }
.nb-dot.warn { background: #d9a13d; }
.nb-dot.error { background: #e5484d; }
.nb-main { flex: 1; min-width: 0; }
.nb-title { font-size: 13px; word-break: break-all; }
.nb-type { font-size: 11px; color: #999; background: rgba(128,128,128,.15); border-radius: 4px; padding: 0 5px; margin-right: 6px; }
.nb-body { font-size: 12px; color: #999; margin-top: 2px; word-break: break-all; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.nb-time { font-size: 11px; color: #aaa; margin-top: 2px; }
.bell-btn { padding: 0 8px; font-size: 15px; }
.sub-list { border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 4px 12px; max-height: 300px; overflow: auto; }
.sub-row { display: flex; align-items: center; justify-content: space-between; padding: 7px 0; border-bottom: 1px solid rgba(128,128,128,.12); }
.sub-row:last-child { border-bottom: none; }
.sub-type { font-family: ui-monospace, Consolas, monospace; font-size: 12.5px; }
</style>
