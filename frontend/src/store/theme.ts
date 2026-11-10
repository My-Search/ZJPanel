import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'zjpanel-theme'
const DARK_QUERY = '(prefers-color-scheme: dark)'

const isThemeMode = (v: string | null): v is ThemeMode =>
  v === 'light' || v === 'dark' || v === 'system'

/** 深浅主题：浅色 / 深色 / 跟随系统，选择持久化，系统主题变化自动跟随。 */
export const useTheme = defineStore('theme', () => {
  const saved = localStorage.getItem(STORAGE_KEY)
  const mode = ref<ThemeMode>(isThemeMode(saved) ? saved : 'system')
  const systemDark = ref(window.matchMedia(DARK_QUERY).matches)
  const isDark = ref(mode.value === 'dark' || (mode.value === 'system' && systemDark.value))

  const sync = () => {
    isDark.value = mode.value === 'dark' || (mode.value === 'system' && systemDark.value)
    document.documentElement.setAttribute('data-theme', isDark.value ? 'dark' : 'light')
  }

  const setMode = (m: ThemeMode) => {
    mode.value = m
    localStorage.setItem(STORAGE_KEY, m)
    sync()
  }

  const mq = window.matchMedia(DARK_QUERY)
  mq.addEventListener('change', e => { systemDark.value = e.matches; sync() })
  watch(mode, sync)
  sync()

  return { mode, isDark, setMode }
})
