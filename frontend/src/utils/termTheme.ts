import type { ITheme } from '@xterm/xterm'
import { useTheme } from '../store/theme'

/** 终端配色随深浅主题联动，供终端页与 Docker/应用 exec 弹窗复用。 */
export const useTermTheme = () => {
  const ui = useTheme()
  const themes: { dark: ITheme; light: ITheme } = {
    dark: { background: '#0d1117', foreground: '#c9d1d9' },
    light: { background: '#ffffff', foreground: '#24292f' },
  }
  const current = () => ui.isDark ? themes.dark : themes.light
  const boxBg = () => ui.isDark ? '#0d1117' : '#ffffff'
  return { ui, current, boxBg }
}
