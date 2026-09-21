import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CODEX_ITEM, CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { isJsonRpcResponseObject } from '../acp/classification'
import { extractItem } from './extractors/item'
import { codexNotificationEntry } from './extractors/notification'
import { codexPlanItemMarkdown, codexTurnPlanParams, codexTurnPlanTodos } from './extractors/plan'
import { codexReasoningHasText } from './extractors/row'
import { CODEX_RATE_LIMITS_METHOD } from './rateLimits'

const CODEX_TURN_FAILED_NOTIFICATION = 'Codex turn failed'

function isCodexJsonRpcResponse(parent: Record<string, unknown>): boolean {
  if ('item' in parent || 'turn' in parent)
    return false
  return isJsonRpcResponseObject(parent)
}

function isCodexEmptyCompletedWebSearch(item: Record<string, unknown>): boolean {
  const query = pickString(item, 'query').trim()
  const action = pickObject(item, 'action')
  const actionType = pickString(action, 'type')

  if (actionType === 'other')
    return query.length === 0

  if (actionType === 'openPage')
    return !action?.url

  return false
}

/**
 * The Codex method that reports an automatic compaction of the thread.
 *
 * The Worker persists it as a threadable notification. The COMPLETION of a
 * `contextCompaction` item is the compaction boundary, not this method, so the
 * chat hides this one.
 */
const CODEX_THREAD_COMPACTED_METHOD = CODEX_METHOD.ThreadCompacted
/** The Codex method that reports one finished item of a turn. */
const CODEX_ITEM_COMPLETED_METHOD = CODEX_METHOD.ItemCompleted
/**
 * Codex JSON-RPC methods that, when persisted as SYSTEM, are notification-thread
 * entries. The consolidator treats these the same way `system+subtype` events
 * are treated for Claude.
 */
const CODEX_NOTIF_METHODS = new Set<string>([
  CODEX_RATE_LIMITS_METHOD,
  CODEX_METHOD.SkillsChanged,
  CODEX_METHOD.RemoteControlStatusChanged,
  CODEX_METHOD.HookCompleted,
  CODEX_METHOD.ThreadTokenUsageUpdated,
  CODEX_METHOD.McpServerOauthLoginCompleted,
  CODEX_METHOD.ThreadNameUpdated,
  CODEX_METHOD.McpServerStartupStatusUpdated,
  // The two the notification reader already builds an entry for -- a `retry` entry
  // stating whether Codex will try again, and a `status` entry for a warning. Neither
  // was claimed here, so the frames classified `unknown` and drew raw JSON: the entry
  // builder was reachable only in theory.
  CODEX_METHOD.Error,
  CODEX_METHOD.Warning,
])

/**
 * Codex-emitted methods that should not appear in the chat: turn/thread
 * lifecycle, metadata invalidations (skills), connection status
 * (remoteControl), and hook starts. Transcript history can contain these methods,
 * so classify them out both standalone and inside a consolidated thread.
 * The two paths must agree.
 *
 * Module-private. It once fed the browser's working-state heuristic as well,
 * which had to skip anything the chat hides; the Worker now publishes that
 * state, so hiding a method is a rendering decision only.
 */
const CODEX_HIDDEN_TRANSCRIPT_METHODS = new Set<string>([
  CODEX_METHOD.ThreadStarted,
  CODEX_METHOD.TurnStarted,
  CODEX_METHOD.ThreadStatusChanged,
  CODEX_METHOD.ThreadNameUpdated,
  CODEX_METHOD.ThreadSettingsUpdated,
  CODEX_METHOD.ThreadTokenUsageUpdated,
  CODEX_METHOD.SkillsChanged,
  CODEX_METHOD.RemoteControlStatusChanged,
  CODEX_METHOD.HookStarted,
  CODEX_METHOD.McpToolCallProgress,
  CODEX_METHOD.RawResponseItemCompleted,
  CODEX_RATE_LIMITS_METHOD,
  CODEX_THREAD_COMPACTED_METHOD,
])

function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  // Read the members BEFORE the shared test. Its type predicate narrows `wrapper` to
  // `null` on the false path, so a read after the call cannot reach them.
  const messages = wrapper?.messages ?? []
  if (isNotificationThreadWrapper(wrapper, undefined, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')) {
    return true
  }
  // Codex method-based notifications now arriving as SYSTEM-roled raw
  // JSON-RPC envelopes — recognize them by the inner `method` field.
  return messages.some((msg) => {
    if (!isObject(msg))
      return false
    if (pickString(pickObject(msg, 'item'), 'type') === CODEX_ITEM.ContextCompaction)
      return true
    const method = pickString(msg, 'method')
    if (CODEX_NOTIF_METHODS.has(method))
      return true
    // item/started for a contextCompaction item is the in-progress compacting
    // indicator, and item/completed for the same item is the boundary that
    // closes it. Both belong in the notification thread.
    if (method === CODEX_METHOD.ItemStarted || method === CODEX_ITEM_COMPLETED_METHOD)
      return pickString(pickObject(pickObject(msg, 'params'), 'item'), 'type') === CODEX_ITEM.ContextCompaction
    return false
  })
}

// The Worker also represents a failed turn in its divider. Remove that normalized
// notification before extraction so one failure does not draw twice. The method
// sets above own frames with no surface; codexNotificationEntry owns visible outcomes.
function isCodexHiddenNotificationThreadMessage(m: unknown): boolean {
  if (!isObject(m))
    return false
  if (m.type === NOTIFICATION_TYPE.AgentError && m.error === CODEX_TURN_FAILED_NOTIFICATION)
    return true
  return false
}

type CodexItemClassifier = (item: Record<string, unknown>) => MessageCategory

/**
 * Per-item-type classifier for messages shaped as `{item: {type, ...}, ...}`.
 * Keyed by the `item.type` string; missing entries fall through to `'unknown'`.
 */
const CODEX_ITEM_CLASSIFIERS: Record<string, CodexItemClassifier> = {
  [CODEX_ITEM.AgentMessage]: () => ({ kind: 'assistant_text' }),
  // A proposed plan leaves the tool path, and BOTH layers read it through the same
  // function: `classify` said `tool_use` while `extractRow` drew `assistant-plan`,
  // so the list measured a tool row and the transcript painted a plan card into it.
  // A plan item with no words states nothing, which is a hidden row and not an
  // empty band.
  [CODEX_ITEM.Plan]: item => codexPlanItemMarkdown(item) !== null
    ? { kind: 'assistant_plan' }
    : { kind: 'hidden' },
  [CODEX_ITEM.CommandExecution]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.FileChange]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.McpToolCall]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.DynamicToolCall]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.CollabAgentToolCall]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.ImageGeneration]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.ImageView]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.WebSearch]: (item) => {
    if (isCodexEmptyCompletedWebSearch(item))
      return { kind: 'hidden' }
    return { kind: 'tool_use' }
  },
  // The extractor's own read, so a measured row is always a row that draws.
  [CODEX_ITEM.Reasoning]: item => codexReasoningHasText(item) ? { kind: 'assistant_thinking' } : { kind: 'hidden' },
  [CODEX_ITEM.UserMessage]: () => ({ kind: 'hidden' }),
  // The five kinds Codex added after this table was written. The row extractor draws
  // each as a status row, and the classifier covered none of them -- so the list
  // measured an unrecognized row and the transcript then painted a tool row into it.
  // Both readers must answer the same kind; `rowKindInvariant` is the guard.
  [CODEX_ITEM.Sleep]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.EnteredReviewMode]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.ExitedReviewMode]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.HookPrompt]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.FunctionCallOutput]: () => ({ kind: 'tool_use' }),
  // subAgentActivity (v2) is consumed by the backend's background-task
  // registry drive and never persisted. A legacy row (pre-migration) must not
  // render raw JSON, so classify it hidden defensively.
  [CODEX_ITEM.SubAgentActivity]: () => ({ kind: 'hidden' }),
}

/** Codex message classification. */
export function classifyCodexMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, codexNotificationEntry)

  // Empty wrapper — hide. This runs BEFORE the thread test, which narrows `wrapper`
  // to `null` on its false path. The thread test refuses an empty wrapper anyway, so
  // the order changes no answer.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // Notification threads (settings_changed, context_cleared, etc.)
  if (isCodexNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter(m => !isCodexHiddenNotificationThreadMessage(m))
    return notification(msgs, 'hidden')
  }

  if (!parent)
    return { kind: 'unknown' }

  // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
  // classifyMessage, before any plugin?.transcript.classify runs, since it is a LeapMux-neutral shape.)

  if (isCodexJsonRpcResponse(parent))
    return { kind: 'hidden' }

  const type = pickString(parent, 'type')
  const subtype = pickString(parent, 'subtype')
  const method = pickString(parent, 'method')

  // Transcript history can contain lifecycle methods. Keep them hidden.
  if (CODEX_HIDDEN_TRANSCRIPT_METHODS.has(method))
    return { kind: 'hidden' }

  // The two method frames whose entries `codexNotificationEntry` already builds --
  // a `retry` entry stating whether Codex will try again, and a `status` entry for
  // a warning. Neither was claimed here, so both classified `unknown` and drew raw
  // JSON, and the entry builder was reachable only in theory. The worker converts a
  // live `error` into an agent_error notification, so that one arrives this way
  // from replayed history; `warning` has no worker case at all and arrives raw on
  // every session.
  if (method === CODEX_METHOD.Error || method === CODEX_METHOD.Warning)
    return notification([parent])

  if (type === 'system') {
    if (subtype === 'init' || subtype === 'task_notification')
      return { kind: 'hidden' }
    if (isFinalCompactingStatus(parent))
      return { kind: 'hidden' }
    return notification([parent])
  }

  if (type === NOTIFICATION_TYPE.AgentError && parent.error === CODEX_TURN_FAILED_NOTIFICATION)
    return { kind: 'hidden' }

  // The same read the extractor makes, for the same reason the `turn/completed`
  // gate below states: a frame this claims and `codexTurnPlanRow` then refuses is
  // measured as a tool row and painted as the raw notification JSON.
  if (method === CODEX_METHOD.TurnPlanUpdated && codexTurnPlanTodos(codexTurnPlanParams(parent)) !== null)
    return { kind: 'tool_use' }

  // turn/completed → result divider. Require a *string* status so the gate
  // matches codexResultDivider's `pickString` read: a non-string status would
  // classify as a divider the hook then can't render (returning null), leaking
  // raw JSON instead of a clean turn-end row.
  const turn = pickObject(parent, 'turn')
  if (turn && typeof turn.status === 'string' && turn.status)
    return { kind: 'result_divider' }

  // item/completed dispatch — keyed on item.type via the classifier table. The
  // item is resolved through the SAME unwrap the extractor uses, so a row stored
  // as the raw `item/started` envelope reaches its classifier instead of falling
  // through to `unknown` and drawing raw JSON.
  const item = extractItem(parent) ?? undefined
  const itemType = item ? pickString(item, 'type', undefined) : undefined
  if (item && itemType) {
    if (itemType === CODEX_ITEM.ContextCompaction)
      return notification([parent])
    // `Object.hasOwn`, not a bare read: `itemType` comes straight off the wire,
    // and a value that spells an `Object.prototype` member answers with a function
    // the call below would then run.
    const itemClassifier = Object.hasOwn(CODEX_ITEM_CLASSIFIERS, itemType) ? CODEX_ITEM_CLASSIFIERS[itemType] : undefined
    if (itemClassifier)
      return itemClassifier(item)
  }

  // User message (persisted by LeapMux service layer)
  if (!parent.item && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (method === CODEX_METHOD.McpServerStartupStatusUpdated)
    return notification([parent], 'hidden')

  if (method === CODEX_METHOD.McpServerOauthLoginCompleted)
    return notification([parent], 'hidden')

  if (method === CODEX_METHOD.HookCompleted)
    return notification([parent], 'hidden')

  // The LeapMux envelope every provider answers the same way. The shared predicate is
  // the one list: a copy here held six of the seven types and drew the raw frame for
  // `plan_execution`.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
