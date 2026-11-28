import { computed } from 'vue'
import type { OpsMessage } from '../store/opsagent'
import { isErrText, isStreaming, toolCallsOf } from '../components/opsagent/meta'

// 会话时间线的回合渲染单元推导（对齐 ZCode conversationTurnRenderUnits 的思路，按消息序列纯推导）。
//
// 一个回合（turn）= 一条输入行（用户消息或事件横幅）+ 工作段（中间助手文本/工具调用）
// + 最终回答（最后一条无工具调用的助手行）。
// 压缩分隔条（summary）是全局标记，独立渲染并终止当前回合。

export interface OpsTurn {
  key: string
  /** 输入行：用户消息或事件横幅；事件横幅以 banner 形态渲染 */
  input: OpsMessage | null
  inputIsEvent: boolean
  /** 工作段：带工具调用的中间助手行 + 工具执行行（按时间序） */
  work: OpsMessage[]
  /** 最终回答：本回合最后一条无工具调用的助手行（可为 null = 未产出回答） */
  final: OpsMessage | null
  /** 回合是否进行中（仅最后一个回合可能为 true） */
  running: boolean
  /** 工作段是否失败（出现错误文本行） */
  failed: boolean
  /** 工作段耗时（首末工作行 createdAt 差，毫秒；无工作段为 0） */
  durationMs: number
}

export type TimelineUnit =
  | { type: 'summary'; msg: OpsMessage }
  | { type: 'turn'; turn: OpsTurn }

export interface TimelineProjection {
  units: TimelineUnit[]
  /** 是否应显示「正在思考」占位：会话运行中且尾部没有流式行兜底 */
  showThinking: boolean
}

const msgTs = (m: OpsMessage): number => {
  const t = new Date(m.createdAt).getTime()
  return isNaN(t) ? 0 : t
}

export function buildTimeline(messages: OpsMessage[], sessionStatus?: string): TimelineProjection {
  const units: TimelineUnit[] = []
  let cur: OpsTurn | null = null
  const pushCur = () => {
    if (cur && (cur.input || cur.work.length || cur.final)) units.push({ type: 'turn', turn: cur })
    cur = null
  }
  for (const m of messages) {
    if (m.role === 'summary') {
      pushCur()
      units.push({ type: 'summary', msg: m })
      continue
    }
    if (m.role === 'user' || m.role === 'event') {
      pushCur()
      cur = { key: `t${m.id}`, input: m, inputIsEvent: m.role === 'event', work: [], final: null, running: false, failed: false, durationMs: 0 }
      continue
    }
    if (!cur) {
      // 兜底：无输入行的工作行（历史数据/异常序列）也要可渲染
      cur = { key: `t${m.id}`, input: null, inputIsEvent: false, work: [], final: null, running: false, failed: false, durationMs: 0 }
    }
    if (m.role === 'tool') {
      cur.work.push(m)
    } else if (m.role === 'assistant') {
      if (toolCallsOf(m).length) {
        cur.work.push(m)
      } else if (cur.final && !isStreaming(cur.final)) {
        // 已有定稿后又出现新文本（如部分内容 + 失败说明）：前者归入工作段
        cur.work.push(cur.final)
        cur.final = m
      } else {
        cur.final = m
      }
      if (isErrText(m)) cur.failed = true
    }
  }
  pushCur()

  // 只有最后一个回合可能进行中；运行态取自会话状态
  const running = sessionStatus === 'running' || sessionStatus === 'waiting_confirm'
  if (units.length) {
    const last = units[units.length - 1]
    if (last.type === 'turn') last.turn.running = running
  }
  for (const u of units) {
    if (u.type !== 'turn') continue
    const w = u.turn.work
    u.turn.durationMs = w.length ? Math.max(0, msgTs(w[w.length - 1]) - msgTs(w[0])) : 0
  }

  // 「正在思考」占位：仅运行中且最后一条消息不是流式助手行（等待首 token / 工具执行间隙）；
  // waiting_confirm 由确认卡片主导，不重复提示
  const lastMsg = messages[messages.length - 1]
  const showThinking = sessionStatus === 'running' && !!lastMsg && lastMsg.role !== 'assistant' && lastMsg.role !== 'summary'

  return { units, showThinking }
}

/** 回合工作段的折叠判定：运行中 / 失败 / 无最终回答时默认展开，其余折叠。 */
export function turnWorkDefaultOpen(turn: OpsTurn): boolean {
  return turn.running || turn.failed || !turn.final
}

/** 「已工作 N 秒」文案。 */
export function fmtWorkDuration(ms: number): string {
  if (ms <= 0) return ''
  const s = Math.round(ms / 100) / 10
  return s >= 60 ? `${Math.floor(s / 60)} 分 ${Math.round(s % 60)} 秒` : `${s} 秒`
}

/** 消息列表 → 回合投影（组件直接用 computed 包一层即可）。 */
export function useConversationTurns(messages: () => OpsMessage[], sessionStatus: () => string | undefined) {
  return computed(() => buildTimeline(messages(), sessionStatus()))
}
