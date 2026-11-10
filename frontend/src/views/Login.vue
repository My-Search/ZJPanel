<template>
  <div class="login-wrap">
    <div class="login-brand">
      <div class="brand-badge">🚀</div>
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
  background: linear-gradient(135deg, #3b5bdb 0%, #5f3dc4 50%, #7048e8 100%); padding: 24px;
}
.login-brand { text-align: center; margin-bottom: 26px; color: #fff; }
.brand-badge {
  width: 62px; height: 62px; margin: 0 auto 12px; border-radius: 18px;
  background: rgba(255,255,255,.16); backdrop-filter: blur(6px);
  display: flex; align-items: center; justify-content: center; font-size: 30px;
  box-shadow: inset 0 1px 0 rgba(255,255,255,.25), 0 8px 24px rgba(0,0,0,.18);
}
.login-brand h1 { margin: 0; font-size: 28px; letter-spacing: 2px; font-weight: 700; }
.login-brand p { margin: 6px 0 0; font-size: 13px; opacity: .78; letter-spacing: 1px; }
.login-card {
  width: 400px; max-width: 92vw; border-radius: 14px; box-shadow: 0 18px 48px rgba(0,0,0,.28);
  border: 1px solid rgba(255,255,255,.35);
}
.login-foot { margin-top: 22px; color: rgba(255,255,255,.55); font-size: 12px; letter-spacing: 1px; }
@media (max-width: 480px) {
  .login-brand h1 { font-size: 22px; }
  .login-card { width: 94vw; }
}
</style>
