<template>
  <a-layout style="min-height: 100vh">
    <!-- 桌面：固定侧栏；移动：抽屉 -->
    <a-layout-sider
      v-if="!isMobile"
      v-model:collapsed="collapsed"
      collapsible
      :theme="ui.isDark ? 'dark' : 'light'"
      :width="220"
    >
      <div class="logo">🚀 {{ collapsed ? '' : 'ZJPanel' }}</div>
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
      <div class="logo">🚀 ZJPanel</div>
      <SideMenu :selected="selected" :plugin-menus="pluginMenus" @navigate="onMenu" />
    </a-drawer>

    <a-layout>
      <a-layout-header class="header">
        <div class="header-left">
          <a-button v-if="isMobile" type="text" class="menu-btn" @click="drawerOpen = true">
            <MenuOutlined />
          </a-button>
          <span class="ver">ZJPanel {{ version }}</span>
        </div>
        <a-space :size="4">
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
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get } from '../api'
import { useAuth } from '../store/auth'
import { useTheme, type ThemeMode } from '../store/theme'
import { usePluginMenu } from '../store/pluginMenu'
import SideMenu from '../components/SideMenu.vue'
import { MenuOutlined, SkinOutlined, UserOutlined } from '@ant-design/icons-vue'

const route = useRoute()
const router = useRouter()
const auth = useAuth()
const ui = useTheme()
const menus = usePluginMenu()
const collapsed = ref(false)
const drawerOpen = ref(false)
const version = ref('')
const pluginMenus = computed(() => menus.items)
const isMobile = ref(false)

const mq = window.matchMedia('(max-width: 767px)')
const syncViewport = () => { isMobile.value = mq.matches; if (!mq.matches) drawerOpen.value = false }

const themeLabel = computed(() => ({ light: '浅色', dark: '深色', system: '跟随系统' })[ui.mode])

const selected = computed(() => {
  const p = route.path
  const hit = ['/dashboard', '/apps', '/nginx', '/terminal', '/certs', '/map', '/monitor', '/mcp', '/plugins', '/settings']
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
})
onUnmounted(() => mq.removeEventListener('change', syncViewport))
</script>

<style scoped>
.logo { height: 56px; font-size: 18px; font-weight: 700; display: flex; align-items: center; justify-content: center; letter-spacing: 1px; }
.header { padding: 0 16px; display: flex; align-items: center; justify-content: space-between; height: 48px; line-height: 48px; }
.header-left { display: flex; align-items: center; gap: 4px; min-width: 0; }
.ver { font-size: 12px; opacity: .65; white-space: nowrap; }
.user { cursor: pointer; padding: 0 8px; }
.menu-btn, .theme-btn { padding: 0 8px; }
.content { margin: 16px; }
:global(html[data-theme='light']) .header { background: #fff !important; box-shadow: 0 1px 4px rgba(0,0,0,.08); color: #1f1f1f; }
:global(html[data-theme='dark']) .header { box-shadow: 0 1px 4px rgba(0,0,0,.4); color: #e6e6e6; }
:global(html[data-theme='light']) .ver { color: #1f1f1f; }
:global(html[data-theme='dark']) .ver { color: #e6e6e6; }
:global(html[data-theme='light']) .logo { color: #1f1f1f; }
:global(html[data-theme='dark']) .logo { color: #fff; }
:global(html[data-theme='light']) .user { color: #444; }
:global(html[data-theme='dark']) .user { color: #e6e6e6; }
:global(html[data-theme='light']) .theme-btn, :global(html[data-theme='light']) .menu-btn { color: #444; }
:global(html[data-theme='dark']) .theme-btn, :global(html[data-theme='dark']) .menu-btn { color: #e6e6e6; }
@media (max-width: 767px) {
  .content { margin: 8px; }
  .header { padding: 0 8px; }
}
</style>
