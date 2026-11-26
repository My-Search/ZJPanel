<template>
  <a-menu :theme="ui.isDark ? 'dark' : 'light'" mode="inline" :selected-keys="selected" @click="$emit('navigate', $event)">
    <a-menu-item key="/dashboard"><template #icon><AppstoreOutlined /></template>仪表盘</a-menu-item>
    <a-menu-item key="/apps"><template #icon><ClusterOutlined /></template>应用</a-menu-item>
    <a-menu-item key="/nginx"><template #icon><CodeOutlined /></template>Nginx</a-menu-item>
    <a-menu-item key="/hosts"><template #icon><CloudServerOutlined /></template>主机</a-menu-item>
    <a-menu-item key="/certs"><template #icon><SafetyCertificateOutlined /></template>证书</a-menu-item>
    <a-menu-item key="/monitor"><template #icon><DashboardOutlined /></template>监控</a-menu-item>
    <a-menu-item key="/waf"><template #icon><SafetyOutlined /></template>WAF</a-menu-item>
    <a-menu-item key="/mcp"><template #icon><ApiOutlined /></template>MCP</a-menu-item>
    <a-menu-item key="/plugins"><template #icon><AppstoreAddOutlined /></template>插件</a-menu-item>
    <a-menu-item key="/settings"><template #icon><SettingOutlined /></template>设置</a-menu-item>
    <a-menu-divider v-if="pluginMenus.length" />
    <a-menu-item v-for="pm in pluginMenus" :key="'/plugin/' + pm.pluginId" :title="pm.title">
      <template #icon><component :is="pluginIcon(pm.icon)" /></template>{{ pm.title }}
    </a-menu-item>
  </a-menu>
</template>

<script setup lang="ts">
import { useTheme } from '../store/theme'
import {
  AppstoreOutlined, ClusterOutlined, CodeOutlined,
  SafetyCertificateOutlined, DashboardOutlined, SafetyOutlined,
  ApiOutlined, AppstoreAddOutlined, SettingOutlined, CloudServerOutlined,
  // 插件菜单图标池（icon 名来自插件 manifest 的 menu.icon，kebab-case）
  ThunderboltOutlined, DatabaseOutlined, ClockCircleOutlined,
  CloudOutlined, FileTextOutlined, FolderOutlined, KeyOutlined,
  LinkOutlined, MailOutlined, BellOutlined, ToolOutlined,
  GlobalOutlined, HddOutlined, RocketOutlined, SearchOutlined,
  BugOutlined, BookOutlined, TableOutlined, UserOutlined,
  TeamOutlined, PushpinOutlined, AlertOutlined, ClearOutlined,
} from '@ant-design/icons-vue'
import type { FunctionalComponent } from 'vue'

defineProps<{ selected: string[]; pluginMenus: { pluginId: string; title: string; icon: string; path: string }[] }>()
defineEmits<{ (e: 'navigate', payload: { key: string }): void }>()
const ui = useTheme()

// icon 名 → 组件映射；未收录的名字回落闪电（内置插件实际使用：clock-circle / database / thunderbolt）
const PLUGIN_ICONS: Record<string, FunctionalComponent> = {
  'thunderbolt': ThunderboltOutlined,
  'database': DatabaseOutlined,
  'clock-circle': ClockCircleOutlined,
  'clock': ClockCircleOutlined,
  'cloud': CloudOutlined,
  'cloud-server': CloudOutlined,
  'file': FileTextOutlined,
  'file-text': FileTextOutlined,
  'folder': FolderOutlined,
  'key': KeyOutlined,
  'link': LinkOutlined,
  'mail': MailOutlined,
  'bell': BellOutlined,
  'tool': ToolOutlined,
  'global': GlobalOutlined,
  'hdd': HddOutlined,
  'rocket': RocketOutlined,
  'search': SearchOutlined,
  'bug': BugOutlined,
  'book': BookOutlined,
  'table': TableOutlined,
  'user': UserOutlined,
  'team': TeamOutlined,
  'pushpin': PushpinOutlined,
  'alert': AlertOutlined,
  'clear': ClearOutlined,
}
const pluginIcon = (name?: string) => PLUGIN_ICONS[(name || '').trim().toLowerCase()] || ThunderboltOutlined
</script>
