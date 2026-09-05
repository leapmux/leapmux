/* eslint-disable solid/components-return-once -- renderMessage is a plain dispatcher returning JSX, not a Solid component */
import type { JSX } from 'solid-js'
import type { Question } from '../../controls/types'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { ClassificationContext, ClassificationInput, Provider } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { AgentSessionInfo, ContextUsageInfo, RateLimitInfo } from '~/stores/agentSession.store'
import type { CommandStreamSegment } from '~/stores/chatTypes'
import { buildJsonRpcResult } from '~/components/chat/controls/types'
import { buildPlanMode } from '~/components/chat/settingsGroups'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'
import { CODEX_RATE_LIMIT_REACHED_TIME_WINDOW, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { CODEX_RATE_LIMITS_METHOD, codexRateLimitReachedType, iterCodexRateLimitTiers } from '~/lib/rateLimitUtils'
import { CODEX_INTERNAL_TOOL, CODEX_ITEM, CODEX_METHOD, CODEX_STATUS } from '~/types/toolMessages'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import { defaultMarkPreview } from '../../markPreviewShared'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from '../../messageUtils'
import { isJsonRpcResponseObject } from '../acp/classification'
import { registerProvider } from '../registry'
import {
  CodexControlActions,
  CodexControlContent,
  codexRequestedPermissions,
  markCodexPlanPromptResponse,
  resolveCodexDecisions,
  sendCodexUserInputRejectResponse,
  sendCodexUserInputResponse,
} from './CodexControlRequest'
import {
  CODEX_OPTION_COLLABORATION_MODE,
  DEFAULT_CODEX_COLLABORATION_MODE,
} from './constants'
import { codexControlResponseDisplay } from './controlResponse'
import { CODEX_RENDERERS } from './defineRenderer'
import { codexToolResultImages } from './extractors/image'
import { codexNotificationThreadEntry } from './notifications'
// The named imports below are the renderers dispatched explicitly (not via
// the registry) by `renderMessage` for non-`item.type` shapes.
import {
  CodexAgentMessageRenderer,
  CodexMcpToolCallRenderer,
  CodexReasoningRenderer,
  codexResultDivider,
  CodexTurnPlanRenderer,
} from './renderers'
import { extractItem } from './renderHelpers'
import { codexToolResultMeta } from './toolResult'
// Side-effect import: each renderer module's `defineCodexRenderer(...)` call
// runs at load time and registers itself in `CODEX_RENDERERS`. Without this
// import a tree-shaking bundler could drop the modules whose named exports
// aren't directly referenced below.
import './renderers/registerAll'

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

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set([NOTIFICATION_TYPE.AgentError])
/**
 * Codex JSON-RPC methods that, when persisted as SYSTEM, are notification-thread
 * entries. The consolidator treats these the same way `system+subtype` events
 * are treated for Claude.
 */
const CODEX_NOTIF_METHODS = new Set<string>([
  CODEX_RATE_LIMITS_METHOD,
  CODEX_METHOD.SKILLS_CHANGED,
  CODEX_METHOD.REMOTE_CONTROL_STATUS_CHANGED,
  'thread/tokenUsage/updated',
  'thread/name/updated',
  'mcpServer/startupStatus/updated',
])

/**
 * Codex-emitted methods that should not appear in the chat: turn/thread
 * lifecycle, metadata invalidations (skills), connection status
 * (remoteControl), and hook lifecycle. Persisted upstream; classified out
 * here -- both standalone (the classify `method` gate) and inside a
 * consolidated thread (isCodexHiddenNotificationThreadMessage), so the two
 * paths agree.
 *
 * Exported because `agentState.ts`'s working-state heuristic must skip these
 * too — anything we hide from the chat must also be ignored when deciding
 * "is the agent thinking?". Adding a method here propagates automatically.
 */
export const CODEX_HIDDEN_LIFECYCLE_METHODS = new Set<string>([
  CODEX_METHOD.THREAD_STARTED,
  CODEX_METHOD.TURN_STARTED,
  CODEX_METHOD.THREAD_STATUS_CHANGED,
  CODEX_METHOD.THREAD_NAME_UPDATED,
  CODEX_METHOD.THREAD_SETTINGS_UPDATED,
  CODEX_METHOD.THREAD_TOKEN_USAGE_UPDATED,
  CODEX_METHOD.SKILLS_CHANGED,
  CODEX_METHOD.REMOTE_CONTROL_STATUS_CHANGED,
  CODEX_METHOD.HOOK_STARTED,
  CODEX_METHOD.HOOK_COMPLETED,
])

function isCodexNotifThread(wrapper: { old_seqs: number[], messages: unknown[] } | null): wrapper is { old_seqs: number[], messages: unknown[] } {
  if (isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')) {
    return true
  }
  // Codex method-based notifications now arriving as SYSTEM-roled raw
  // JSON-RPC envelopes — recognize them by the inner `method` field.
  if (!wrapper || (wrapper as { messages: unknown[] }).messages.length < 1)
    return false
  return (wrapper as { messages: unknown[] }).messages.some((msg: unknown) => {
    if (!isObject(msg))
      return false
    const directItem = (msg as Record<string, unknown>).item
    if (isObject(directItem) && (directItem as Record<string, unknown>).type === 'contextCompaction')
      return true
    const method = (msg as Record<string, unknown>).method
    if (typeof method !== 'string')
      return false
    if (CODEX_NOTIF_METHODS.has(method))
      return true
    // item/started for a contextCompaction item is the in-progress
    // compacting indicator. The completed contextCompaction item closes it.
    if (method === 'item/started') {
      const params = (msg as Record<string, unknown>).params
      if (isObject(params)) {
        const item = (params as Record<string, unknown>).item
        if (isObject(item) && (item as Record<string, unknown>).type === 'contextCompaction')
          return true
      }
    }
    return false
  })
}

/** Returns true when a Codex rate limit message has all tiers below the warning threshold. */
function isCodexRateLimitAllAllowed(m: Record<string, unknown>): boolean {
  if (m.method !== CODEX_RATE_LIMITS_METHOD)
    return false
  // An authoritative reached-type (rate limit / credits / usage cap) is a real
  // block -- never hide it, even when every rolling window is under threshold.
  if (codexRateLimitReachedType(m))
    return false
  for (const { info } of iterCodexRateLimitTiers(m)) {
    if (info.status !== 'allowed')
      return false
  }
  return true
}

/**
 * Per-message hidden rules for a Codex notification, applied by the
 * consolidated-thread filter so a notification that is hidden on its own stays
 * hidden once Hub threads it into a `notification_thread` wrapper -- mirroring
 * Claude's isHiddenClaudeNotification. Without this the two paths drift: the
 * standalone classifier hides lifecycle methods and final statuses while the
 * thread filter kept them, so a thread of only-hidden entries surfaced as a
 * `notification` that renders nothing and falls back to a raw-JSON bubble.
 *
 * Hides:
 *  - lifecycle/metadata methods (CODEX_HIDDEN_LIFECYCLE_METHODS): thread/started,
 *    turn/started, thread/{status,name,settings,tokenUsage}/updated,
 *    skills/changed, remoteControl/status/changed, hook/{started,completed} --
 *    transient signals persisted upstream, never rendered in chat.
 *  - a final (non-compacting) system status (see isFinalCompactingStatus).
 *  - the "Codex turn failed" agent_error (surfaced via the result divider).
 *  - an all-allowed rate-limit update (no throttle to show).
 *
 * The full wrapper is still preserved verbatim for "Copy Raw JSON" (that reads
 * `parsed.rawText`, not the filtered category messages), so nothing is lost.
 */
function isCodexHiddenNotificationThreadMessage(m: unknown): boolean {
  if (!isObject(m))
    return false
  const msg = m as Record<string, unknown>
  const method = msg.method
  if (typeof method === 'string' && CODEX_HIDDEN_LIFECYCLE_METHODS.has(method))
    return true
  if (isFinalCompactingStatus(msg))
    return true
  if (msg.type === NOTIFICATION_TYPE.AgentError && msg.error === CODEX_TURN_FAILED_NOTIFICATION)
    return true
  return isCodexRateLimitAllAllowed(msg)
}

type CodexItemClassifier = (item: Record<string, unknown>, context?: ClassificationContext) => MessageCategory

/**
 * Per-item-type classifier for messages shaped as `{item: {type, ...}, ...}`.
 * Keyed by the `item.type` string; missing entries fall through to `'unknown'`.
 */
const CODEX_ITEM_CLASSIFIERS: Record<string, CodexItemClassifier> = {
  [CODEX_ITEM.AGENT_MESSAGE]: () => ({ kind: 'assistant_text' }),
  [CODEX_ITEM.PLAN]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.PLAN, toolUse: item, content: [] }),
  [CODEX_ITEM.COMMAND_EXECUTION]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.COMMAND_EXECUTION, toolUse: item, content: [] }),
  [CODEX_ITEM.FILE_CHANGE]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.FILE_CHANGE, toolUse: item, content: [] }),
  [CODEX_ITEM.MCP_TOOL_CALL]: item => ({ kind: 'tool_use', toolName: pickString(item, 'tool') || 'mcpTool', toolUse: item, content: [] }),
  [CODEX_ITEM.DYNAMIC_TOOL_CALL]: item => ({ kind: 'tool_use', toolName: pickString(item, 'tool') || 'dynamicTool', toolUse: item, content: [] }),
  [CODEX_ITEM.COLLAB_AGENT_TOOL_CALL]: (item) => {
    if (item.tool === 'spawnAgent' && item.status === CODEX_STATUS.COMPLETED)
      return { kind: 'hidden' }
    return { kind: 'tool_use', toolName: CODEX_ITEM.COLLAB_AGENT_TOOL_CALL, toolUse: item, content: [] }
  },
  [CODEX_ITEM.IMAGE_GENERATION]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.IMAGE_GENERATION, toolUse: item, content: [] }),
  [CODEX_ITEM.IMAGE_VIEW]: item => ({ kind: 'tool_use', toolName: CODEX_ITEM.IMAGE_VIEW, toolUse: item, content: [] }),
  [CODEX_ITEM.WEB_SEARCH]: (item) => {
    if (isCodexEmptyCompletedWebSearch(item))
      return { kind: 'hidden' }
    return { kind: 'tool_use', toolName: CODEX_ITEM.WEB_SEARCH, toolUse: item, content: [] }
  },
  [CODEX_ITEM.REASONING]: (item, context) => {
    const summary = item.summary as unknown[] | undefined
    const content = item.content as unknown[] | undefined
    if ((!summary || summary.length === 0) && (!content || content.length === 0))
      return context?.hasCommandStream ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    return { kind: 'assistant_thinking' }
  },
  [CODEX_ITEM.USER_MESSAGE]: () => ({ kind: 'hidden' }),
  // subAgentActivity (v2) is consumed by the backend's background-task
  // registry drive and never persisted. A legacy row (pre-migration) must not
  // render raw JSON, so classify it hidden defensively.
  subAgentActivity: () => ({ kind: 'hidden' }),
}

/** LeapMux-side notification `type` values produced by the worker. */
const CODEX_LEAPMUX_NOTIFICATION_TYPES = new Set<string>([
  NOTIFICATION_TYPE.SettingsChanged,
  NOTIFICATION_TYPE.ContextCleared,
  NOTIFICATION_TYPE.Interrupted,
  NOTIFICATION_TYPE.AgentError,
  NOTIFICATION_TYPE.PlanUpdated,
  NOTIFICATION_TYPE.Compacting,
])

/** Codex rate limits: {method:"account/rateLimits/updated", params:{rateLimits:{primary,secondary}}}. */
function codexRateLimitsFromMessage(parsed: ParsedMessageContent): { key: string, info: RateLimitInfo }[] | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.method !== CODEX_RATE_LIMITS_METHOD)
    return null
  const results: { key: string, info: RateLimitInfo }[] = []
  for (const { info } of iterCodexRateLimitTiers(inner)) {
    if (info.rateLimitType)
      results.push({ key: info.rateLimitType, info })
  }
  // Mirror the backend's elevate so this replay path agrees with the live session-info broadcast:
  // when the authoritative reached-type says a time-windowed limit is hit but rounding kept every
  // window under 100%, surface the most-utilized window as "exceeded".
  if (codexRateLimitReachedType(inner) === CODEX_RATE_LIMIT_REACHED_TIME_WINDOW
    && !results.some(r => r.info.status === 'exceeded')) {
    let top: { key: string, info: RateLimitInfo } | undefined
    for (const r of results) {
      if (!top || (r.info.utilization ?? 0) > (top.info.utilization ?? 0))
        top = r
    }
    if (top)
      top.info = { ...top.info, status: 'exceeded' }
  }
  return results
}

/** Codex context usage from a `thread/tokenUsage/updated` notification (`params.tokenUsage.last`). */
function codexContextUsageFromNotification(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.method !== 'thread/tokenUsage/updated')
    return null
  const params = inner.params as Record<string, unknown> | undefined
  const tokenUsage = params?.tokenUsage as Record<string, unknown> | undefined
  const last = tokenUsage?.last as Record<string, unknown> | undefined
  if (!last || typeof last.inputTokens !== 'number')
    return null
  const cached = typeof last.cachedInputTokens === 'number' ? last.cachedInputTokens as number : 0
  const contextUsage: ContextUsageInfo = {
    inputTokens: Math.max((last.inputTokens as number) - cached, 0),
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: cached,
  }
  if (typeof tokenUsage?.modelContextWindow === 'number')
    contextUsage.contextWindow = tokenUsage.modelContextWindow as number
  return contextUsage
}

/**
 * Codex lifecycle → session-info patch: clear the live turn id on thread/started, clear the plan
 * streaming indicator on a `plan` item.
 */
function codexLifecycleSessionInfo(parsed: ParsedMessageContent): Partial<AgentSessionInfo> | null {
  const method = parsed.parentObject?.method as string | undefined
  const item = parsed.parentObject?.item as Record<string, unknown> | undefined
  const patch: Partial<AgentSessionInfo> = {}
  // A new Codex thread starts idle. Clear any stale turn ID restored from localStorage so the chat
  // shows its empty state instead of a phantom thinking indicator.
  if (method === 'thread/started')
    patch.codexTurnId = ''
  if (item?.type === 'plan')
    patch.streamingType = ''
  return Object.keys(patch).length > 0 ? patch : null
}

/**
 * Codex: a persisted span row supersedes its command stream when a commandExecution/fileChange
 * item is completed, or a reasoning item now carries summary/content.
 */
function codexCommandSpanSuperseded(parsed: ParsedMessageContent): boolean {
  const item = parsed.parentObject?.item as Record<string, unknown> | undefined
  if (!item)
    return false
  if ((item.type === 'commandExecution' || item.type === 'fileChange') && item.status === 'completed')
    return true
  return item.type === 'reasoning'
    && (((item.summary as unknown[] | undefined)?.length ?? 0) > 0 || ((item.content as unknown[] | undefined)?.length ?? 0) > 0)
}

// Map a Codex command-stream delta method to its segment kind; unknown methods are plain output.
const CODEX_METHOD_TO_SEGMENT_KIND: Record<string, CommandStreamSegment['kind']> = {
  'item/commandExecution/terminalInteraction': 'interaction',
  'item/reasoning/summaryTextDelta': 'reasoning_summary',
  'item/reasoning/textDelta': 'reasoning_content',
  'item/reasoning/summaryPartAdded': 'reasoning_summary_break',
}

const codexPlugin: Provider = {
  permissionPresets: { bypass: CODEX_BYPASS_SETTINGS },
  // Seed a new Codex agent with its default collaboration mode.
  defaultProviderOptions: { [CODEX_OPTION_COLLABORATION_MODE]: DEFAULT_CODEX_COLLABORATION_MODE },
  // Codex accepts an option selection AND a free-text note together, so the
  // AskUserQuestion UI keeps both instead of treating them as mutually exclusive.
  preservesSelectionNotes: true,
  // Codex collab child threads accept host-initiated turns inside the same
  // process, so a child tab keeps an enabled composer. AgentInfo.accepts_messages
  // (the backend-authoritative field) wins when present; this is the fallback.
  supportsSubagentSend: true,
  attachments: {
    text: true,
    image: true,
    pdf: false,
    binary: false,
  },
  // Codex's wire format dispatches via JSON-RPC `method`. Anything we hide
  // from the chat plus the metadata-only updates (mcp startup, rate limits,
  // thread compaction) must also be invisible to the working-state
  // heuristic — adding a method to either set propagates automatically.
  nonProgressMethods: new Set<string>([
    ...CODEX_HIDDEN_LIFECYCLE_METHODS,
    'mcpServer/startupStatus/updated',
    'account/rateLimits/updated',
  ]),
  // Codex exposes an explicit turn ID for the active turn. Prefer it over
  // the generic message-history heuristic so idle-but-running tabs don't
  // show as thinking on creation, and so post-reconnect rehydration is
  // driven by the authoritative server-side state.
  hasActiveTurn: (_agent, sessionInfo) => Boolean(sessionInfo?.codexTurnId),
  planMode: buildPlanMode(CODEX_OPTION_COLLABORATION_MODE, 'plan', DEFAULT_CODEX_COLLABORATION_MODE),
  // The trigger's mode segment shows the "Workflow" (collaboration_mode) group --
  // Codex's mode axis -- not the approval policy. It reads "Plan Mode" when the
  // workflow sits at its plan value.
  triggerModeGroupKey: CODEX_OPTION_COLLABORATION_MODE,
  classify(input: ClassificationInput, context?: ClassificationContext): MessageCategory {
    const parent = input.parentObject
    const wrapper = input.wrapper

    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper)) {
      const msgs = wrapper.messages.filter(m => !isCodexHiddenNotificationThreadMessage(m))
      return msgs.length === 0
        ? { kind: 'hidden' }
        : { kind: 'notification', messages: msgs }
    }

    // Empty wrapper — hide.
    if (wrapper && (wrapper as { messages: unknown[] }).messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
    // classifyMessage, before any plugin.classify runs, since it is a LeapMux-neutral shape.)

    if (isCodexJsonRpcResponse(parent))
      return { kind: 'hidden' }

    const type = parent.type as string | undefined
    const subtype = parent.subtype as string | undefined
    const method = parent.method as string | undefined

    // Lifecycle methods are transient signals; persist upstream, hide here.
    if (method && CODEX_HIDDEN_LIFECYCLE_METHODS.has(method))
      return { kind: 'hidden' }

    if (type === 'system') {
      if (subtype === 'init' || subtype === 'task_notification')
        return { kind: 'hidden' }
      if (isFinalCompactingStatus(parent))
        return { kind: 'hidden' }
      return { kind: 'notification', messages: [parent] }
    }

    if (type === NOTIFICATION_TYPE.AgentError && parent.error === CODEX_TURN_FAILED_NOTIFICATION)
      return { kind: 'hidden' }

    if (method === CODEX_METHOD.TURN_PLAN_UPDATED && isObject(parent.params))
      return { kind: 'tool_use', toolName: CODEX_INTERNAL_TOOL.TURN_PLAN, toolUse: parent, content: [] }

    // turn/completed → result divider. Require a *string* status so the gate
    // matches codexResultDivider's `pickString` read: a non-string status would
    // classify as a divider the hook then can't render (returning null), leaking
    // raw JSON instead of a clean turn-end row.
    const turn = pickObject(parent, 'turn')
    if (turn && typeof turn.status === 'string' && turn.status)
      return { kind: 'result_divider' }

    // item/completed dispatch — keyed on item.type via the classifier table.
    const item = pickObject(parent, 'item') ?? undefined
    const itemType = item ? pickString(item, 'type', undefined) : undefined
    if (item && itemType) {
      if (itemType === 'contextCompaction')
        return { kind: 'notification', messages: [parent] }
      const itemClassifier = CODEX_ITEM_CLASSIFIERS[itemType]
      if (itemClassifier)
        return itemClassifier(item, context)
    }

    // User message (persisted by LeapMux service layer)
    if (!parent.item && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    // Codex method-based notifications
    if (method === CODEX_RATE_LIMITS_METHOD) {
      return isCodexRateLimitAllAllowed(parent) ? { kind: 'hidden' } : { kind: 'notification', messages: [parent] }
    }

    if (method === CODEX_METHOD.MCP_SERVER_STARTUP_STATUS_UPDATED)
      return { kind: 'notification', messages: [parent] }

    if (type && CODEX_LEAPMUX_NOTIFICATION_TYPES.has(type))
      return { kind: 'notification', messages: [parent] }

    return { kind: 'unknown' }
  },

  renderMessage(category: MessageCategory, parsed: unknown, context?: RenderContext): JSX.Element | null {
    if (category.kind === 'assistant_text')
      return <CodexAgentMessageRenderer parsed={parsed} context={context} />
    if (category.kind === 'assistant_thinking')
      return <CodexReasoningRenderer parsed={parsed} context={context} />
    if (category.kind === 'user_content')
      return <UserContentMessage parsed={parsed} context={context} />
    if (category.kind === 'plan_execution') {
      const obj = isObject(parsed) ? parsed as Record<string, unknown> : null
      const text = obj && typeof obj.content === 'string' ? obj.content as string : ''
      return text ? <PlanExecutionMessage text={text} context={context} /> : null
    }
    if (category.kind === 'tool_use') {
      // Use the item stored in category.toolUse (resolved to final state in classify).
      const cat = category as { toolName: string, toolUse: Record<string, unknown> }
      // turnPlan dispatches off `parent.method` (not item.type), so it's
      // not in CODEX_RENDERERS — handle it explicitly.
      if (cat.toolName === CODEX_INTERNAL_TOOL.TURN_PLAN)
        return <CodexTurnPlanRenderer parsed={cat.toolUse} context={context} />
      // For mcp/dynamic tool calls the toolName comes from item.tool, not
      // item.type. Look up by the actual item type (after unwrapping the
      // optional `{item, threadId}` envelope) and fall back to the generic
      // MCP body for any unrecognized tool-call shape.
      const item = extractItem(cat.toolUse)
      const itemType = item ? pickString(item, 'type', undefined) : undefined
      const Renderer = itemType ? CODEX_RENDERERS.get(itemType) : undefined
      if (Renderer && item)
        return <Renderer item={item} context={context} />
      return <CodexMcpToolCallRenderer parsed={cat.toolUse} context={context} />
    }
    return null
  },

  toolResultMeta: codexToolResultMeta,
  toolResultImages: codexToolResultImages,

  resultDivider: codexResultDivider,

  // A persisted `turn_completed` result divider is the turn boundary that must stop
  // the thinking indicator after a reconnect / missed live event -- so it clears the
  // active codex_turn_id, mirroring the ephemeral session-info clear.
  resultDividerEndsActiveTurn: subtype => subtype === 'turn_completed',

  rateLimitsFromMessage: codexRateLimitsFromMessage,
  contextUsageFromMessage: codexContextUsageFromNotification,
  // Codex turn/completed carries no `subtype`; detect via the `turn` object's status.
  resultSubtype: (parsed) => {
    const turn = getInnerMessage(parsed)?.turn as Record<string, unknown> | undefined
    return turn && typeof turn === 'object' && typeof turn.status === 'string' ? 'turn_completed' : undefined
  },
  lifecycleSessionInfo: codexLifecycleSessionInfo,
  commandSpanSuperseded: codexCommandSpanSuperseded,
  commandStreamSegmentKind: method => CODEX_METHOD_TO_SEGMENT_KIND[method] ?? null,

  extractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
    const obj = parsed.parentObject
    if (!obj)
      return null
    if (category.kind === 'assistant_text' || category.kind === 'assistant_thinking') {
      const item = pickObject(obj, 'item')
      const text = pickString(item, 'text')
      return text.trim() || null
    }
    if (category.kind === 'user_content' || category.kind === 'plan_execution') {
      if (typeof obj.content === 'string')
        return (obj.content as string).trim() || null
    }
    return null
  },

  // Codex marks only user sends and control-response answers. A user send is the LeapMux-neutral
  // `{content}` shape the shared extractor handles; a control answer is the structured
  // `{controlResponse}` row, which classifies as `control_response` and resolves its preview
  // through controlResponseDisplay (chatMarkPreview), not here.
  previewText: defaultMarkPreview,
  controlResponseDisplay: codexControlResponseDisplay,

  notificationThreadEntry: codexNotificationThreadEntry,

  askUserQuestion: {
    isRequest: payload => payload.method === 'item/tool/requestUserInput',
    extractQuestions(payload) {
      const params = pickObject(payload, 'params')
      return Array.isArray(params?.questions) ? params.questions as Question[] : []
    },
    sendAnswer: (request, sendControlResponse, questions, answerState) =>
      sendCodexUserInputResponse(sendControlResponse, request.requestId, questions, answerState),
    sendReject: (request, sendControlResponse) =>
      sendCodexUserInputRejectResponse(sendControlResponse, request.requestId),
  },

  buildControlResponse(payload, content, requestId) {
    const method = pickString(payload, 'method', '')
    if (getToolName(payload) === 'CodexPlanModePrompt')
      return markCodexPlanPromptResponse(content ? buildDenyResponse(requestId, content) : buildAllowResponse(requestId, getToolInput(payload)))
    if (method === 'item/permissions/requestApproval') {
      return buildJsonRpcResult(requestId, {
        permissions: content ? {} : codexRequestedPermissions(payload),
        scope: 'turn',
      })
    }
    const decisions = resolveCodexDecisions(pickObject(payload, 'params')?.availableDecisions)
    return buildJsonRpcResult(requestId, { decision: content ? decisions.negative : decisions.positive })
  },
  // The worker already forwards synthetic plan feedback as a user message.
  controlFeedbackAsFollowUpMessage: payload => getToolName(payload) !== 'CodexPlanModePrompt',

  ControlContent: CodexControlContent,
  ControlActions: CodexControlActions,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
