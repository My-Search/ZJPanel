<template>
  <a-menu :theme="ui.isDark ? 'dark' : 'light'" mode="inline" :selected-keys="selected" @click="$emit('navigate', $event)">
    <a-menu-item key="/dashboard"><template #icon><AppstoreOutlined /></template>仪表盘</a-menu-item>
    <a-menu-item key="/apps"><template #icon><ClusterOutlined /></template>应用</a-menu-item>
    <a-menu-item key="/nginx"><template #icon><CodeOutlined /></template>Nginx</a-menu-item>
    <a-menu-item key="/terminal"><template #icon><TerminalIcon /></template>终端</a-menu-item>
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
</template>

<script setup lang="ts">
import { useTheme } from '../store/theme'
import TerminalIcon from './icons/TerminalIcon.vue'
import {
  AppstoreOutlined, ClusterOutlined, CodeOutlined,
  SafetyCertificateOutlined, EnvironmentOutlined, DashboardOutlined,
  ApiOutlined, AppstoreAddOutlined, SettingOutlined, ThunderboltOutlined,
} from '@ant-design/icons-vue'

defineProps<{ selected: string[]; pluginMenus: { pluginId: string; title: string; icon: string; path: string }[] }>()
defineEmits<{ (e: 'navigate', payload: { key: string }): void }>()
const ui = useTheme()
</script>
