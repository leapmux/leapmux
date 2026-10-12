import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../capabilities'
import { DEEPSEEK_HARNESS_CONTENT_TYPE, DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { deepseekHarnessNotificationEntry } from './extractors/notification'
import { deepseekHarnessAssistantBlock, deepseekHarnessCallId, deepseekHarnessEventData } from './protocol'

const NOTIFICATION_EVENTS: ReadonlySet<string> = new Set([DEEPSEEK_HARNESS_EVENT.CompactionEnd])
const HIDDEN_EVENTS: ReadonlySet<string> = new Set([
  DEEPSEEK_HARNESS_EVENT.TurnStart,
  DEEPSEEK_HARNESS_EVENT.AssistantAttempt,
  DEEPSEEK_HARNESS_EVENT.RequestContext,
  DEEPSEEK_HARNESS_EVENT.WorkflowStart,
  DEEPSEEK_HARNESS_EVENT.WorkflowEnd,
  DEEPSEEK_HARNESS_EVENT.WorkflowAgentStart,
  DEEPSEEK_HARNESS_EVENT.WorkflowAgentEnd,
  DEEPSEEK_HARNESS_EVENT.GoalChange,
  DEEPSEEK_HARNESS_EVENT.PlanMode,
  DEEPSEEK_HARNESS_EVENT.SubagentCatalog,
])

export function classifyDeepseekHarnessMessage(input: ClassificationInput): MessageCategory {
  const notification = notificationClassifierFor(input.agentProvider, deepseekHarnessNotificationEntry)
  if (input.wrapper?.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(input.wrapper, NOTIFICATION_EVENTS))
    return notification(input.wrapper.messages, 'hidden')
  const payload = input.parentObject
  if (!payload)
    return { kind: 'unknown' }
  if (!payload.type && typeof payload.content === 'string')
    return payload.hidden === true ? { kind: 'hidden' } : payload.planExecution === true ? { kind: 'plan_execution' } : { kind: 'user_content' }
  const type = pickString(payload, 'type')
  if (type === DEEPSEEK_HARNESS_EVENT.TurnEnd)
    return { kind: 'result_divider' }
  if (type === DEEPSEEK_HARNESS_EVENT.ToolCall && deepseekHarnessCallId(payload))
    return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
  if (type === DEEPSEEK_HARNESS_EVENT.ToolResult && deepseekHarnessCallId(payload))
    return { kind: 'tool_result' }
  if (type === DEEPSEEK_HARNESS_EVENT.AssistantMessage) {
    const block = deepseekHarnessAssistantBlock(payload)
    if (!block)
      return { kind: 'unknown' }
    if (block.type === DEEPSEEK_HARNESS_CONTENT_TYPE.Reasoning)
      return pickString(block, 'text') ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    if (block.type === DEEPSEEK_HARNESS_CONTENT_TYPE.Text)
      return pickString(block, 'text') ? { kind: 'assistant_text' } : { kind: 'hidden' }
    return { kind: 'hidden' }
  }
  if (type === DEEPSEEK_HARNESS_EVENT.UserMessage && deepseekHarnessEventData(payload))
    return { kind: 'user_content' }
  if (type === DEEPSEEK_HARNESS_EVENT.TodoWrite)
    return { kind: 'hidden' }
  if (NOTIFICATION_EVENTS.has(type))
    return notification([payload], 'hidden')
  return HIDDEN_EVENTS.has(type) ? { kind: 'hidden' } : { kind: 'unknown' }
}
