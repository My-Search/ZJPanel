import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '../store/auth'

// 路由 → 页面标题映射：浏览器标签栏显示当前页面名称，多标签时一眼区分
const TITLE_MAP: Record<string, string> = {
  '/dashboard': '仪表盘',
  '/apps': '应用',
  '/nginx': 'Nginx',
  '/terminal': '终端',
  '/hosts': '主机',
  '/certs': '证书',
  '/map': '请求地图',
  '/monitor': '监控',
  '/security': 'WAF',
  '/waf': 'WAF',
  '/mcp': 'MCP',
  '/plugins': '插件',
  '/settings': '设置',
  '/login': '登录',
}

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('../views/Login.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('../layout/MainLayout.vue'),
      children: [
        { path: '', redirect: '/dashboard' },
        { path: 'apps', component: () => import('../views/AppsPage.vue') },
        { path: 'sites', redirect: '/apps' },
        { path: 'compose', redirect: '/apps' },
        { path: 'docker', redirect: '/apps' },
        { path: 'dashboard', component: () => import('../views/Dashboard.vue') },
        { path: 'nginx', component: () => import('../views/NginxConf.vue') },
        { path: 'terminal', component: () => import('../views/TerminalPage.vue') },
        { path: 'hosts', component: () => import('../views/HostsPage.vue') },
        { path: 'certs', component: () => import('../views/CertsPage.vue') },
        { path: 'map', component: () => import('../views/MapPage.vue') },
        { path: 'monitor', component: () => import('../views/MonitorPage.vue') },
        { path: 'security', redirect: '/waf' },
        { path: 'waf', component: () => import('../views/SecurityPage.vue') },
        { path: 'mcp', component: () => import('../views/McpPage.vue') },
        { path: 'plugins', component: () => import('../views/PluginsPage.vue') },
        { path: 'settings', component: () => import('../views/SettingsPage.vue') },
        { path: 'plugin/:id', component: () => import('../views/PluginFrame.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.afterEach((to) => {
  const base = 'ZJPanel'
  const matched = Object.entries(TITLE_MAP).find(([prefix]) => to.path.startsWith(prefix))
  // 插件页：标题取路由参数 id
  const title = to.path.startsWith('/plugin/')
    ? `插件 · ${to.params.id}`
    : matched?.[1]
  document.title = title ? `${title} - ${base}` : base
})

router.beforeEach(async (to) => {
  const auth = useAuth()
  if (to.meta.public) return true
  // 首次进入面板：同步校验登录态（必须等结果，防止未登录进入后台页面）
  if (!auth.checked) {
    try {
      await auth.refresh()
    } catch {
      return { path: '/login' }
    }
  }
  if (!auth.loggedIn) return { path: '/login' }
  // 已校验过登录态：乐观放行立即切换页面，后台异步复核（会话失效由 401 拦截器统一跳登录页）
  if (!to.path.startsWith('/login')) {
    auth.refresh().catch(() => { /* 401 已由拦截器统一处理 */ })
  }
  return true
})

export default router
