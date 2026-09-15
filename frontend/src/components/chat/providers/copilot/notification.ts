import type { NotificationThreadEntry } from '../registry'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../rendererUtils'
import { copilotEvent } from './protocol'

/** The sentence a subagent outcome reads as, with what the runtime measured. */
function copilotSubagentLine(data: Record<string, unknown>, failed: boolean): string {
  const name = pickString(data, 'agentDisplayName') || pickString(data, 'agentName') || 'A subagent'
  const head = data.cancelled === true
    ? `${name} stopped`
    : failed
      ? `${name} failed`
      : `${name} finished`
  const parts: string[] = []
  const error = pickString(data, 'error')
  if (failed && error)
    parts.push(error)
  for (const [label, key] of [['tool calls', 'totalToolCalls'], ['tokens', 'totalTokens']] as const) {
    const value = pickNumber(data, key, undefined)
    if (value !== undefined && Number.isSafeInteger(value) && value >= 0)
      parts.push(`${formatNumber(value)} ${label}`)
  }
  const duration = pickNumber(data, 'durationMs', undefined)
  if (duration !== undefined && Number.isSafeInteger(duration) && duration >= 0)
    parts.push(formatDuration(duration))
  return parts.length > 0 ? `${head} — ${parts.join(', ')}` : head
}

/** The question, the ask or the plan summary one control request states. */
function copilotControlLine(type: string, data: Record<string, unknown>): string | null {
  switch (type) {
    case COPILOT_EVENT.PermissionRequested: {
      const request = pickObject(data, 'permissionRequest')
      const kind = pickString(request, 'kind') || pickString(request, 'type')
      return kind ? `Asked to approve ${kind}` : 'Asked to approve a tool call'
    }
    case COPILOT_EVENT.UserInputRequested: {
      const question = pickString(data, 'question')
      return question ? `Asked: ${question}` : 'Asked a question'
    }
    case COPILOT_EVENT.ExitPlanModeRequested: {
      const summary = pickString(data, 'summary')
      return summary ? `Proposed a plan: ${summary}` : 'Proposed a plan'
    }
    case COPILOT_EVENT.ElicitationRequested: {
      const message = pickString(data, 'message')
      return message ? `Asked for input: ${message}` : 'Asked for input'
    }
    default:
      return null
  }
}

/**
 * A readable line for one Copilot notification row.
 *
 * Null for a shape Copilot does not own, so the shared provider-neutral notification
 * switch can try it instead, and for a row of Copilot's own that states nothing to
 * read -- which is hidden rather than shown as an empty notification.
 */
export function describeCopilotNotification(parsed: unknown): string | null {
  const event = copilotEvent(parsed)
  if (!event)
    return null
  const data = event.data
  switch (event.type) {
    case COPILOT_EVENT.SessionError:
    case COPILOT_EVENT.ModelCallFailure: {
      const message = pickString(data, 'message') || pickString(data, 'errorType')
      return message ? `Error: ${message}` : null
    }
    case COPILOT_EVENT.SessionWarning: {
      const message = pickString(data, 'message')
      return message ? `Warning: ${message}` : null
    }
    case COPILOT_EVENT.SessionInfo:
    case COPILOT_EVENT.SystemMessage:
    case COPILOT_EVENT.SystemNotification:
      return pickString(data, 'message') || null
    case COPILOT_EVENT.SessionCompactionStart:
      return 'Compacting the conversation'
    case COPILOT_EVENT.SessionCompactionComplete:
      return 'Conversation compacted'
    case COPILOT_EVENT.SessionContextCleared:
      return 'Context cleared'
    case COPILOT_EVENT.SessionTruncation: {
      const removed = pickNumber(data, 'messagesRemovedDuringTruncation', undefined)
      return removed !== undefined && removed > 0
        ? `Removed ${formatNumber(removed)} messages to fit the context window`
        : 'Removed older messages to fit the context window'
    }
    case COPILOT_EVENT.SkillInvoked: {
      const skill = pickString(data, 'skill') || pickString(data, 'name')
      return skill ? `Loaded the skill ${skill}` : null
    }
    case COPILOT_EVENT.SubagentCompleted:
      return copilotSubagentLine(data, false)
    case COPILOT_EVENT.SubagentFailed:
      return copilotSubagentLine(data, true)
    default:
      return copilotControlLine(event.type, data)
  }
}

/**
 * Copilot's one notification render seam, for a standalone row and for one entry of a
 * consolidated thread alike. Without it a multi-event thread would render only its
 * first message.
 */
export function copilotNotificationThreadEntry(msg: Record<string, unknown>): NotificationThreadEntry[] | null {
  const text = describeCopilotNotification(msg)
  return text === null ? null : [{ kind: 'text', text }]
}
