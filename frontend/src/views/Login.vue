<template>
  <div class="login-wrap">
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
.login-wrap { height: 100vh; display: flex; align-items: center; justify-content: center; background: linear-gradient(135deg, #4f7cff 0%, #7c4dff 100%); }
.login-card { width: 400px; border-radius: 12px; box-shadow: 0 12px 40px rgba(0,0,0,.18); }
</style>
