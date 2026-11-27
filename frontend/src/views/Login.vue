<template>
  <a-config-provider :theme="loginTheme" :locale="zhCN">
    <div class="login-page">
      <!-- 左侧品牌区：与表单共用同一块深色画布，不再单独铺底色 -->
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

      <!-- 右侧表单区：玻璃卡片浮在同一画布上，避免左右两块的割裂感 -->
      <main class="form-panel">
        <section class="form-card">
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
        </section>
      </main>
    </div>
  </a-config-provider>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { message, theme as antdTheme } from 'ant-design-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
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

// 登录页固定深色商务视觉：左侧品牌区本身就恒定深色，右侧若跟随浅色主题会再次劈成两半，
// 因此局部锁死 antd 暗色算法，只让表单控件、按钮与提示跟随这套深色令牌。
const loginTheme = {
  algorithm: antdTheme.darkAlgorithm,
  token: {
    colorPrimary: '#3b82f6',
    colorInfo: '#3b82f6',
    colorLink: '#60a5fa',
    colorText: '#e2e8f0',
    colorTextSecondary: '#94a3b8',
    colorTextPlaceholder: '#64748b',
    colorBgContainer: '#0f1c33',
    colorBgElevated: '#12203a',
    colorBgLayout: '#081120',
    colorBorder: 'rgba(148, 163, 184, .28)',
    borderRadius: 8,
    fontFamily:
      "'Inter', ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif",
  },
}

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
/* ============ 一体化深色登录页 ============
 * 整页只有一块画布：深海军蓝渐变 + 细网格贯穿左右，品牌文案与表单都浮在同一背景上；
 * 右侧表单收进半透明玻璃卡片，靠边框与投影建立层次，而不是用整块异色面板切一刀。
 * 窄屏（≤960px）：隐藏品牌区，卡片居中，顶部保留紧凑品牌行。
 */
.login-page {
  position: relative;
  min-height: 100vh;
  min-height: 100dvh; /* 移动端地址栏会改变可视高度，按真实可视区铺满 */
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 600px);
  background:
    radial-gradient(ellipse 70% 60% at 8% 4%, rgba(37, 99, 235, .32), transparent 62%),
    radial-gradient(ellipse 55% 55% at 96% 96%, rgba(14, 165, 233, .18), transparent 62%),
    linear-gradient(152deg, #070d18 0%, #0d1930 48%, #081322 100%);
  color: #e2e8f0;
  color-scheme: dark;
  font-family: var(--font-sans);
}

/* 细网格：技术感装饰，同一条纹理横跨左右，中心可见、边缘淡出 */
.login-page::before {
  content: '';
  position: absolute;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  background-image:
    linear-gradient(rgba(148, 163, 184, .09) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, .09) 1px, transparent 1px);
  background-size: 56px 56px;
  mask-image: radial-gradient(ellipse 92% 88% at 42% 30%, #000 24%, transparent 94%);
}

/* ---------- 左侧品牌区 ---------- */
.brand-panel {
  position: relative;
  z-index: 1;
  display: flex;
  align-items: center;
  padding: 48px clamp(36px, 5.5vw, 88px);
  overflow-x: hidden;
  overflow-y: auto;
  color: #e2e8f0;
}
.brand-inner {
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
  position: relative;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px clamp(24px, 4vw, 56px);
  overflow-y: auto;
}

.form-card {
  position: relative;
  width: 100%;
  max-width: 420px;
  padding: 38px 34px 30px;
  border-radius: 20px;
  background: linear-gradient(165deg, rgba(24, 40, 68, .72), rgba(9, 17, 31, .62));
  border: 1px solid rgba(148, 163, 184, .16);
  box-shadow:
    0 30px 80px rgba(2, 6, 23, .55),
    0 0 0 1px rgba(96, 165, 250, .05),
    0 0 90px rgba(37, 99, 235, .10);
  backdrop-filter: blur(18px) saturate(120%);
}
/* 顶部一条极淡的蓝色高光，让卡片边缘与背景有连续的光照感 */
.form-card::before {
  content: '';
  position: absolute;
  inset: 0 0 auto;
  height: 1px;
  border-radius: 20px 20px 0 0;
  background: linear-gradient(90deg, transparent, rgba(96, 165, 250, .4), transparent);
}

.form-brand { display: none; }

.form-head h2 {
  margin: 0;
  font-size: 26px;
  font-weight: 600;
  letter-spacing: -.02em;
  color: #f8fafc;
}
.form-head p { margin: 8px 0 0; font-size: 14px; color: #94a3b8; }

.login-form { margin-top: 26px; }
.login-form :deep(.ant-form-item) { margin-bottom: 18px; }
.login-form :deep(.ant-form-item-label > label) { font-size: 13px; font-weight: 500; color: #cbd5e1; }
.login-form :deep(.ant-input-affix-wrapper-lg) { min-height: 44px; border-radius: var(--radius-inputs); }
.login-form :deep(.ant-input-prefix) { margin-inline-end: 9px; color: #94a3b8; }
.login-form :deep(.ant-input) { background: transparent; color: #e2e8f0; }
/* 浏览器自动填充会把输入框刷成亮黄/浅蓝，在深色卡片上非常刺眼，统一压回卡片底色 */
.login-form :deep(input:-webkit-autofill) {
  -webkit-box-shadow: 0 0 0 1000px #0f1c33 inset;
  -webkit-text-fill-color: #e2e8f0;
  transition: background-color 9999s ease-out;
}

.submit-btn {
  height: 46px;
  margin-top: 8px;
  font-size: 15px;
  font-weight: 600;
  border: none;
  border-radius: 10px;
  background: linear-gradient(135deg, #2563eb 0%, #3b82f6 100%);
  box-shadow: 0 10px 24px rgba(37, 99, 235, .35);
}
.submit-btn:hover {
  background: linear-gradient(135deg, #1d4ed8 0%, #2563eb 100%);
  box-shadow: 0 12px 28px rgba(37, 99, 235, .42);
}

.form-note {
  display: flex; align-items: center; gap: 6px;
  margin: 18px 0 0;
  font-size: 12px;
  color: #94a3b8;
}
.form-foot {
  margin-top: 32px;
  padding-top: 16px;
  border-top: 1px solid rgba(148, 163, 184, .12);
  font-size: 11.5px;
  letter-spacing: .04em;
  color: #64748b;
}

/* ---------- 响应式 ---------- */
@media (max-width: 960px) {
  .login-page { display: block; }
  .brand-panel { display: none; }
  .form-panel {
    min-height: 100dvh;
    padding: 32px 18px;
    align-items: center;
    overflow-y: visible;
  }
  .form-card { max-width: 400px; padding: 30px 24px 26px; }
  .form-brand {
    display: flex; align-items: center; gap: 10px;
    margin-bottom: 26px;
  }
  .form-brand img { width: 34px; height: 34px; border-radius: 9px; }
  .form-brand span { font-size: 17px; font-weight: 600; color: #e2e8f0; }
}
/* 矮屏（笔记本横屏）：压缩品牌区与卡片节奏，避免纵向溢出 */
@media (max-height: 820px) and (min-width: 961px) {
  .brand-panel { padding-top: 36px; padding-bottom: 36px; }
  .brand-inner { gap: 26px; }
  .brand-copy h1 { font-size: 30px; }
  .brand-features { gap: 10px; }
  .form-card { padding: 30px 32px 24px; }
  .form-foot { margin-top: 24px; }
}
</style>
