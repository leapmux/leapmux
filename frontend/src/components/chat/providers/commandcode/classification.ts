import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../capabilities'
import { COMMAND_CODE_EVENT, COMMAND_CODE_FRAME_KIND, COMMAND_CODE_METHOD, COMMAND_CODE_NOTIFICATION_EVENT } from '~/generated/contracts/commandcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { commandCodeNotificationEntry } from './extractors/notification'
import { commandCodeEvent, commandCodeText } from './protocol'
import { COMMAND_CODE_TOOL_RESULTS } from './spanRole'

const NOTIFICATION_EVENTS: ReadonlySet<string> = new Set([
  ...COMMAND_CODE_NOTIFICATION_EVENT,
  COMMAND_CODE_EVENT.SubagentProgress,
])

const NOTIFICATION_FRAME_KINDS = new Set([COMMAND_CODE_FRAME_KIND.Event])

export function classifyCommandCodeMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const notification = notificationClassifierFor(input.agentProvider, commandCodeNotificationEntry)
  if (input.wrapper?.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(input.wrapper, NOTIFICATION_FRAME_KINDS))
    return notification(input.wrapper.messages, 'hidden')
  if (!parent)
    return { kind: 'unknown' }
  if (!parent.type && typeof parent.content === 'string') {
    return parent.planExecution === true
      ? { kind: 'plan_execution' }
      : { kind: 'user_content' }
  }
  if (parent.method === COMMAND_CODE_METHOD.TurnCompleted)
    return { kind: 'result_divider' }
  const event = commandCodeEvent(parent)
  if (!event)
    return { kind: 'unknown' }
  const type = pickString(event, 'type')
  if (type === COMMAND_CODE_EVENT.ToolQueued) {
    return retainedRowIsFinal(input.completion)
      ? { kind: 'tool_result' }
      : { kind: 'tool_use' }
  }
  if (COMMAND_CODE_TOOL_RESULTS.has(type))
    return { kind: 'tool_result' }
  if (type === COMMAND_CODE_EVENT.ThinkingEnd) {
    return pickString(event, 'text')
      ? { kind: 'assistant_thinking' }
      : { kind: 'hidden' }
  }
  if (type === COMMAND_CODE_EVENT.MessageEnd) {
    return commandCodeText(event.content)
      ? { kind: 'assistant_text' }
      : { kind: 'hidden' }
  }
  if (NOTIFICATION_EVENTS.has(type))
    return notification([parent])
  return { kind: 'unknown' }
}
