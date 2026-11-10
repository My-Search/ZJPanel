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
  // Layout.Header 背景由组件 token colorBgHeader 控制（默认恒为深蓝 #001529），
  // 必须显式跟随主题，否则浅色模式下顶栏仍是深蓝、文字不可读。
  token: { colorPrimary: '#1677ff', borderRadius: 6 },
  components: {
    Layout: { colorBgHeader: ui.isDark ? '#001529' : '#ffffff', colorBgTrigger: ui.isDark ? '#002140' : '#f0f2f5' },
  },
}))
</script>

<style>
html, body, #app { height: 100%; margin: 0; }
body { font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
html[data-theme='light'] body { background: #f5f7fa; color: #1f1f1f; }
html[data-theme='dark'] body { background: #141414; color: #e6e6e6; }
/* 深色模式兜底：历史页面中写死的浅色内联/局部背景，统一压暗 */
html[data-theme='dark'] pre[style*="#f6f6f6"],
html[data-theme='dark'] pre[style*="background: #f6f6f6"] { background: #1f1f1f !important; color: #d9d9d9 !important; }
html[data-theme='dark'] .mode-card.active,
html[data-theme='dark'] .provider-card.active,
html[data-theme='dark'] .proj-card.active { background: #16243f !important; }
html[data-theme='dark'] .dir-item:hover { background: #16243f !important; }
/* 滚动条主题化：跟随深浅主题（表格纵向滚动、长列表、日志等所有滚动区域） */
::-webkit-scrollbar { width: 8px; height: 8px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb { background: #c4ccd6; border-radius: 4px; }
::-webkit-scrollbar-thumb:hover { background: #a9b3c0; }
html[data-theme='dark'] ::-webkit-scrollbar-thumb { background: #4b4b4b; }
html[data-theme='dark'] ::-webkit-scrollbar-thumb:hover { background: #5f5f5f; }
::-webkit-scrollbar-corner { background: transparent; }
/* 企业级统一设计语言：卡片阴影/表格头（深浅主题各自适配） */
.ant-card { border-radius: 10px; }
html[data-theme='light'] .ant-card { box-shadow: 0 1px 3px rgba(15, 23, 42, .06), 0 1px 2px rgba(15, 23, 42, .04); }
html[data-theme='light'] .ant-table-thead > tr > th { background: #f8fafc; color: #475569; font-weight: 600; }
html[data-theme='dark'] .ant-table-thead > tr > th { background: #1d1d1d; color: #a3a3a3; font-weight: 600; }
.ant-btn { border-radius: 7px; }
.ant-modal .ant-modal-content { border-radius: 12px; }
/* 窄屏：表格横向滚动而非撑破布局；弹窗/抽屉宽度不超过视口 */
@media (max-width: 767px) {
  .ant-table-body, .ant-table-content { overflow-x: auto; }
  .ant-modal, .ant-modal-content, .ant-drawer-content-wrapper { max-width: 96vw !important; }
  .ant-modal-body { overflow-x: auto; }
  /* 表单控件内联固定宽度在窄屏收缩，避免撑出横向滚动条 */
  .ant-select, .ant-input, .ant-input-number, .ant-picker { max-width: 100%; }
  .login-card { max-width: 92vw; }
  /* 工具栏（按钮 + 长说明）在窄屏换行，避免 flex 子项把页面撑出水平滚动条 */
  .toolbar { flex-wrap: wrap; }
  .toolbar > * { max-width: 100%; }
  .toolbar .dim { min-width: 0; overflow-wrap: anywhere; word-break: break-word; }
  /* 栅格列窄屏全宽堆叠（页面向下排布，不出横向滚动） */
  .ant-row { row-gap: 10px; }
  .ant-col { max-width: 100% !important; }
  .ant-col[class*='ant-col-']:not(.ant-col-24) { flex: 0 0 100% !important; width: 100% !important; }
  /* 描述列表、标签组换行防溢出 */
  .ant-descriptions-item { padding-bottom: 8px; }
  .ant-space { flex-wrap: wrap; }
  .ant-statistic-title { font-size: 12px; }
  .ant-statistic-content { font-size: 16px; }
}
</style>
