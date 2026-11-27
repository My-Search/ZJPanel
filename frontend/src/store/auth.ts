import { defineStore } from 'pinia'
import { get, getQuiet, post } from '../api'

export const useAuth = defineStore('auth', {
  state: () => ({ initialized: false, loggedIn: false, username: '' as string, checked: false }),
  actions: {
    // quiet=true 时不弹错误提示：登录页做「是否已登录」探测，后端短暂不可用不应打断用户
    async refresh(quiet = false) {
      const r = quiet ? await getQuiet<any>('/api/auth/status') : await get<any>('/api/auth/status')
      this.initialized = r.initialized
      this.loggedIn = r.loggedIn
      this.username = r.username || ''
      this.checked = true
      return r
    },
    async login(username: string, password: string) {
      await post('/api/auth/login', { username, password })
      await this.refresh()
    },
    async init(username: string, password: string) {
      await post('/api/auth/init', { username, password })
      await this.refresh()
    },
    async logout() {
      await post('/api/auth/logout')
      this.loggedIn = false
      this.checked = false
    },
    async changePassword(oldP: string, newP: string) {
      await post('/api/auth/change-password', { old: oldP, new: newP })
    },
  },
})
