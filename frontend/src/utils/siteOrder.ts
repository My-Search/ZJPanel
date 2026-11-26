/**
 * 站点排序（仪表盘「站点状态」与「应用 → 全部网站」共用，改一处两处生效）：
 * 维护中 > 异常(error/offline/检测中) > online，已停用/应用停用排最后；同层级保持原顺序（sort 稳定）。
 */
export const siteRank = (s: any) => {
  if (s.maintenance) return 0
  if (s.enabled === false || s.disabled) return 3
  if (s.check?.status === 'online') return 2
  return 1
}
export const sortSites = (sites: any[]) => [...sites].sort((a, b) => siteRank(a) - siteRank(b))
