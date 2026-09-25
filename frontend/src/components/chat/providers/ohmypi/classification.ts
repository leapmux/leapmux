import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { OH_MY_PI_EVENT, OH_MY_PI_ROLE } from '~/generated/contracts/ohmypi-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { ohMyPiNotificationEntry } from './extractors/notification'
import { ohMyPiContentText } from './messageContent'

/**
 * omp frames that carry no transcript surface of their own.
 *
 * The worker drops or consumes each of them before it persists anything: a lifecycle
 * marker restates a transition the transcript already draws, a streamed delta is
 * folded into the message that completes it, and a settings, goal or subagent frame
 * feeds a surface outside the transcript. This set hides only the rows an earlier
 * build wrote, which no later worker fix reaches.
 */
const OH_MY_PI_HIDDEN_EVENT_TYPES = new Set<string>([
  OH_MY_PI_EVENT.Ready,
  OH_MY_PI_EVENT.RpcChunk,
  OH_MY_PI_EVENT.PromptResult,
  OH_MY_PI_EVENT.AgentStart,
  OH_MY_PI_EVENT.TurnStart,
  OH_MY_PI_EVENT.TurnEnd,
  OH_MY_PI_EVENT.MessageStart,
  OH_MY_PI_EVENT.MessageUpdate,
  OH_MY_PI_EVENT.ToolExecutionUpdate,
  OH_MY_PI_EVENT.ToolStreamUpdate,
  OH_MY_PI_EVENT.ExtensionUIResponse,
  OH_MY_PI_EVENT.TodoAutoClear,
  OH_MY_PI_EVENT.GoalUpdated,
  OH_MY_PI_EVENT.ModelChanged,
  OH_MY_PI_EVENT.ThinkingLevelChanged,
  OH_MY_PI_EVENT.ConfigUpdate,
  OH_MY_PI_EVENT.SubagentLifecycle,
  OH_MY_PI_EVENT.SubagentProgress,
  OH_MY_PI_EVENT.SubagentEvent,
  OH_MY_PI_EVENT.AvailableCommandsUpdate,
  OH_MY_PI_EVENT.SessionInfoUpdate,
  OH_MY_PI_EVENT.TtsrTriggered,
  OH_MY_PI_EVENT.AdvisorCostChanged,
  OH_MY_PI_EVENT.AdvisorYielded,
  OH_MY_PI_EVENT.ConfigWarningsChanged,
  OH_MY_PI_EVENT.HostToolCall,
  OH_MY_PI_EVENT.HostToolCancel,
  OH_MY_PI_EVENT.HostUriRequest,
  OH_MY_PI_EVENT.HostUriCancel,
])

/**
 * omp frames that the worker persists as notifications.
 *
 * They are also the NON-PROGRESS set: each one is visible but says nothing about the
 * agent working, so the chat-level thinking heuristic keeps scanning past them. When
 * the worker threads several into one wrapper, each must be recognized as a thread
 * entry, or only the first would draw.
 */
const OH_MY_PI_NOTIFICATION_TYPES = new Set<string>([
  OH_MY_PI_EVENT.AutoCompactionStart,
  OH_MY_PI_EVENT.AutoCompactionEnd,
  OH_MY_PI_EVENT.AutoRetryStart,
  OH_MY_PI_EVENT.AutoRetryEnd,
  OH_MY_PI_EVENT.RetryFallbackApplied,
  OH_MY_PI_EVENT.RetryFallbackSucceeded,
  OH_MY_PI_EVENT.Notice,
  OH_MY_PI_EVENT.ExtensionError,
  OH_MY_PI_EVENT.CommandOutput,
  OH_MY_PI_EVENT.TodoReminder,
  OH_MY_PI_EVENT.IrcMessage,
  OH_MY_PI_EVENT.RpcFrameError,
  OH_MY_PI_EVENT.ExtensionUIRequest,
  // The answer to the worker's own `compact` command, which states the compaction.
  OH_MY_PI_EVENT.Response,
])

/** omp message classification. */
export function classifyOhMyPiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, ohMyPiNotificationEntry)

  // The empty-wrapper test runs FIRST, so the thread test below stays the one
  // narrowing on `wrapper`.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, OH_MY_PI_NOTIFICATION_TYPES))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // A user row the service layer persisted is LeapMux's `{content}` shape, with no omp
  // `type`. It is matched BEFORE the event dispatch, so it does not fall through to the
  // unknown card.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === OH_MY_PI_EVENT.AgentEnd)
    return { kind: 'result_divider' }

  if (type === OH_MY_PI_EVENT.ToolExecutionStart) {
    // A turn that ended while the call ran stores this frame AGAIN as the closing row,
    // with the partial result omp reported beside it. That copy is the call's result.
    return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
  }
  if (type === OH_MY_PI_EVENT.ToolExecutionEnd)
    return { kind: 'tool_result' }

  if (type === OH_MY_PI_EVENT.MessageEnd) {
    const message = pickObject(parent, 'message')
    const role = pickString(message, 'role')
    // The reply's TEXT alone. The worker persists its thinking as a reasoning row of
    // its own, before this one, so a reply with no text draws nothing here.
    if (role === OH_MY_PI_ROLE.Assistant)
      return ohMyPiContentText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    if (role === OH_MY_PI_ROLE.Custom) {
      // omp writes a custom message for the model: a job's result, a late diagnostic,
      // the file of a skill the reader called. None of them is the assistant's reply,
      // whatever its attribution, so each draws as a notice, and a copy that another
      // row already draws reads as no entry and hides.
      if (message?.display === false)
        return { kind: 'hidden' }
      return notification([parent], 'hidden')
    }
    // The user's prompt, a tool result and a shell echo each reach the transcript by
    // another row: LeapMux's own user row and the tool span.
    return { kind: 'hidden' }
  }

  if (OH_MY_PI_NOTIFICATION_TYPES.has(type))
    return notification([parent], 'hidden')

  if (OH_MY_PI_HIDDEN_EVENT_TYPES.has(type))
    return { kind: 'hidden' }

  // LeapMux's own envelope, which every provider answers the same way. No branch
  // above claims one of these rows, because omp's vocabulary spells none of their
  // tokens.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
