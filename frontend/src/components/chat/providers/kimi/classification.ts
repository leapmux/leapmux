import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { KIMI_NOTIFICATION_EVENT, KIMI_TRANSCRIPT_TOOL, KIMI_TRANSCRIPT_TURN } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { kimiNotificationEntry } from './extractors/notification'
import { kimiPlanText } from './extractors/plan'
import { kimiEvent } from './protocol'
import { kimiTranscriptMarker, kimiTranscriptRetry, kimiTranscriptTaskText, kimiTranscriptTool, kimiTranscriptTurn } from './transcript'

/** Whether one thread entry is a Kimi Code notice. */
function kimiNotifies(entry: unknown): boolean {
  if (kimiTranscriptMarker(entry) || kimiTranscriptRetry(entry))
    return true
  const event = kimiEvent(entry)
  return !!event && KIMI_NOTIFICATION_EVENT.has(event.type)
}

/** Kimi Code message classification. */
export function classifyKimiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, kimiNotificationEntry)

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A thread of Kimi notices collapses to hidden when none of them words anything.
    if (wrapper.messages.some(kimiNotifies))
      return notification(wrapper.messages, 'hidden')
    if (isNotificationThreadWrapper(wrapper))
      return notification(wrapper.messages)
  }

  if (!parent)
    return { kind: 'unknown' }

  if (kimiTranscriptMarker(parent) || kimiTranscriptRetry(parent) || kimiTranscriptTaskText(parent) !== null)
    return notification([parent], 'hidden')

  const nativeTurn = kimiTranscriptTurn(parent)
  if (nativeTurn) {
    switch (pickString(nativeTurn, 'state')) {
      case KIMI_TRANSCRIPT_TURN.Completed:
      case KIMI_TRANSCRIPT_TURN.Cancelled:
      case KIMI_TRANSCRIPT_TURN.Failed:
        return { kind: 'result_divider' }
      case KIMI_TRANSCRIPT_TURN.Running:
        return notification([parent], 'hidden')
      case KIMI_TRANSCRIPT_TURN.Queued:
        return { kind: 'hidden' }
      default:
        return { kind: 'unknown' }
    }
  }

  const nativeTool = kimiTranscriptTool(parent)
  if (nativeTool) {
    const finished = nativeTool.state !== KIMI_TRANSCRIPT_TOOL.Running || retainedRowIsFinal(input.completion)
    if (kimiPlanText(parent) !== null) {
      return finished
        ? { kind: 'hidden' }
        : { kind: 'assistant_plan' }
    }
    return finished
      ? { kind: 'tool_result' }
      : { kind: 'tool_use' }
  }

  const event = kimiEvent(parent)
  if (!event) {
    // A row the service layer wrote: the LeapMux-neutral `{content}` user row, or one
    // of LeapMux's own notices.
    if (typeof parent.content === 'string') {
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isObject(parent) && isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  if (KIMI_NOTIFICATION_EVENT.has(event.type))
    return notification([parent], 'hidden')
  // The Worker preserves unknown native events as separate message rows.
  return { kind: 'unknown' }
}
