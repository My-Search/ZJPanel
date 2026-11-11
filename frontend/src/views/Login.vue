<template>
  <div class="login-wrap">
    <div class="login-brand">
      <div class="brand-badge"><img src="/favicon.svg" alt="ZJPanel" /></div>
      <h1>ZJPanel</h1>
      <p>一体化站点 · 应用 · 证书运维面板</p>
    </div>
    <a-card class="login-card" :title="initialized ? '登录 ZJPanel' : '初始化 ZJPanel'">
      <a-alert v-if="!initialized" type="info" show-icon message="首次使用：创建管理员账号（密码至少 8 位）" style="margin-bottom: 16px" />
      <a-form layout="vertical" @submit.prevent="submit">
        <a-form-item label="用户名">
          <a-input v-model:value="username" placeholder="用户名" size="large" />
        </a-form-item>
        <a-form-item label="密码">
          <a-input-password v-model:value="password" placeholder="密码" size="large" @pressEnter="submit" />
        </a-form-item>
        <a-button type="primary" html-type="submit" size="large" block :loading="loading">
          {{ initialized ? '登 录' : '创建并登录' }}
        </a-button>
      </a-form>
    </a-card>
    <div class="login-foot">ZJPanel · Self-hosted Infrastructure Panel</div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import { useAuth } from '../store/auth'

const router = useRouter()
const auth = useAuth()
const initialized = ref(true)
const username = ref('')
const password = ref('')
const loading = ref(false)

onMounted(async () => {
  try {
    const r = await auth.refresh()
    initialized.value = r.initialized
    if (r.loggedIn) router.replace('/')
  } catch { /* ignore */ }
})

const submit = async () => {
  if (!username.value || !password.value) { message.warning('请填写用户名与密码'); return }
  loading.value = true
  try {
    if (initialized.value) await auth.login(username.value, password.value)
    else {
      if (password.value.length < 8) { message.warning('密码至少 8 位'); loading.value = false; return }
      await auth.init(username.value, password.value)
    }
    router.replace('/')
  } catch { /* message 已由拦截器展示 */ }
  loading.value = false
}
</script>

<style scoped>
.login-wrap {
  min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center;
  background:
    radial-gradient(ellipse 60% 45% at 18% 12%, rgba(76, 110, 245, .32), transparent 60%),
    radial-gradient(ellipse 55% 40% at 85% 85%, rgba(112, 72, 232, .30), transparent 62%),
    radial-gradient(ellipse 45% 35% at 75% 15%, rgba(15, 118, 214, .22), transparent 60%),
    linear-gradient(160deg, #0b1220 0%, #101a30 55%, #0b1322 100%);
  position: relative; padding: 24px; overflow: hidden;
}
/* 细网格纹理（企业科技感） */
.login-wrap::before {
  content: ''; position: absolute; inset: 0; pointer-events: none;
  background-image:
    linear-gradient(rgba(148, 163, 184, .055) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, .055) 1px, transparent 1px);
  background-size: 44px 44px;
  mask-image: radial-gradient(ellipse 70% 60% at 50% 45%, #000 30%, transparent 100%);
}
/* 顶部高光弧 */
.login-wrap::after {
  content: ''; position: absolute; top: -240px; left: 50%; transform: translateX(-50%);
  width: 760px; height: 380px; border-radius: 50%; pointer-events: none;
  background: radial-gradient(ellipse at center, rgba(96, 130, 255, .18), transparent 68%);
}
.login-brand { text-align: center; margin-bottom: 26px; color: #eef2ff; position: relative; z-index: 1; }
.brand-badge {
  width: 62px; height: 62px; margin: 0 auto 12px; border-radius: 18px; overflow: hidden;
  background: rgba(255,255,255,.06);
  box-shadow: inset 0 1px 0 rgba(255,255,255,.22), 0 10px 30px rgba(2, 6, 23, .55), 0 0 0 1px rgba(148,163,184,.18);
}
.brand-badge img { width: 100%; height: 100%; display: block; }
.login-brand h1 { margin: 0; font-size: 28px; letter-spacing: 3px; font-weight: 700; color: #fff; }
.login-brand p { margin: 7px 0 0; font-size: 13px; color: #94a3b8; letter-spacing: 2px; }
.login-card {
  width: 400px; max-width: 92vw; border-radius: 14px; position: relative; z-index: 1;
  box-shadow: 0 24px 70px rgba(2, 6, 23, .6), 0 0 0 1px rgba(148, 163, 184, .14);
}
.login-foot { margin-top: 24px; color: #64748b; font-size: 12px; letter-spacing: 1.5px; position: relative; z-index: 1; }
@media (max-width: 480px) {
  .login-brand h1 { font-size: 22px; }
  .login-card { width: 94vw; }
}
</style>
