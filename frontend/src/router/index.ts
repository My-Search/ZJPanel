import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '../store/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('../views/Login.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('../layout/MainLayout.vue'),
      children: [
        { path: '', redirect: '/apps' },
        { path: 'apps', component: () => import('../views/AppsPage.vue') },
        { path: 'sites', redirect: '/apps' },
        { path: 'compose', redirect: '/apps' },
        { path: 'docker', redirect: '/apps' },
        { path: 'dashboard', component: () => import('../views/Dashboard.vue') },
        { path: 'nginx', component: () => import('../views/NginxConf.vue') },
        { path: 'terminal', component: () => import('../views/TerminalPage.vue') },
        { path: 'certs', component: () => import('../views/CertsPage.vue') },
        { path: 'map', component: () => import('../views/MapPage.vue') },
        { path: 'monitor', component: () => import('../views/MonitorPage.vue') },
        { path: 'mcp', component: () => import('../views/McpPage.vue') },
        { path: 'plugins', component: () => import('../views/PluginsPage.vue') },
        { path: 'settings', component: () => import('../views/SettingsPage.vue') },
        { path: 'plugin/:id', component: () => import('../views/PluginFrame.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuth()
  if (to.meta.public) return true
  try {
    await auth.refresh()
  } catch {
    return { path: '/login' }
  }
  if (!auth.loggedIn) return { path: '/login' }
  return true
})

export default router
