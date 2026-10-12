import type { MessageCategory } from '../../messageClassifier'
import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ClassificationInput } from '../capabilities'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { MUSE_ITEM_KIND, MUSE_METHOD } from '~/generated/contracts/muse-protocol'
import { notificationClassifierFor } from '../../notificationClassification'
import { museNotificationEntry } from './extractors/notification'
import { museItemLifecycle } from './extractors/toolCommon'
import { museItem, museItemText } from './protocol'

export function classifyMuseMessage(input: ClassificationInput): MessageCategory {
  const payload = input.parentObject
  if (input.wrapper)
    return notificationClassifierFor(input.agentProvider, museNotificationEntry)(input.wrapper.messages, 'hidden')
  if (!payload)
    return { kind: 'unknown' }
  if (!payload.method && typeof payload.content === 'string')
    return payload.hidden === true ? { kind: 'hidden' } : payload.planExecution === true ? { kind: 'plan_execution' } : { kind: 'user_content' }
  if (payload.method === MUSE_METHOD.TurnCompleted)
    return { kind: 'result_divider' }
  if (payload.method === MUSE_METHOD.TodoListChanged)
    return notificationClassifierFor(input.agentProvider, museNotificationEntry)([payload], 'hidden')
  const item = museItem(payload)
  if (!item)
    return museNotificationEntry(payload).length ? notificationClassifierFor(input.agentProvider, museNotificationEntry)([payload]) : { kind: 'unknown' }
  switch (item.kind) {
    case MUSE_ITEM_KIND.AgentMessage:
      return museItemText(item) ? { kind: 'assistant_text' } : { kind: 'hidden' }
    case MUSE_ITEM_KIND.Reasoning:
      return museItemText(item) ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    case MUSE_ITEM_KIND.UserMessage:
      return { kind: 'user_content' }
    case MUSE_ITEM_KIND.ToolCall:
    case MUSE_ITEM_KIND.UserShell:
      return museItemLifecycle(item.status, input.completion).facts.rowFinal ? { kind: 'tool_result' } : { kind: 'tool_use' }
    case MUSE_ITEM_KIND.Workflow:
      return museItemLifecycle(item.status, input.completion).facts.rowFinal ? { kind: 'tool_result' } : { kind: 'hidden' }
    case MUSE_ITEM_KIND.Compaction:
      return notificationClassifierFor(input.agentProvider, museNotificationEntry)([payload], 'hidden')
    case MUSE_ITEM_KIND.Subagent:
    case MUSE_ITEM_KIND.ReminderChild:
      return { kind: 'hidden' }
    case MUSE_ITEM_KIND.HookRun:
      return { kind: 'unknown' }
    default:
      return { kind: 'unknown' }
  }
}

export function museSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const item = museItem(parsed.parentObject)
  if (!item || (item.kind !== MUSE_ITEM_KIND.ToolCall && item.kind !== MUSE_ITEM_KIND.UserShell))
    return 'other'
  return museItemLifecycle(item.status, parsed.completion).facts.rowFinal ? 'result' : 'request'
}
