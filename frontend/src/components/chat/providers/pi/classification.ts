import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { PI_ENTRY_TYPE, PI_EVENT, PI_NOTIFICATION_EVENT, PI_ROLE } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { piSubagentNotifications, piVisibleCustomMessage } from './extractors/customMessage'
import { piNotificationEntry } from './extractors/notification'
import { piPlanStatement } from './extractors/plan'
import { piContentText } from './messageContent'
import { piSessionEntry } from './sessionEntry'

/** The contract supplies notification membership for both readers. */
const PI_NOTIFICATION_SURFACE_TYPES = new Set<string>([
  ...PI_NOTIFICATION_EVENT,
  PI_EVENT.ExtensionUIRequest,
])

/** Pi message classification. */
export function classifyPiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, piNotificationEntry)

  // Hide an empty wrapper before the thread test narrows its type on the false path.
  // The thread test refuses empty wrappers also.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // Read native notification threads as well as the base LeapMux types.
  // The thread renderer must display every entry, including multiple compaction ends and retries.
  if (wrapper?.messages.some(member => isObject(member) && piSessionEntry(member)?.type === PI_ENTRY_TYPE.Compaction)
    || isNotificationThreadWrapper(wrapper, PI_NOTIFICATION_SURFACE_TYPES)) {
    // Empty extension notices have no display. Hide a thread that contains only such notices.
    return notification(wrapper.messages, 'hidden')
  }

  if (!parent)
    return { kind: 'unknown' }

  // classifyMessage handles the neutral {isSynthetic, controlResponse} shape before it calls the plugin.

  const type = pickString(parent, 'type')

  if (type === PI_EVENT.EntryAppended) {
    const entry = piSessionEntry(parent)
    if (entry?.type === PI_ENTRY_TYPE.CustomMessage && entry.display === true)
      return piContentText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    if (entry?.type === PI_ENTRY_TYPE.Compaction)
      return notification([parent])
    return { kind: 'unknown' }
  }

  // The Worker stores user input as {content, attachments}, without a type field.
  // Read that neutral envelope before native event dispatch.
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === PI_EVENT.AgentEnd)
    return { kind: 'result_divider' }

  if (type === PI_EVENT.MessageEnd) {
    if (piVisibleCustomMessage(parent)) {
      if (piSubagentNotifications(parent))
        return { kind: 'tool_result' }
      return piContentText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    }
    const messageRole = pickString(pickObject(parent, 'message'), 'role')
    if (messageRole === PI_ROLE.Custom)
      return { kind: 'hidden' }
    if (messageRole !== PI_ROLE.Assistant)
      return { kind: 'unknown' }
    // The worker persists the message's thinking as a reasoning row of its own,
    // before this row, so this row draws the text alone.
    if (piContentText(parent).trim() !== '')
      return { kind: 'assistant_text' }
    return { kind: 'hidden' }
  }

  if (type === PI_EVENT.ToolExecutionStart || type === PI_EVENT.ToolExecutionEnd) {
    // A proposed plan uses piPlanStatement in both extraction paths.
    // Either span row can contain the plan, so test it before the two tool branches.
    const plan = piPlanStatement(parent)
    if (plan)
      return plan.kind === 'plan' ? { kind: 'assistant_plan' } : { kind: 'assistant_text' }
  }
  if (type === PI_EVENT.ToolExecutionStart) {
    // An interrupted call stores its start frame as a closing row with any native partial result.
    // Read that closing copy as the result. See piSpanRole.
    if (retainedRowIsFinal(input.completion))
      return { kind: 'tool_result' }
    return { kind: 'tool_use' }
  }
  if (type === PI_EVENT.ToolExecutionEnd) {
    // The end frame contains the result and error flag but no arguments.
    // The chat store pairs it with its request through spanId.
    return { kind: 'tool_result' }
  }

  if (PI_NOTIFICATION_EVENT.has(type))
    return notification([parent], 'hidden')

  if (type === PI_EVENT.ExtensionUIRequest) {
    // The control surface handles dialog requests. The Worker converts one-way methods to session state or transcript entries.
    // Read an informational request as a notification. Hide an empty notice because it supplies no display text.
    return notification([parent], 'hidden')
  }

  // Read an unwrapped LeapMux envelope after native dispatch.
  // Pi's event vocabulary does not overlap these types, and the user envelope requires no type.
  // The thread test above handles wrapped envelopes through BASE_NOTIFICATION_TYPES.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
