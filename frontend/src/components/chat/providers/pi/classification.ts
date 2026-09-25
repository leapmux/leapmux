import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { piSubagentNotifications, piVisibleCustomMessage } from './extractors/customMessage'
import { piNotificationEntry } from './extractors/notification'
import { piPlanStatement } from './extractors/plan'
import { piContentText } from './messageContent'

/**
 * Pi event types that carry no UI surface of their own.
 *
 * Two groups, and both reach this rule for the same reason. A LIFECYCLE marker
 * states a transition the transcript already draws: `agent_settled` says only that
 * Pi will not continue on its own after the `agent_end` that drew the divider. A
 * CONSUMED event feeds a surface outside the transcript: a thinking-level change
 * and a queue update reach the settings and the session-info channels, and Pi's own
 * session name reaches nothing at all, because LeapMux gives its tabs their own names.
 *
 * The worker drops or consumes every one of them before it persists anything
 * (`handlePiOutput`), so this rule hides only the rows an earlier build wrote. That
 * is exactly why it must list them: a retained row draws raw JSON otherwise, and no
 * later worker fix reaches a row that is already in the database.
 */
const PI_HIDDEN_EVENT_TYPES = new Set<string>([
  PI_EVENT.AgentStart,
  PI_EVENT.AgentSettled,
  PI_EVENT.TurnStart,
  PI_EVENT.TurnEnd,
  PI_EVENT.MessageStart,
  PI_EVENT.MessageUpdate,
  PI_EVENT.ToolExecutionUpdate,
  PI_EVENT.QueueUpdate,
  PI_EVENT.BashExecutionUpdate,
  PI_EVENT.SessionInfoChanged,
  PI_EVENT.ThinkingLevelChanged,
  PI_EVENT.ExtensionUIResponse,
  PI_EVENT.Response,
])

/**
 * Pi notification-style event types.
 *
 * The three `summarization_retry_*` events belong here for the same reason the
 * auto-retry pair does: each states that a summary failed and that Pi waits before
 * it tries again, which is a stall the reader must be able to explain. The worker
 * persists all three as notifications, so a frontend that did not list them here
 * classified each one as unknown and drew raw JSON -- and a consolidated thread that
 * held one rendered its first entry alone.
 */
const PI_NOTIFICATION_EVENT_TYPES = new Set<string>([
  PI_EVENT.CompactionStart,
  PI_EVENT.CompactionEnd,
  PI_EVENT.AutoRetryStart,
  PI_EVENT.AutoRetryEnd,
  PI_EVENT.ExtensionError,
  PI_EVENT.SummarizationRetryScheduled,
  PI_EVENT.SummarizationRetryAttemptStart,
  PI_EVENT.SummarizationRetryFinished,
])

/**
 * The full Pi notification surface: the notification-style events plus the
 * extension UI passthrough. These thread into chat as notifications (so they're
 * non-progress for the working-state heuristic) and, when the backend
 * consolidates several into one `notification_thread` envelope, each must be
 * recognized as a thread entry -- otherwise only the first would render.
 */
const PI_NOTIFICATION_SURFACE_TYPES = new Set<string>([
  ...PI_NOTIFICATION_EVENT_TYPES,
  PI_EVENT.ExtensionUIRequest,
])

/** Pi message classification. */
export function classifyPiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, piNotificationEntry)

  // An empty wrapper hides. This runs BEFORE the thread test, whose type predicate
  // narrows `wrapper` to `null` on its false path. The thread test refuses an empty
  // wrapper anyway, so the order changes no answer.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // Wrapper-style notification thread. Beyond the base LeapMux types
  // (settings_changed, context_cleared, etc.), recognize a consolidated
  // wrapper of Pi notification events -- e.g. several compaction_end
  // boundaries, or auto_retry + compaction_end -- so renderNotificationThread
  // renders every entry instead of MessageBubble showing only the first.
  if (isNotificationThreadWrapper(wrapper, PI_NOTIFICATION_SURFACE_TYPES)) {
    // Drop notifications that render nothing (an empty-message extension notify)
    // so a thread of only those collapses to `hidden` instead of falling back
    // to a raw-JSON bubble.
    return notification(wrapper.messages, 'hidden')
  }

  if (!parent)
    return { kind: 'unknown' }

  // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
  // classifyMessage, before any plugin?.transcript.classify runs, since it is a LeapMux-neutral shape.)

  const type = pickString(parent, 'type')

  // EVERY entry, whatever its kind. An entry states a fact the event stream
  // already stated -- a `message` entry IS the assistant and tool rows, a
  // `compaction` entry repeats the compaction pair -- or it is bookkeeping for the
  // session file, and an extension's own `custom` entry needs that extension's own
  // renderer, which LeapMux does not have.
  //
  // The worker keeps only the two custom kinds and drops the rest, so the seven
  // others reach this rule as rows an EARLIER build wrote. Each of them used to
  // fall through to `unknown` and draw raw JSON, and no later worker fix reaches a
  // row that is already in the database.
  if (type === PI_EVENT.EntryAppended)
    return { kind: 'hidden' }

  // User messages persisted by the LeapMux service layer are stored as
  // plain `{"content":"...","attachments":[...]}` with no `type` field —
  // not a Pi RPC event. Match this shape *before* event-type dispatch so
  // Pi-persisted user echoes don't fall through to the unknown fallback
  // (which would JSON-stringify the body into the chat bubble).
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === PI_EVENT.AgentEnd)
    return { kind: 'result_divider' }

  if (PI_HIDDEN_EVENT_TYPES.has(type))
    return { kind: 'hidden' }

  if (type === PI_EVENT.MessageEnd) {
    if (piVisibleCustomMessage(parent)) {
      if (piSubagentNotifications(parent))
        return { kind: 'tool_result' }
      return piContentText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    }
    // Pi emits message_end for *every* message added to the conversation —
    // the user's prompt, tool results, and bash-execution echoes — not just
    // the assistant's reply. LeapMux already persists the user message via
    // the synthetic user_content row, and tool results render through the
    // tool_execution_* span. Hide these to avoid duplicates; only the
    // assistant's message_end should reach the chat view.
    // Pi's wire envelope carries the message author under `role` (Anthropic
    // Messages API style), distinct from the proto-side MessageSource that
    // describes who persisted the row. Read the wire field by name.
    const messageRole = pickString(pickObject(parent, 'message'), 'role')
    if (messageRole !== 'assistant')
      return { kind: 'hidden' }
    // The worker persists the message's thinking as a reasoning row of its own,
    // before this row, so this row draws the text alone.
    if (piContentText(parent).trim() !== '')
      return { kind: 'assistant_text' }
    return { kind: 'hidden' }
  }

  if (type === PI_EVENT.ToolExecutionStart || type === PI_EVENT.ToolExecutionEnd) {
    // A proposed plan leaves the tool path, and BOTH layers read it through the
    // same function -- see `piPlanStatement`. Either side of the span can carry
    // it, so the test runs before both branches below.
    const plan = piPlanStatement(parent)
    if (plan)
      return plan.kind === 'plan' ? { kind: 'assistant_plan' } : { kind: 'assistant_text' }
  }
  if (type === PI_EVENT.ToolExecutionStart) {
    // A turn that ended while the call ran stores this frame AGAIN as the closing
    // row, with the partial result Pi did report beside it. That copy is the
    // call's result, so it reads as one; see piSpanRole.
    if (retainedRowIsFinal(input.completion))
      return { kind: 'tool_result' }
    return { kind: 'tool_use' }
  }
  if (type === PI_EVENT.ToolExecutionEnd) {
    // Pi's tool_execution_end carries `{toolCallId, toolName, result,
    // isError}` — no args. Classify as `tool_result` so the result
    // renderer reads only what's there, and the chat store pairs it
    // with the matching tool_execution_start via spanId.
    return { kind: 'tool_result' }
  }

  if (PI_NOTIFICATION_EVENT_TYPES.has(type))
    return notification([parent], 'hidden')

  if (type === PI_EVENT.ExtensionUIRequest) {
    // Dialog requests are surfaced as control requests (handled outside the
    // chat flow); fire-and-forget methods become session-info or transcript
    // entries server-side. An informational request that yields a renderable
    // line is a notification; one with nothing to show (e.g. a notify with an
    // empty message) is hidden rather than surfaced as a raw-JSON bubble.
    return notification([parent], 'hidden')
  }

  // The LeapMux envelope, which every provider answers the same way.
  //
  // The rule sits LAST, so every Pi-specific branch keeps its own answer. This one runs
  // where Pi's own vocabulary has no match, ahead of the raw-JSON fallback below.
  //
  // No branch above claims one of these rows. Every branch above but one matches `type`
  // against Pi's event vocabulary, and that vocabulary spells none of these tokens. The
  // one exception is the user echo, which requires a row with NO `type`.
  //
  // This rule answers for an UNWRAPPED row. The thread test at the top answers for a
  // threaded one, because `BASE_NOTIFICATION_TYPES` holds all of these types too.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
