import type { ReactNode } from 'react'
import {
  ApiOutlined, CheckOutlined, CheckCircleOutlined,
  CloseCircleOutlined, ClockCircleOutlined, IssuesCloseOutlined,
  MessageOutlined, NotificationOutlined, RetweetOutlined,
  RobotOutlined, ThunderboltOutlined,
} from '@ant-design/icons'
import { createElement } from 'react'
import type { RepeatRung } from './types'

// ---------------------------------------------------------------------------
// timelineActionMeta — central translation table for the IncidentTimeline.action
// discriminator so the lifecycle v2 events (auto_resolved, reopened) and the
// legacy ones (created/acked/...) all render with consistent Chinese labels +
// icons + Timeline dot colors.
// ---------------------------------------------------------------------------
export function timelineActionMeta(
  action: string,
): { label: string; color: string; icon: ReactNode } {
  switch (action) {
    case 'created':
      return { label: '事件创建', color: 'blue', icon: createElement(ApiOutlined) }
    case 'acked':
      return { label: '已确认', color: 'cyan', icon: createElement(CheckOutlined) }
    case 'assigned':
      return { label: '已分派', color: 'cyan', icon: createElement(CheckOutlined) }
    case 'commented':
      return { label: '评论', color: 'gray', icon: createElement(MessageOutlined) }
    case 'resolved':
      return { label: '已解决', color: 'green', icon: createElement(CheckCircleOutlined) }
    case 'auto_resolved':
      return { label: '自动恢复', color: 'green', icon: createElement(IssuesCloseOutlined) }
    case 'reopened':
      return { label: '复活 (Reopened)', color: 'orange', icon: createElement(RetweetOutlined) }
    case 'closed':
      return { label: '已关闭', color: 'gray', icon: createElement(CloseCircleOutlined) }
    case 'escalated':
      return { label: '严重级升级', color: 'red', icon: createElement(ThunderboltOutlined) }
    case 'ai_triggered':
      return { label: 'AI 分析触发', color: 'purple', icon: createElement(RobotOutlined) }
    case 'ai_notified':
      return { label: 'AI 结论已发送', color: 'purple', icon: createElement(NotificationOutlined) }
    default:
      return { label: action || '未知动作', color: 'gray', icon: createElement(ClockCircleOutlined) }
  }
}

// ---------------------------------------------------------------------------
// currentRepeatRung — local mirror of internal/incident/service.go's
// pickRepeatRung over the default schedule. Only used for a decorative tag
// in the header; if operators tune the schedule the order-of-magnitude
// semantics still hold so we don't fetch the live schedule.
// ---------------------------------------------------------------------------
export function currentRepeatRung(openedAt: string): RepeatRung {
  const elapsedMs = Date.now() - new Date(openedAt).getTime()
  if (elapsedMs >= 24 * 3600 * 1000) {
    return { label: '24h+ · [REPEAT] / 6h', key: 'repeat-low' }
  }
  if (elapsedMs >= 3 * 3600 * 1000) {
    return { label: '3h+ · [ATTENTION] / 2h', key: 'attention' }
  }
  return { label: '0-3h · [REPEAT] / 30m', key: 'normal' }
}

export function repeatRungColor(rung: RepeatRung): string {
  switch (rung.key) {
    case 'attention':
      return 'red'
    case 'repeat-low':
      return 'gold'
    default:
      return 'blue'
  }
}

// estimateWords gives a rough "字数" count for mixed CN/EN reports. We
// count CJK code points individually and word-split the rest.
export function estimateWords(s: string): number {
  let cjk = 0
  let other = ''
  for (const ch of s) {
    if (/[\u4e00-\u9fff\u3040-\u30ff\uac00-\ud7af]/.test(ch)) cjk++
    else other += ch
  }
  const words = other.trim().split(/\s+/).filter(Boolean).length
  return cjk + words
}

// truncateInline shortens a single label value without splitting code points.
export function truncateInline(s: string, max: number): string {
  if (!s) return ''
  const arr = Array.from(s)
  if (arr.length <= max) return s
  return arr.slice(0, max).join('') + '…'
}

// base64ToUtf8 decodes a base64 string into a UTF-8 string.
// atob() alone corrupts multi-byte characters (e.g. Chinese) because it
// treats each byte as a Latin-1 character; we must reassemble the bytes
// and decode them as UTF-8.
function base64ToUtf8(base64: string): string {
  const binary = atob(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i)
  }
  return new TextDecoder().decode(bytes)
}

// decodeRawPayload turns the on-wire raw_payload (base64-encoded []byte from
// the Go model) into a pretty-printed JSON string when possible. Returns
// undefined when the field is absent so callers can simply skip the
// raw_payload section, and returns the raw string when decoding/parsing
// fails so we never silently drop the diagnostic data.
export function decodeRawPayload(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined
  let decoded = raw
  try {
    decoded = base64ToUtf8(raw)
  } catch {
    // Not base64 — keep the original (some adapters may send a raw JSON
    // string in the future, and we don't want to swallow that).
  }
  try {
    return JSON.stringify(JSON.parse(decoded), null, 2)
  } catch {
    return decoded
  }
}

export const AI_STATUS_MAP: Record<string, { color: string; text: string }> = {
  pending: { color: '#8c8c8c', text: '未分析' },
  running: { color: '#1677ff', text: 'AI 分析中...' },
  done: { color: '#52c41a', text: '分析完成' },
  failed: { color: '#f5222d', text: '分析失败' },
  disabled: { color: '#8c8c8c', text: 'AI 分析未启用' },
}
