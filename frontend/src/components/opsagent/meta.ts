import type { OpsMessage } from '../../store/opsagent'

// 消息 meta/判定的共享工具（工作台与 opsagent 子组件共用）。
// meta 是 JSON 字符串：assistant→{toolCalls:[…],streaming?}；tool→{callId,pending?}；summary→{toId,count}

export function metaObj(m?: OpsMessage | null): Record<string, any> {
  if (!m?.meta) return {}
  try { return JSON.parse(m.meta) || {} } catch { return {} }
}

export interface ToolCallRef { id: string; name: string; arguments?: string }

export function toolCallsOf(m: OpsMessage): ToolCallRef[] {
  const v = metaObj(m).toolCalls
  return Array.isArray(v) ? v : []
}

/** 确认模式挂起中的工具行（等待人工放行）。 */
export function isPending(m: OpsMessage): boolean {
  return metaObj(m).pending === true
}

/** 流式进行中的助手行（后端 delta 帧持续追加，定稿后该标记消失）。 */
export function isStreaming(m: OpsMessage): boolean {
  return metaObj(m).streaming === true
}

/** 错误文本：meta error 标记或历史「⚠」前缀约定。 */
export function isErrText(m: OpsMessage): boolean {
  return metaObj(m).error === true || m.content.startsWith('⚠')
}

export function summaryCount(m: OpsMessage): number | string {
  const n = metaObj(m).count
  return typeof n === 'number' ? n : '?'
}

/** 参数单行摘要（空白折叠 + 截断），用于卡片头部。 */
export function fmtArgs(a: string | undefined, max = 72): string {
  if (!a) return ''
  const s = a.replace(/\s+/g, ' ').trim()
  return s.length > max ? s.slice(0, max) + '…' : s
}

/** 参数美化（JSON 则缩进两格，否则原样）。 */
export function prettyArgs(a?: string): string {
  if (!a) return ''
  try { return JSON.stringify(JSON.parse(a), null, 2) } catch { return a }
}

/** 工具展示名：面板工具名多为 list_sites 形式，直接展示；保持等宽字体渲染。 */
export const fmtTime = (t: string) => {
  const d = new Date(t)
  if (isNaN(d.getTime())) return ''
  return `${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
