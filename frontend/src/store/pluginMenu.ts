import { defineStore } from 'pinia'
import { get } from '../api'

/** 侧栏插件菜单：数据独立于路由，登录后拉取一次并缓存（安装/卸载/启停插件时手动刷新）。 */
export const usePluginMenu = defineStore('pluginMenu', {
  state: () => ({ items: [] as { pluginId: string; title: string; icon: string; path: string }[] }),
  actions: {
    async load(force = false) {
      if (!force && this.items.length) return
      try { this.items = await get('/api/plugins/menu') } catch { /* ignore */ }
    },
    clear() { this.items = [] },
  },
})
