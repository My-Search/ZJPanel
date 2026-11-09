<template>
  <a-layout style="min-height: 100vh">
    <a-layout-sider v-model:collapsed="collapsed" collapsible theme="dark" :width="220">
      <div class="logo">🚀 {{ collapsed ? '' : 'ZJPanel' }}</div>
      <a-menu theme="dark" mode="inline" :selected-keys="selected" @click="onMenu">
        <a-menu-item key="/dashboard"><template #icon><AppstoreOutlined /></template>仪表盘</a-menu-item>
        <a-menu-item key="/apps"><template #icon><ClusterOutlined /></template>应用</a-menu-item>
        <a-menu-item key="/nginx"><template #icon><CodeOutlined /></template>Nginx</a-menu-item>
        <a-menu-item key="/terminal"><template #icon><CodeSandboxOutlined /></template>终端</a-menu-item>
        <a-menu-item key="/certs"><template #icon><SafetyCertificateOutlined /></template>证书</a-menu-item>
        <a-menu-item key="/map"><template #icon><EnvironmentOutlined /></template>请求地图</a-menu-item>
        <a-menu-item key="/monitor"><template #icon><DashboardOutlined /></template>监控</a-menu-item>
        <a-menu-item key="/mcp"><template #icon><ApiOutlined /></template>MCP</a-menu-item>
        <a-menu-item key="/plugins"><template #icon><AppstoreAddOutlined /></template>插件</a-menu-item>
        <a-menu-item key="/settings"><template #icon><SettingOutlined /></template>设置</a-menu-item>
        <a-menu-divider v-if="pluginMenus.length" />
        <a-menu-item v-for="pm in pluginMenus" :key="'/plugin/' + pm.pluginId" :title="pm.title">
          <template #icon><ThunderboltOutlined /></template>{{ pm.title }}
        </a-menu-item>
      </a-menu>
    </a-layout-sider>
    <a-layout>
      <a-layout-header class="header">
        <span class="ver">ZJPanel {{ version }}</span>
        <a-dropdown>
          <span class="user"><UserOutlined /> {{ auth.username || 'admin' }}</span>
          <template #overlay>
            <a-menu>
              <a-menu-item key="logout" @click="doLogout">退出登录</a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </a-layout-header>
      <a-layout-content class="content">
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get } from '../api'
import { useAuth } from '../store/auth'
import {
  AppstoreOutlined, ClusterOutlined, CodeOutlined,
  CodeSandboxOutlined, SafetyCertificateOutlined, EnvironmentOutlined, DashboardOutlined,
  ApiOutlined, AppstoreAddOutlined, SettingOutlined, UserOutlined, ThunderboltOutlined,
} from '@ant-design/icons-vue'

const route = useRoute()
const router = useRouter()
const auth = useAuth()
const collapsed = ref(false)
const version = ref('')
const pluginMenus = ref<{ pluginId: string; title: string; icon: string; path: string }[]>([])

const selected = computed(() => {
  const p = route.path
  const hit = ['/dashboard', '/apps', '/nginx', '/terminal', '/certs', '/map', '/monitor', '/mcp', '/plugins', '/settings']
    .find(k => p.startsWith(k))
  if (hit) return [hit]
  if (p.startsWith('/plugin/')) return ['/plugin/' + route.params.id]
  return []
})

const onMenu = ({ key }: { key: string }) => router.push(key as string)

const doLogout = async () => {
  await auth.logout()
  router.push('/login')
}

const loadVersion = async () => {
  try { version.value = (await get<any>('/api/update/current')).version } catch { /* ignore */ }
}
const loadMenus = async () => {
  try { pluginMenus.value = await get('/api/plugins/menu') } catch { /* ignore */ }
}

watch(() => route.path, loadMenus)
onMounted(() => { loadVersion(); loadMenus() })
</script>

<style scoped>
.logo { height: 56px; color: #fff; font-size: 18px; font-weight: 700; display: flex; align-items: center; justify-content: center; letter-spacing: 1px; }
.header { background: #fff; padding: 0 24px; display: flex; align-items: center; justify-content: space-between; box-shadow: 0 1px 4px rgba(0,0,0,.08); height: 48px; line-height: 48px; }
.ver { color: #999; font-size: 12px; }
.user { cursor: pointer; color: #444; }
.content { margin: 16px; }
</style>
