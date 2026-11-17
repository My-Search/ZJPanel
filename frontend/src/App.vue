<template>
  <a-config-provider :theme="antdTheme" :locale="zhCN">
    <router-view />
  </a-config-provider>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { theme as antd } from 'ant-design-vue'
// antd 默认英文：表格排序提示（Click to sort ascending）、弹窗按钮（OK/Cancel）、空数据（No data）
// 全部要中文，与项目「中文优先」约定一致
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import { useTheme } from './store/theme'

const ui = useTheme()
// 企业简洁风：浅色单主蓝 #1e40af（链接/选中同源），深色提亮为 #3b82f6 保证对比度。
// 圆角：控件 8px（按钮/输入），卡片/弹窗 12px 由全局 CSS 与组件 token 补齐。
const antdTheme = computed(() => {
  const dark = ui.isDark
  return {
    algorithm: dark ? antd.darkAlgorithm : antd.defaultAlgorithm,
    token: {
      colorPrimary: dark ? '#3b82f6' : '#1e40af',
      colorInfo: dark ? '#3b82f6' : '#1e40af', // 信息/链接与主色同源：全站只用一种蓝
      colorLink: dark ? '#3b82f6' : '#1e40af',
      borderRadius: 8,
      colorBgLayout: dark ? '#141414' : '#f8fafc',
      colorBorderSecondary: dark ? '#333333' : '#e5e7eb',
      fontFamily:
        "'Inter', ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif",
    },
    components: {
      // Layout.Header 背景由组件 token colorBgHeader 控制（默认恒为深蓝 #001529），
      // 必须显式跟随主题，否则浅色模式下顶栏仍是深蓝、文字不可读。
      Layout: {
        colorBgHeader: dark ? '#141414' : '#ffffff',
        colorBgTrigger: dark ? '#1f1f1f' : '#f8fafc',
      },
      // 侧边菜单：选中 = 浅蓝底 + 企业蓝字（深色走 antd 暗色算法默认）
      // 深色必须覆盖 colorItemBg：antd-v4 深色菜单默认 #001529 藏蓝（旧命名 token），
      // 会与侧栏 #141414 拼成两截颜色；主题对象深浅两套整体切换，不会影响浅色菜单
      Menu: dark
        ? { itemBorderRadius: 8, colorItemBg: '#141414', menuSubMenuBg: '#1f1f1f' }
        : { itemSelectedBg: '#eff6ff', itemSelectedColor: '#1e40af', itemBorderRadius: 8, itemColor: '#334155' },
      Table: dark
        ? { headerBg: '#1d1d1d', headerColor: '#a3a3a3' }
        : { headerBg: '#f8fafc', headerColor: '#475569' },
    },
  }
})
</script>

<style>
html, body, #app { height: 100%; margin: 0; }
/* 深色优先兜底：CSS 加载完成前就不出现白底（夜间不闪眼）；
   首屏主题引导脚本与 store/theme.ts 会按用户选择同步覆盖。
   浅色画布 = Slate Mist #f8fafc（比纯白低一档，让白色卡片浮起来）。 */
html { background: #141414; color-scheme: dark; }
html[data-theme='light'] { background: #f8fafc; color-scheme: light; }
body { font-family: var(--font-sans); }
html[data-theme='light'] body { background: #f8fafc; color: #0f172a; }
html[data-theme='dark'] body { background: #141414; color: #e6e6e6; }

/* ---------- 卡片：白底 + 1px 描边 + 12px 圆角 + 两级浅阴影，不用重阴影 ---------- */
.ant-card { border-radius: var(--radius-cards); transition: box-shadow var(--motion-fast), transform var(--motion-fast); }
html[data-theme='light'] .ant-card { box-shadow: var(--shadow-rest); }
html[data-theme='light'] .ant-card:hover { box-shadow: var(--shadow-hover); transform: translateY(-2px); }
html[data-theme='dark'] .ant-card:hover { box-shadow: var(--shadow-hover); transform: translateY(-2px); }

/* ---------- 按钮：8px 圆角；主按钮 hover 深蓝填充+上浮，active 缩放给出按压反馈 ---------- */
.ant-btn { border-radius: var(--radius-buttons); transition: all var(--motion-fast); }
.ant-btn-primary:not(:disabled):not(.ant-btn-dangerous):hover {
  background: var(--color-corporate-blue-deep);
  transform: translateY(-2px);
  box-shadow: var(--shadow-hover);
}
/* 所有按钮必须有按压反馈，否则像装饰 */
.ant-btn:not(:disabled):active { transform: scale(0.98); }

/* ---------- 焦点环：2px Action Blue + 2px offset（无障碍底线，仅键盘触发避免与点击态叠加） ---------- */
.ant-btn:focus-visible,
button:focus-visible,
.ant-menu-item:focus-visible,
.ant-dropdown-menu-item:focus-visible,
a:focus-visible {
  outline: 2px solid var(--color-action-blue);
  outline-offset: 2px;
}

/* ---------- 状态标签：全圆角 ---------- */
.ant-tag { border-radius: 999px; }

/* 弹窗沿用卡片圆角 */
.ant-modal .ant-modal-content { border-radius: var(--radius-cards); }

/* ---------- 代码块：浅色用画布色 + 描边（MCP 快速接入 JSON、证书 PEM 查看） ---------- */
.code-block { background: #f8fafc; border: 1px solid #e5e7eb; padding: 10px; font-size: 12px; overflow: auto; border-radius: 8px; }
html[data-theme='dark'] .code-block { background: #1f1f1f; border-color: #333; color: #d9d9d9; }
html[data-theme='dark'] .mode-card.active,
html[data-theme='dark'] .provider-card.active,
html[data-theme='dark'] .proj-card.active { background: #16243f !important; border-color: #3b82f6; }
html[data-theme='dark'] .dir-item:hover { background: #16243f !important; }

/* 滚动条主题化：跟随深浅主题（表格纵向滚动、长列表、日志等所有滚动区域） */
::-webkit-scrollbar { width: 8px; height: 8px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb { background: #cbd5e1; border-radius: 4px; }
::-webkit-scrollbar-thumb:hover { background: #94a3b8; }
html[data-theme='dark'] ::-webkit-scrollbar-thumb { background: #4b4b4b; }
html[data-theme='dark'] ::-webkit-scrollbar-thumb:hover { background: #5f5f5f; }
::-webkit-scrollbar-corner { background: transparent; }

/* 移动端抽屉侧栏：容器与菜单同色（仅导航抽屉，避免影响其它抽屉） */
.nav-drawer .ant-drawer-content, .nav-drawer .ant-drawer-body { background: #141414; }
html[data-theme='light'] .nav-drawer .ant-drawer-content,
html[data-theme='light'] .nav-drawer .ant-drawer-body { background: #fff; }

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
  /* 统计卡右侧背景水印图标：窄屏卡片变高后缩小并垂直居中，避免压住文字 */
  .stat-icon { right: 6px !important; top: 50% !important; bottom: auto !important; transform: translateY(-50%) !important; font-size: 40px !important; }
  /* 窄屏卡片不做上浮动效（触屏无 hover 语义） */
  .ant-card:hover { transform: none; }
}
</style>
