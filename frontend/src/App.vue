<template>
  <a-config-provider :theme="antdTheme">
    <router-view />
  </a-config-provider>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { theme as antd } from 'ant-design-vue'
import { useTheme } from './store/theme'

const ui = useTheme()
const antdTheme = computed(() => ({
  algorithm: ui.isDark ? antd.darkAlgorithm : antd.defaultAlgorithm,
  token: { colorPrimary: '#1677ff', borderRadius: 6 },
}))
</script>

<style>
html, body, #app { height: 100%; margin: 0; }
body { font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
html[data-theme='light'] body { background: #f5f7fa; color: #1f1f1f; }
html[data-theme='dark'] body { background: #141414; color: #e6e6e6; }
/* 窄屏：表格横向滚动而非撑破布局；弹窗/抽屉宽度不超过视口 */
@media (max-width: 767px) {
  .ant-table-body, .ant-table-content { overflow-x: auto; }
  .ant-modal, .ant-modal-content, .ant-drawer-content-wrapper { max-width: 96vw !important; }
  .ant-modal-body { overflow-x: auto; }
  /* 表单控件内联固定宽度在窄屏收缩，避免撑出横向滚动条 */
  .ant-select, .ant-input, .ant-input-number, .ant-picker { max-width: 100%; }
  .login-card { max-width: 92vw; }
}
</style>
