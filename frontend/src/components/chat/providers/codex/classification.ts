import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CODEX_ITEM, CODEX_METHOD, CODEX_RAW_FIELD, CODEX_RAW_TOOL } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { codexRawExecRole } from './extractors/execute'
import { extractItem } from './extractors/item'
import { codexNotificationEntry } from './extractors/notification'
import { codexPlanItemMarkdown, codexTurnPlanParams, codexTurnPlanTodos } from './extractors/plan'
import { codexReasoningHasText } from './extractors/row'

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

/** Native methods whose stored frames form notification threads. */
const CODEX_NOTIF_METHODS = new Set<string>([
  CODEX_METHOD.HookCompleted,
  CODEX_METHOD.McpServerOauthLoginCompleted,
  CODEX_METHOD.McpServerStartupStatusUpdated,
  // The RENDER notices, for which the notification reader writes the text: the
  // retry entry for an `error` that Codex will try again, and one sentence for
  // each notice below.
  CODEX_METHOD.Error,
  CODEX_METHOD.Warning,
  CODEX_METHOD.ConfigWarning,
  CODEX_METHOD.DeprecationNotice,
  CODEX_METHOD.ModelSafetyBufferingUpdated,
  CODEX_METHOD.ModelVerification,
  CODEX_METHOD.AuthRecoveryStarted,
  CODEX_METHOD.AuthRecoveryCompleted,
  // Auto-review: the worker keeps the completed review, the denial-limit warning and
  // the strict-review notice, and the notification reader draws each one.
  CODEX_METHOD.AutoApprovalReviewCompleted,
  CODEX_METHOD.GuardianWarning,
  CODEX_METHOD.StrictReviewRequired,
])

// Native exec rows pass their role checks before this remaining mirror filter.
const CODEX_HIDDEN_TRANSCRIPT_METHODS = new Set<string>([CODEX_METHOD.RawResponseItemCompleted])

function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  // Read the members before the shared type predicate narrows wrapper on its false branch.
  const messages = wrapper?.messages ?? []
  if (isNotificationThreadWrapper(wrapper))
    return true
  return messages.some((message) => {
    if (!isObject(message))
      return false
    const method = pickString(message, 'method')
    return CODEX_NOTIF_METHODS.has(method)
      || (method === CODEX_METHOD.ItemCompleted
        && pickString(pickObject(pickObject(message, 'params'), 'item'), 'type') === CODEX_ITEM.ContextCompaction)
  })
}

type CodexItemClassifier = (item: Record<string, unknown>) => MessageCategory

/**
 * Per-item-type classifier for messages shaped as `{item: {type, ...}, ...}`.
 * Keyed by the `item.type` string; missing entries fall through to `'unknown'`.
 */
const CODEX_ITEM_CLASSIFIERS: Record<string, CodexItemClassifier> = {
  [CODEX_ITEM.AgentMessage]: () => ({ kind: 'assistant_text' }),
  // Use the same plan reader for classification and extraction.
  // Measurement and rendering must agree on the row kind.
  // An empty plan item stays hidden instead of adding an empty band.
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
  [CODEX_ITEM.Reasoning]: item => codexReasoningHasText(item)
    ? { kind: 'assistant_thinking' }
    : { kind: 'hidden' },
  // These native items use the tool renderer. Classification and extraction must agree, as rowKindInvariant checks.
  [CODEX_ITEM.Sleep]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.EnteredReviewMode]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.ExitedReviewMode]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.HookPrompt]: () => ({ kind: 'tool_use' }),
  [CODEX_ITEM.FunctionCallOutput]: () => ({ kind: 'tool_use' }),
}

/** Codex message classification. */
export function classifyCodexMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, codexNotificationEntry)

  // The reducer can leave an empty notification thread.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  if (isCodexNotifThread(wrapper))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
  // classifyMessage, before any plugin?.transcript.classify runs, since it is a LeapMux-neutral shape.)

  const type = pickString(parent, 'type')
  const method = pickString(parent, 'method')

  // Native exec has no typed app-server item. Keep its supported raw rows before mirror suppression.
  const executionRole = codexRawExecRole(parent)
  const executionItem = extractItem(parent)
  if (executionRole === 'request'
    || (executionRole === 'result' && (input.spanType === CODEX_RAW_TOOL.Exec || executionItem?.[CODEX_RAW_FIELD.Name] === CODEX_RAW_TOOL.Exec))) {
    return { kind: 'tool_use' }
  }

  // Keep unsupported raw mirrors hidden after the native exec checks.
  if (CODEX_HIDDEN_TRANSCRIPT_METHODS.has(method))
    return { kind: 'hidden' }

  // The method frames whose entries `codexNotificationEntry` builds: a `retry`
  // entry stating whether Codex will try again, one sentence per RENDER notice,
  // and the status entries for hook and MCP failures. The empty policy is
  // `hidden`: a frame the entry builder suppresses draws nothing rather than
  // raw JSON. The worker persists these rows raw and routed, so this claims
  // both the standalone row and its notification thread.
  if (CODEX_NOTIF_METHODS.has(method))
    return notification([parent], 'hidden')

  // Read turn-plan content through the same helper as extraction.
  // Both readers must agree that the frame contains a tool row.
  // Otherwise, measurement expects a tool row while rendering shows raw notification JSON.
  if (method === CODEX_METHOD.TurnPlanUpdated && codexTurnPlanTodos(codexTurnPlanParams(parent)) !== null)
    return { kind: 'tool_use' }

  // turn/completed → result divider. Require a *string* status so the gate
  // matches codexResultDivider's `pickString` read: a non-string status would
  // classify as a divider the hook then can't render (returning null), leaking
  // raw JSON instead of a clean turn-end row.
  const turn = pickObject(parent, 'turn')
  if (turn && typeof turn.status === 'string' && turn.status)
    return { kind: 'result_divider' }

  // Classify the item through the same unwrap function that the extractor uses.
  // This also recognizes a tool row whose stored envelope retains item/started.
  const item = extractItem(parent) ?? undefined
  const itemType = item
    ? pickString(item, 'type', undefined)
    : undefined
  if (item && itemType) {
    if (itemType === CODEX_ITEM.ContextCompaction && method === CODEX_METHOD.ItemCompleted)
      return notification([parent])
    // `Object.hasOwn`, not a bare read: `itemType` comes straight off the wire,
    // and a value that spells an `Object.prototype` member answers with a function
    // the call below would then run.
    const itemClassifier = Object.hasOwn(CODEX_ITEM_CLASSIFIERS, itemType)
      ? CODEX_ITEM_CLASSIFIERS[itemType]
      : undefined
    if (itemClassifier)
      return itemClassifier(item)
  }

  // User message (persisted by LeapMux service layer)
  if (!parent.item && typeof parent.content === 'string') {
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  // The LeapMux envelope every provider answers the same way. The shared predicate is
  // the one list: a copy here held six of the seven types and drew the raw frame for
  // `plan_execution`.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
