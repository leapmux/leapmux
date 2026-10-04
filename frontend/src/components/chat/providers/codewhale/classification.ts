import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import type { CodewhaleToolFrame } from './extractors/toolCommon'
import { CODEWHALE_EVENT, CODEWHALE_ITEM_KIND } from '~/generated/contracts/codewhale-protocol'
import { pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { notificationClassifierFor } from '../../notificationClassification'
import { codewhaleMessageText } from './extractors/message'
import { codewhaleNotificationEntry } from './extractors/notification'
import { codewhaleEnvelope, codewhaleFrameDrawsNothing, codewhaleItem, codewhaleToolFrame, codewhaleToolSpanRole } from './extractors/toolCommon'

/**
 * The runtime events the worker persists as notification rows.
 *
 * Each states something the reader must know and nothing about the conversation: a
 * steer that the runtime dropped and that nothing sends again, an approval nobody
 * answered in time, a sandbox refusal, and a failure to save the thread.
 */
const CODEWHALE_NOTIFICATION_EVENTS: ReadonlySet<string> = new Set<string>([
  CODEWHALE_EVENT.TurnSteerDropped,
  CODEWHALE_EVENT.ApprovalTimeout,
  CODEWHALE_EVENT.SandboxDenied,
  CODEWHALE_EVENT.StoreFailure,
])

/** The non-tool items the worker persists as notification rows. */
const CODEWHALE_NOTIFICATION_ITEMS: ReadonlySet<string> = new Set<string>([
  CODEWHALE_ITEM_KIND.Status,
  CODEWHALE_ITEM_KIND.ContextCompaction,
  CODEWHALE_ITEM_KIND.Error,
])

/** Codewhale message classification. */
export function classifyCodewhaleMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, codewhaleNotificationEntry)

  // A notification THREAD holds runtime events and LeapMux's own notifications side by
  // side, and a runtime event states `event` where the shared wrapper test reads
  // `type`. Every member is a notification by construction, so the entries decide, and
  // a thread of entries that draw nothing collapses to hidden.
  if (wrapper)
    return wrapper.messages.length === 0 ? { kind: 'hidden' } : notification(wrapper.messages, 'hidden')
  if (!parent)
    return { kind: 'unknown' }

  const envelope = codewhaleEnvelope(parent)
  if (envelope)
    return classifyEvent(input, envelope.event, notification)

  // A user row the service layer persisted is the LeapMux-neutral `{content}` shape,
  // with no runtime event. It is matched before the shared notification types, which
  // carry a `type` it never has.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }
  if (isPlainNotificationType(type))
    return notification([parent])

  // A block of a subagent's own transcript.
  const frame = codewhaleToolFrame(parent)
  if (frame)
    return codewhaleToolCategory(frame, input)
  return messageCategory(parent) ?? { kind: 'unknown' }
}

/** The category of one tool row, from either transcript. See `codewhaleFrameDrawsNothing`. */
function codewhaleToolCategory(frame: CodewhaleToolFrame, input: ClassificationInput): MessageCategory {
  if (codewhaleFrameDrawsNothing(frame))
    return { kind: 'hidden' }
  return codewhaleToolSpanRole(frame, input) === 'result' ? { kind: 'tool_result' } : { kind: 'tool_use' }
}

/** The category of one message row: a reply, a reasoning step, or a row with no text. */
function messageCategory(parent: Record<string, unknown>): MessageCategory | null {
  const message = codewhaleMessageText(parent)
  if (!message)
    return null
  if (!message.text.trim())
    return { kind: 'hidden' }
  return message.kind === 'thinking' ? { kind: 'assistant_thinking' } : { kind: 'assistant_text' }
}

/** The category of one runtime event. */
function classifyEvent(
  input: ClassificationInput,
  event: string,
  notification: ReturnType<typeof notificationClassifierFor>,
): MessageCategory {
  const parent = input.parentObject ?? {}
  if (event === CODEWHALE_EVENT.TurnCompleted)
    return { kind: 'result_divider' }
  if (CODEWHALE_NOTIFICATION_EVENTS.has(event))
    return notification([parent], 'hidden')

  const frame = codewhaleToolFrame(parent)
  if (frame)
    return codewhaleToolCategory(frame, input)
  const item = codewhaleItem(parent)
  if (!item)
    return { kind: 'unknown' }
  // LeapMux already persisted what the reader sent, and the worker drops the runtime's
  // own echo; a stored one repeats it.
  if (item.kind === CODEWHALE_ITEM_KIND.UserMessage)
    return { kind: 'hidden' }
  if (CODEWHALE_NOTIFICATION_ITEMS.has(item.kind))
    return notification([parent], 'hidden')
  return messageCategory(parent) ?? { kind: 'unknown' }
}
