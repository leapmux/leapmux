import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { kimiNotificationEntry } from './extractors/notification'
import { kimiPlanText } from './extractors/plan'
import { kimiEvent } from './protocol'

/**
 * The event types the worker records as notification rows.
 *
 * A turn the agent started by itself, a retry, a compaction, a warning, an error and a
 * background task's notice. Each states something the reader acts on and owns no other
 * surface. The same set keeps a consolidated thread of them alive.
 */
export const KIMI_NOTIFICATION_TYPES: ReadonlySet<string> = new Set<string>([
  KIMI_EVENT.TurnStarted,
  KIMI_EVENT.TurnStepRetrying,
  KIMI_EVENT.CompactionStarted,
  KIMI_EVENT.CompactionCompleted,
  KIMI_EVENT.CompactionBlocked,
  KIMI_EVENT.CompactionCancelled,
  KIMI_EVENT.Warning,
  KIMI_EVENT.Error,
  KIMI_EVENT.TaskNotified,
])

/** Whether one thread entry is a Kimi Code notice. */
function kimiNotifies(entry: unknown): boolean {
  const event = kimiEvent(entry)
  return !!event && KIMI_NOTIFICATION_TYPES.has(event.type)
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

  const event = kimiEvent(parent)
  if (!event) {
    // A row the service layer wrote: the LeapMux-neutral `{content}` user row, or one
    // of LeapMux's own notices.
    if (typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isObject(parent) && isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  switch (event.type) {
    case KIMI_EVENT.ToolCallStarted: {
      // A retained copy of this frame is the call's RESULT: the turn ended while the
      // call ran, so the server sent no result and the worker stored the start again.
      const retained = retainedRowIsFinal(input.completion)
      // The plan an `ExitPlanMode` call proposes is drawn as the plan itself. The
      // retained copy of that start is the call's closing row, which is hidden for the
      // same reason the result of a plan call is: the plan row already states the plan.
      if (kimiPlanText(parent) !== null)
        return retained ? { kind: 'hidden' } : { kind: 'assistant_plan' }
      return retained ? { kind: 'tool_result' } : { kind: 'tool_use' }
    }
    case KIMI_EVENT.ToolResult:
      // The plan row and the answer row already state the plan and the decision, and
      // the result repeats the whole plan for the model.
      if (input.spanType === KIMI_TOOL.ExitPlanMode)
        return { kind: 'hidden' }
      return { kind: 'tool_result' }
    case KIMI_EVENT.TurnEnded:
      return { kind: 'result_divider' }
  }
  if (KIMI_NOTIFICATION_TYPES.has(event.type))
    return notification([parent], 'hidden')
  // Every other event carries no surface of its own; the worker persists none of them.
  return { kind: 'hidden' }
}
