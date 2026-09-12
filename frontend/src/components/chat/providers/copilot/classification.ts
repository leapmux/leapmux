import type { MessageCategory } from '../../messageClassification'
import type { ClassificationInput } from '../registry'
import { COPILOT_EVENT, COPILOT_EVENT_PREFIX } from '~/generated/contracts/copilot-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { turnEndLabel } from '../../turnEndLabel'
import { retainedRowIsFinal } from '../registry'
import { describeCopilotNotification } from './notification'
import { copilotEvent } from './protocol'

/**
 * The events that thread into chat as notifications.
 *
 * Each one states something the user acts on and owns no other surface: a failure, a
 * warning, a compaction, a subagent outcome, a context reset.
 */
export const COPILOT_NOTIFICATION_TYPES = new Set<string>([
  COPILOT_EVENT.SessionError,
  COPILOT_EVENT.SessionWarning,
  COPILOT_EVENT.SessionInfo,
  COPILOT_EVENT.SessionCompactionStart,
  COPILOT_EVENT.SessionCompactionComplete,
  COPILOT_EVENT.SessionContextCleared,
  COPILOT_EVENT.SessionTruncation,
  COPILOT_EVENT.ModelCallFailure,
  COPILOT_EVENT.SubagentCompleted,
  COPILOT_EVENT.SubagentFailed,
  COPILOT_EVENT.SystemNotification,
])

/**
 * Events that carry no surface of their own.
 *
 * Each is hidden for a stated reason, and the reasons differ:
 *
 *   - The session lifecycle events repeat what the create and resume responses
 *     already returned.
 *   - A streaming delta is transient, and the worker never stores one; a historical
 *     row from an older build would repeat the finished message.
 *   - A turn boundary is bookkeeping: `session.idle` is the divider.
 *   - A mode, permission or model change reaches the settings panel, not the chat.
 *   - `subagent.started` repeats the `task` tool call that opened it, which is the
 *     row that already renders the launch, and `subagent.configured` states the model
 *     and effort that launch runs under -- a model change, which reaches the settings
 *     panel and not the chat, exactly as `session.model_change` does.
 *   - Every control COMPLETION announces an answer the control surface recorded.
 *   - A title change would fight LeapMux's own naming, and a to-do or plan change
 *     states only THAT the list moved: the `update_todo` tool call carries the list.
 */
const COPILOT_HIDDEN_TYPES = new Set<string>([
  COPILOT_EVENT.SessionStart,
  COPILOT_EVENT.SessionResume,
  COPILOT_EVENT.SessionShutdown,
  COPILOT_EVENT.SessionTitleChanged,
  COPILOT_EVENT.SessionTodosChanged,
  COPILOT_EVENT.SessionPlanChanged,
  COPILOT_EVENT.SessionUsageInfo,
  COPILOT_EVENT.SessionLimitsChanged,
  COPILOT_EVENT.SessionModeChanged,
  COPILOT_EVENT.SessionPermissionsChanged,
  COPILOT_EVENT.SessionModelChange,
  COPILOT_EVENT.SessionAutopilotObjectiveChanged,
  COPILOT_EVENT.AssistantTurnStart,
  COPILOT_EVENT.AssistantTurnEnd,
  COPILOT_EVENT.AssistantUsage,
  COPILOT_EVENT.AssistantMessageDelta,
  COPILOT_EVENT.AssistantStreamingDelta,
  COPILOT_EVENT.AssistantToolCallDelta,
  COPILOT_EVENT.AssistantReasoningDelta,
  COPILOT_EVENT.ToolProgress,
  COPILOT_EVENT.ToolPartialResult,
  COPILOT_EVENT.SubagentStarted,
  COPILOT_EVENT.SubagentConfigured,
  // The runtime calls its own `task_complete` TOOL and then announces the same thing
  // here. Both halves of that call are rows already, and the result row reads
  // `Task completed:` with the summary this event repeats.
  COPILOT_EVENT.SessionTaskComplete,
  COPILOT_EVENT.Abort,
  COPILOT_EVENT.PermissionCompleted,
  COPILOT_EVENT.UserInputCompleted,
  COPILOT_EVENT.ExitPlanModeCompleted,
  COPILOT_EVENT.ElicitationCompleted,
  // A user message the runtime echoes back. LeapMux persists the user's own row when
  // it delivers the input, so this one would double it.
  COPILOT_EVENT.UserMessage,
])

/**
 * Reports whether an event type names a family that describes the RUNTIME.
 *
 * The `model.` family is the runtime's own model-call trace, and the `hook.` family
 * states that the runtime ran one of its own hooks. The worker drops both, so a live
 * turn never stores one. A transcript an earlier build wrote still holds them -- ten
 * for one ordinary turn -- and each reached the reader as a raw-JSON bubble.
 *
 * `model.call_failure` is the exception, because LeapMux surfaces that one as a
 * notification.
 */
function copilotDescribesRuntime(type: string): boolean {
  if (type === COPILOT_EVENT.ModelCallFailure)
    return false
  return type.startsWith(COPILOT_EVENT_PREFIX.ModelTrace) || type.startsWith(COPILOT_EVENT_PREFIX.Hook)
}

/**
 * The control REQUESTS. Each one reaches the control surface, which is where it is
 * answered, so the transcript copy states the ask without an action on it.
 */
const COPILOT_CONTROL_REQUEST_TYPES = new Set<string>([
  COPILOT_EVENT.PermissionRequested,
  COPILOT_EVENT.UserInputRequested,
  COPILOT_EVENT.ExitPlanModeRequested,
  COPILOT_EVENT.ElicitationRequested,
])

/** True for a Copilot event that belongs in a notification thread. */
function copilotNotifies(entry: unknown): boolean {
  const event = copilotEvent(entry)
  return !!event && (COPILOT_NOTIFICATION_TYPES.has(event.type) || COPILOT_CONTROL_REQUEST_TYPES.has(event.type)
    || event.type === COPILOT_EVENT.SkillInvoked || event.type === COPILOT_EVENT.SystemMessage)
}

export function classifyCopilotMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A Copilot row is a JSON-RPC frame with no top-level `type`, so the shared
    // wrapper test cannot recognize one. Read the event instead, and drop the
    // entries that state nothing so a thread of only those collapses to hidden.
    if (wrapper.messages.some(entry => copilotNotifies(entry))) {
      const messages = wrapper.messages.filter(entry => describeCopilotNotification(entry) !== null)
      return messages.length === 0 ? { kind: 'hidden' } : { kind: 'notification', messages }
    }
    if (isNotificationThreadWrapper(wrapper))
      return { kind: 'notification', messages: wrapper.messages }
  }

  if (!parent)
    return { kind: 'unknown' }

  // A row the service layer wrote is the LeapMux-neutral `{content}` shape and carries
  // no native frame. It is matched before the event dispatch so a user echo does not
  // reach the unknown fallback and get stringified into the bubble.
  const event = copilotEvent(parent)
  if (!event) {
    if (typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isPlainNotificationType(pickString(parent, 'type')))
      return { kind: 'notification', messages: [parent] }
    return { kind: 'unknown' }
  }

  switch (event.type) {
    case COPILOT_EVENT.AssistantMessage:
      return pickString(event.data, 'content').trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    case COPILOT_EVENT.AssistantReasoning:
      return pickString(event.data, 'content').trim() ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    case COPILOT_EVENT.ToolStarted:
      // A retained copy of this frame is the call's RESULT: the turn ended while the
      // call ran, so the runtime sent no completion and the worker stored the start
      // frame again. See copilotSpanRole.
      if (retainedRowIsFinal(input.completion))
        return { kind: 'tool_result' }
      return { kind: 'tool_use', toolName: pickString(event.data, 'toolName') || 'tool', toolUse: parent, content: [] }
    case COPILOT_EVENT.ToolCompleted:
      return { kind: 'tool_result' }
    case COPILOT_EVENT.SessionIdle:
      return { kind: 'result_divider' }
  }
  if (copilotNotifies(parent)) {
    // A row whose describer reads nothing renders as an empty notification, so hide
    // it rather than surfacing a blank line or a raw-JSON bubble.
    return describeCopilotNotification(parent) === null ? { kind: 'hidden' } : { kind: 'notification', messages: [parent] }
  }
  if (COPILOT_HIDDEN_TYPES.has(event.type) || copilotDescribesRuntime(event.type))
    return { kind: 'hidden' }
  return { kind: 'unknown' }
}

/** The text a quoted Copilot row carries. */
export function copilotQuotableText(category: MessageCategory, parsed: { parentObject?: Record<string, unknown> }): string | null {
  const parent = parsed.parentObject
  if (!parent)
    return null
  if (category.kind === 'assistant_text' || category.kind === 'assistant_thinking')
    return pickString(copilotEvent(parent)?.data, 'content').trim() || null
  if ((category.kind === 'user_content' || category.kind === 'plan_execution') && typeof parent.content === 'string')
    return parent.content.trim() || null
  return null
}

/**
 * The turn-end label for one `session.idle` row.
 *
 * The runtime reports no duration for a turn, so the label carries none.
 */
export function copilotResultDivider(parsed: unknown): { label: string, isError?: boolean } | null {
  if (!isObject(parsed))
    return null
  const data = copilotEvent(parsed)?.data
  if (!data)
    return null
  return { label: turnEndLabel(data.aborted === true ? 'interrupted' : 'ended') }
}
