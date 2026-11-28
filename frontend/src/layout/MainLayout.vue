<template>
  <a-layout class="app-shell">
    <!-- 桌面：固定侧栏；移动：抽屉 -->
    <a-layout-sider
      v-if="!isMobile"
      v-model:collapsed="collapsed"
      collapsible
      :theme="ui.isDark ? 'dark' : 'light'"
      :width="220"
    >
      <div class="logo"><img src="/favicon.svg" alt="" class="logo-mark" /> {{ collapsed ? '' : 'ZJPanel' }}</div>
      <SideMenu :selected="selected" :plugin-menus="pluginMenus" @navigate="onMenu" />
    </a-layout-sider>
    <a-drawer
      v-else
      v-model:open="drawerOpen"
      placement="left"
      :width="220"
      :closable="false"
      :body-style="{ padding: 0 }"
      class="nav-drawer"
    >
      <div class="logo"><img src="/favicon.svg" alt="" class="logo-mark" /> ZJPanel</div>
      <SideMenu :selected="selected" :plugin-menus="pluginMenus" @navigate="onMenu" />
    </a-drawer>

    <a-layout class="main-col">
      <a-layout-header class="header">
        <div class="header-left">
          <a-button v-if="isMobile" type="text" class="menu-btn" @click="drawerOpen = true">
            <MenuOutlined />
          </a-button>
          <span class="ver">ZJPanel {{ version }}</span>
        </div>
        <a-space :size="4">
          <NotifyBell />
          <a-dropdown>
            <a-button type="text" class="theme-btn" :title="'主题：' + themeLabel">
              <SkinOutlined />
            </a-button>
            <template #overlay>
              <a-menu :selected-keys="[ui.mode]" @click="onThemeChange">
                <a-menu-item key="light">浅色</a-menu-item>
                <a-menu-item key="dark">深色</a-menu-item>
                <a-menu-item key="system">跟随系统</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
          <a-dropdown>
            <span class="user"><UserOutlined /> {{ auth.username || 'admin' }}</span>
            <template #overlay>
              <a-menu>
                <a-menu-item key="logout" @click="doLogout">退出登录</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </a-space>
      </a-layout-header>
      <a-layout-content class="content">
        <!-- 页面保活：切换菜单立即显示上次状态，返回时后台刷新数据。
             过渡：out-in（旧页淡出→新页淡入），140ms 单程，避免整页硬切的生硬感；
             transition 必须包在 keep-alive 外层（Vue 官方约定）；
             :key=route.path 强制 vnode 身份随路径变化 —— Transition+KeepAlive 组合下
             组件经缓存激活时 vnode type 相同，Transition 识别不到「子级更换」会静默跳过动画。 -->
        <router-view v-slot="{ Component, route }">
          <transition name="page-fade" mode="out-in">
            <keep-alive :include="cachedViews">
              <component :is="Component" :key="route.path" />
            </keep-alive>
          </transition>
        </router-view>
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, provide, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get } from '../api'
import { useAuth } from '../store/auth'
import { useTheme, type ThemeMode } from '../store/theme'
import { usePluginMenu } from '../store/pluginMenu'
import { useNotify } from '../store/notify'
import SideMenu from '../components/SideMenu.vue'
import NotifyBell from '../components/NotifyBell.vue'
import { MenuOutlined, SkinOutlined, UserOutlined } from '@ant-design/icons-vue'

const route = useRoute()
const router = useRouter()
const auth = useAuth()
const ui = useTheme()
const menus = usePluginMenu()
const collapsed = ref(false)
const drawerOpen = ref(false)
const version = ref('')
// 仪表盘等页面共享同一份版本号（避免各页面重复请求 /api/update/current）
provide('panelVersion', version)
const pluginMenus = computed(() => menus.items)
const isMobile = ref(false)

// keep-alive 白名单：组件名需与页面文件名一致（SFC 默认继承文件名，但 vite 插件已显式注入 name）
const cachedViews = [
  'AppsPage', 'Dashboard', 'NginxConf', 'TerminalPage', 'HostsPage', 'CertsPage',
  'MapPage', 'MonitorPage', 'McpPage', 'PluginsPage', 'SettingsPage', 'WafPage',
]

const mq = window.matchMedia('(max-width: 767px)')
const syncViewport = () => { isMobile.value = mq.matches; if (!mq.matches) drawerOpen.value = false }

const themeLabel = computed(() => ({ light: '浅色', dark: '深色', system: '跟随系统' })[ui.mode])

const selected = computed(() => {
  const p = route.path
  // WAF 只有一个菜单项：/waf 与旧地址 /waf/rules、/security 都归到它
  const hit = ['/dashboard', '/apps', '/nginx', '/hosts', '/terminal', '/certs', '/map', '/monitor', '/waf', '/security', '/mcp', '/plugins', '/settings']
    .find(k => p.startsWith(k))
  if (hit) return [hit]
  if (p.startsWith('/plugin/')) return ['/plugin/' + route.params.id]
  return []
})

const onMenu = ({ key }: { key: string }) => {
  router.push(key as string)
  drawerOpen.value = false
}
const onThemeChange = ({ key }: { key: string }) => ui.setMode(key as ThemeMode)

const doLogout = async () => {
  await auth.logout()
  router.push('/login')
}

const loadVersion = async () => {
  try { version.value = (await get<any>('/api/update/current')).version } catch { /* ignore */ }
}

onMounted(() => {
  syncViewport()
  mq.addEventListener('change', syncViewport)
  loadVersion()
  menus.load()
  useNotify().fetchUnread()
})
onUnmounted(() => mq.removeEventListener('change', syncViewport))
</script>

<style scoped>
/* 字标：18px/600 深灰（Top Navigation 规范） */
.logo { height: 56px; font-size: 18px; font-weight: 600; display: flex; align-items: center; justify-content: center; gap: 8px; }
.logo-mark { width: 26px; height: 26px; border-radius: 8px; }
/* 顶栏 64px：白底、底部 1px 浅灰描边、无阴影 */
.header { padding: 0 24px; display: flex; align-items: center; justify-content: space-between; height: 64px; line-height: 64px; }
.header-left { display: flex; align-items: center; gap: 4px; min-width: 0; }
.ver { font-size: 12px; white-space: nowrap; }
.user { cursor: pointer; padding: 0 8px; font-size: 14px; }
.menu-btn, .theme-btn { padding: 0 8px; }
/* 应用外壳锁定为一屏：document 永不滚动，顶栏/侧栏始终固定可见 */
.app-shell { height: 100vh; height: 100dvh; overflow: hidden; }
/* 右侧纵向列允许收缩，把剩余高度交给 .content */
.main-col { min-height: 0; min-width: 0; }
/* 内容区是唯一滚动容器：页面根节点撑满剩余视口（内容少时无「卡片下面悬一截背景」
   的空档）；内容超高时在本区域内部滚动，不再带动整页（overscroll-behavior 阻断滚动链）。 */
.content { margin: 24px; display: flex; flex-direction: column; min-height: 0; overflow-y: auto; overflow-x: hidden; overscroll-behavior: contain; scrollbar-gutter: stable; }
.content > :deep(*) { flex: 1 0 auto; }
/* 顶栏：浅色白底 + 1px 底描边（无阴影）；深色统一为面板深灰底 + 暗描边 */
html[data-theme='light'] .header { background: #fff !important; border-bottom: 1px solid #e5e7eb; box-shadow: none; color: #0f172a; }
html[data-theme='dark'] .header { background: #141414 !important; border-bottom: 1px solid #2b2b2b; box-shadow: none; color: #e6e6e6; }
html[data-theme='light'] .ver { color: #64748b; }
html[data-theme='dark'] .ver { color: #94a3b8; }
html[data-theme='light'] .logo { color: #0f172a; }
html[data-theme='dark'] .logo { color: #e6e6e6; }
html[data-theme='light'] .user { color: #334155; }
html[data-theme='dark'] .user { color: #e6e6e6; }
html[data-theme='light'] .theme-btn, html[data-theme='light'] .menu-btn { color: #334155; }
html[data-theme='dark'] .theme-btn, html[data-theme='dark'] .menu-btn { color: #e6e6e6; }
/* 侧栏：浅色白底 + 右侧 1px 描边；深色统一为面板深灰底（替代默认深蓝 #001529） */
.app-shell :deep(.ant-layout-sider) { border-right: 1px solid #e5e7eb; }
html[data-theme='dark'] .app-shell :deep(.ant-layout-sider) { background: #141414; border-right-color: #2b2b2b; }
/* 折叠按钮弱化：去掉 antd 默认的色块背景，平时隐入侧栏，hover 才略提亮 */
.app-shell :deep(.ant-layout-sider-trigger),
.app-shell :deep(.ant-layout-sider-trigger:hover) { background: transparent; }
html[data-theme='dark'] .app-shell :deep(.ant-layout-sider-trigger) { color: rgba(255,255,255,.35); }
html[data-theme='dark'] .app-shell :deep(.ant-layout-sider-trigger:hover) { color: rgba(255,255,255,.75); }
html[data-theme='light'] .app-shell :deep(.ant-layout-sider-trigger) { color: rgba(0,0,0,.35); }
html[data-theme='light'] .app-shell :deep(.ant-layout-sider-trigger:hover) { color: rgba(0,0,0,.75); }
@media (max-width: 767px) {
  .content { margin: 12px; }
  .header { padding: 0 8px; height: 56px; line-height: 56px; }
}
</style>

<style>
/* 路由切换过渡（必须是全局样式：动画对象是路由组件的根节点，携带的是子页面的
   scope 属性而非本组件的，scoped 规则永远匹配不上 → 动画时长为 0 直接完成）。
   out-in：旧页淡出 → 新页淡入上移，140ms 单程；系统开启「减少动态效果」时禁用。 */
.page-fade-enter-active,
.page-fade-leave-active {
  transition: opacity 0.14s ease, transform 0.14s ease;
  will-change: opacity, transform;
}
.page-fade-enter-from {
  opacity: 0;
  transform: translateY(10px);
}
.page-fade-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}
@media (prefers-reduced-motion: reduce) {
  .page-fade-enter-active,
  .page-fade-leave-active {
    transition: none;
  }
}
</style>
