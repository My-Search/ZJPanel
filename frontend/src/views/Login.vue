<template>
  <div class="login-page">
    <!-- 左侧品牌区：私有部署控制台定位 + 能力清单，深色背景在深浅主题下都保持一致的商务观感 -->
    <aside class="brand-panel">
      <div class="brand-inner">
        <div class="brand-lockup">
          <span class="brand-badge"><img src="/favicon.svg" alt="ZJPanel" /></span>
          <span class="brand-word">
            <span class="brand-name">ZJPanel</span>
            <span class="brand-sub">自托管运维控制台</span>
          </span>
        </div>

        <div class="brand-copy">
          <h1>把服务器运维<br />收进一个控制台</h1>
          <p>站点、证书、容器与主机，一个面板完成日常运维。</p>
        </div>

        <ul class="brand-features">
          <li v-for="f in features" :key="f.title">
            <span class="feat-icon"><component :is="f.icon" /></span>
            <span class="feat-body">
              <strong>{{ f.title }}</strong>
              <em>{{ f.desc }}</em>
            </span>
          </li>
        </ul>

        <div class="brand-foot">
          <SafetyCertificateOutlined />
          <span>私有部署 · 数据与凭据只留在你的服务器</span>
        </div>
      </div>
    </aside>

    <!-- 右侧表单区：企业控制台登录，标题 + 大号输入框 + 主按钮 -->
    <main class="form-panel">
      <div class="form-inner">
        <!-- 窄屏隐藏左侧品牌区，这里补一个紧凑品牌行 -->
        <div class="form-brand">
          <img src="/favicon.svg" alt="ZJPanel" />
          <span>ZJPanel</span>
        </div>

        <header class="form-head">
          <h2>{{ initialized ? '登录控制台' : '初始化控制台' }}</h2>
          <p>{{ initialized ? '使用管理员账号继续访问面板' : '首次使用，请创建管理员账号' }}</p>
        </header>

        <a-alert
          v-if="!initialized"
          type="info"
          show-icon
          message="密码至少 8 位，创建后即可登录"
          style="margin: 20px 0 4px"
        />

        <a-form class="login-form" layout="vertical" @submit.prevent="submit">
          <a-form-item label="用户名">
            <a-input
              v-model:value="username"
              size="large"
              placeholder="请输入用户名"
              autocomplete="username"
            >
              <template #prefix><UserOutlined /></template>
            </a-input>
          </a-form-item>
          <a-form-item label="密码">
            <a-input-password
              v-model:value="password"
              size="large"
              placeholder="请输入密码"
              autocomplete="current-password"
              @pressEnter="submit"
            >
              <template #prefix><LockOutlined /></template>
            </a-input-password>
          </a-form-item>
          <a-button type="primary" html-type="submit" size="large" block :loading="loading" class="submit-btn">
            <template #icon><LoginOutlined /></template>
            {{ initialized ? '登录' : '创建并登录' }}
          </a-button>
        </a-form>

        <p class="form-note"><SafetyOutlined />凭据仅在本机校验，登录失败会被限流</p>

        <footer class="form-foot">ZJPanel · Self-hosted Infrastructure Panel</footer>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import {
  AppstoreOutlined, CloudServerOutlined, GlobalOutlined, LockOutlined, LoginOutlined,
  SafetyCertificateOutlined, SafetyOutlined, UserOutlined,
} from '@ant-design/icons-vue'
import { useAuth } from '../store/auth'

const router = useRouter()
const auth = useAuth()
const initialized = ref(true)
const username = ref('')
const password = ref('')
const loading = ref(false)

// 左侧能力清单：对应面板的四大模块，纯静态文案（非响应式，图标组件无需 markRaw）
const features = [
  { icon: GlobalOutlined, title: '站点与 Nginx', desc: '站点配置、配置校验与一键重载' },
  { icon: SafetyCertificateOutlined, title: '证书自动化', desc: 'ACME 签发、自动续期与多目标部署' },
  { icon: AppstoreOutlined, title: '应用与容器', desc: 'Compose 应用启停、日志与升级' },
  { icon: CloudServerOutlined, title: '主机与防御', desc: 'SSH 终端、Agent 监控与 CC 防护' },
]

onMounted(async () => {
  try {
    const r = await auth.refresh(true)
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
/* ============ 企业分栏登录页 ============
 * 左：深色品牌区（两种主题下恒定，塑造商务信任感）
 * 右：浅色表单区（跟随主题令牌）
 * 窄屏（≤960px）：折叠品牌区，表单全屏 + 顶部紧凑品牌行
 */
.login-page {
  min-height: 100vh;
  min-height: 100dvh; /* 移动端地址栏会改变可视高度，按真实可视区铺满 */
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 560px);
  background: var(--surface-card);
  font-family: var(--font-sans);
}

/* ---------- 左侧品牌区 ---------- */
.brand-panel {
  position: relative;
  display: flex;
  align-items: center;
  padding: 48px clamp(36px, 5.5vw, 88px);
  overflow-x: hidden;
  overflow-y: auto;
  color: #e2e8f0;
  border-right: 1px solid rgba(148, 163, 184, .14); /* 深色主题下与右侧表单区分隔 */
  background:
    radial-gradient(ellipse 70% 55% at 10% 6%, rgba(37, 99, 235, .38), transparent 62%),
    radial-gradient(ellipse 62% 46% at 94% 98%, rgba(14, 165, 233, .24), transparent 60%),
    linear-gradient(155deg, #0b1220 0%, #101b36 52%, #0a1526 100%);
}
/* 细网格：技术感装饰，中心可见、边缘淡出 */
.brand-panel::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background-image:
    linear-gradient(rgba(148, 163, 184, .10) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, .10) 1px, transparent 1px);
  background-size: 56px 56px;
  mask-image: radial-gradient(ellipse 80% 72% at 26% 26%, #000 18%, transparent 92%);
}
.brand-inner {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 520px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 36px;
}

/* 品牌标识：logo 自带蓝底渐变，直接使用，不再套白底 */
.brand-lockup { display: flex; align-items: center; gap: 14px; }
.brand-badge {
  width: 46px; height: 46px; flex: none;
  border-radius: 12px; overflow: hidden;
  box-shadow: 0 10px 26px rgba(2, 6, 23, .45), 0 0 0 1px rgba(148, 163, 184, .18);
}
.brand-badge img { display: block; width: 100%; height: 100%; }
.brand-word { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.brand-name { font-size: 19px; font-weight: 600; letter-spacing: .02em; color: #fff; }
.brand-sub { font-size: 12px; letter-spacing: .08em; color: rgba(148, 163, 184, .92); }

.brand-copy h1 {
  margin: 0;
  font-size: clamp(30px, 3vw, 40px);
  line-height: 1.3;
  font-weight: 600;
  letter-spacing: -.01em;
  color: #f8fafc;
}
.brand-copy p {
  margin: 16px 0 0;
  max-width: 420px;
  font-size: 15px;
  line-height: 1.7;
  color: rgba(203, 213, 225, .78);
}

.brand-features { list-style: none; margin: 0; padding: 0; display: grid; gap: 14px; }
.brand-features li { display: flex; align-items: flex-start; gap: 14px; }
.feat-icon {
  width: 38px; height: 38px; flex: none;
  display: grid; place-items: center;
  font-size: 17px; color: #bfdbfe;
  background: rgba(59, 130, 246, .12);
  border: 1px solid rgba(96, 165, 250, .24);
  border-radius: 10px;
}
.feat-body { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.feat-body strong { font-size: 14px; font-weight: 600; color: #e2e8f0; }
.feat-body em { font-style: normal; font-size: 12.5px; line-height: 1.5; color: rgba(148, 163, 184, .92); }

.brand-foot {
  display: flex; align-items: center; gap: 8px;
  padding-top: 18px;
  border-top: 1px solid rgba(148, 163, 184, .16);
  font-size: 12px;
  color: rgba(148, 163, 184, .9);
}

/* ---------- 右侧表单区 ---------- */
.form-panel {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 48px clamp(24px, 4vw, 64px);
  background: var(--surface-card);
}
.form-inner { width: 100%; max-width: 372px; }
.form-brand { display: none; }

.form-head h2 {
  margin: 0;
  font-size: 26px;
  font-weight: 600;
  letter-spacing: -.02em;
  color: var(--color-ink);
}
.form-head p { margin: 8px 0 0; font-size: 14px; color: var(--color-slate); }

.login-form { margin-top: 26px; }
.login-form :deep(.ant-form-item) { margin-bottom: 18px; }
.login-form :deep(.ant-form-item-label > label) { font-size: 13px; font-weight: 500; color: var(--color-ink); }
.login-form :deep(.ant-input-affix-wrapper-lg) { min-height: 44px; border-radius: var(--radius-inputs); }
.login-form :deep(.ant-input-prefix) { margin-inline-end: 9px; color: var(--color-slate); }

.submit-btn {
  height: 44px;
  margin-top: 6px;
  font-size: 15px;
  font-weight: 600;
  border-radius: var(--radius-buttons);
  box-shadow: 0 6px 16px rgba(30, 64, 175, .18);
}

.form-note {
  display: flex; align-items: center; gap: 6px;
  margin: 18px 0 0;
  font-size: 12px;
  color: var(--color-slate);
}
.form-foot {
  margin-top: 44px;
  font-size: 11.5px;
  letter-spacing: .06em;
  color: #94a3b8;
}

/* ---------- 深色主题微调 ---------- */
html[data-theme='dark'] .submit-btn { box-shadow: 0 6px 18px rgba(2, 6, 23, .55); }
html[data-theme='dark'] .form-foot { color: #64748b; }

/* ---------- 响应式 ---------- */
@media (max-width: 960px) {
  .login-page { grid-template-columns: minmax(0, 1fr); }
  .brand-panel { display: none; }
  .form-panel {
    min-height: 100dvh;
    align-items: flex-start; /* 移动端顶部对齐，比垂直居中更像正式登录页 */
    background: var(--surface-canvas);
    padding: 56px 20px 40px;
  }
  .form-inner { max-width: 400px; }
  .form-brand {
    display: flex; align-items: center; gap: 10px;
    margin-bottom: 30px;
  }
  .form-brand img { width: 34px; height: 34px; border-radius: 9px; }
  .form-brand span { font-size: 17px; font-weight: 600; color: var(--color-ink); }
}
/* 矮屏（笔记本横屏）：压缩品牌区节奏，避免纵向溢出 */
@media (max-height: 820px) and (min-width: 961px) {
  .brand-panel { padding-top: 36px; padding-bottom: 36px; }
  .brand-inner { gap: 26px; }
  .brand-copy h1 { font-size: 30px; }
  .brand-features { gap: 10px; }
}
</style>
