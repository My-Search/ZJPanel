import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'zjpanel-theme'
const DARK_QUERY = '(prefers-color-scheme: dark)'

/** 深浅主题：浅色 / 深色 / 跟随系统，选择持久化，系统主题变化自动跟随。 */
export const useTheme = defineStore('theme', () => {
  // 默认深色优先（与 index.html 首屏引导一致）：夜间首次打开不闪白；用户显式选浅色才用浅色。
  // 旧版本存的 'system' 也按深色兜底，避免夜间仍跟随到浅色系统。
  const saved = localStorage.getItem(STORAGE_KEY)
  const mode = ref<ThemeMode>(saved === 'light' || saved === 'dark' || saved === 'system' ? saved : 'dark')
  const systemDark = ref(window.matchMedia(DARK_QUERY).matches)
  const isDark = ref(mode.value === 'dark' || (mode.value === 'system' && systemDark.value))

  const sync = () => {
    isDark.value = mode.value === 'dark' || (mode.value === 'system' && systemDark.value)
    const theme = isDark.value ? 'dark' : 'light'
    const el = document.documentElement
    el.setAttribute('data-theme', theme)
    el.style.colorScheme = theme // 原生控件/表单/滚动条一起跟随，避免局部白底
    el.style.backgroundColor = isDark.value ? '#141414' : '#f5f7fa' // 与 index.html 首屏引导一致
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', isDark.value ? '#141414' : '#ffffff') // 手机浏览器地址栏
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
