/**
 * The agent-event pipeline, as module-level units.
 *
 * Every handler here takes its stores as an explicit deps bag rather than
 * closing over the connection hook -- which is what makes the branches of
 * `agentMessage` independently testable, and what let this move out of a
 * 1700-line module without changing a line of behaviour.
 */
import type { AgentActivityState, AgentChatMessage, AgentControlRequest, AgentStatusChange, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { AgentActivityStore } from '~/stores/agentActivity.store'
import type { createAgentSessionStore, RateLimitInfo } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { GoalProgress } from '~/stores/chatGoal'
import type { ToolProgressRetry, ToolProgressUpdate } from '~/stores/chatToolProgress'
import type { createControlStore } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { AgentTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { classifyAgentMessage } from '~/components/chat/messageClassification'
import { providerFor } from '~/components/chat/providers/registry'
import { mergeStableOptionGroupRefs, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { GOAL_PROGRESS_FIELD, RATE_LIMIT_FIELD, RUNNING_TOOL_FIELD, RUNNING_TOOL_RETRY_FIELD, SESSION_INFO_KEY } from '~/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentStatus, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { isTabOnScreen } from '~/hooks/watchPlan'
import { assignDefined, isObject, pickBoolean, pickCounter, pickNumber, pickString } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { extractCompactionContextTokens, extractContextUsage, extractPlanFilePath, extractPlanUpdated, extractResultMetadata, extractSettingsChanges, getInnerMessage, normalizeContextUsage, parseMessageContent } from '~/lib/messageParser'
import { emitSettingsChanged } from '~/lib/settingsChangedEvent'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { compactionContextUsage } from '~/stores/agentSession.store'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { migrateErrorHintFromForResolvedRepo, upsertRepoGitFromProtoStatus } from '~/stores/repoGit'
import { deriveOptionGroupTabFields, tabKey } from '~/stores/tab.helpers'

const log = createLogger('agentEvents')

/** Shared by control-request decoding. */
const TEXT_DECODER = new TextDecoder()

/**
 * Translate a snake_case `rate_limits` broadcast payload to the camelCase
 * `RateLimitInfo` shape that the agent-session store and rate-limit utils
 * consume. The wire format is provider-agnostic snake_case (Claude/Codex
 * both emit it that way); the frontend keeps idiomatic camelCase types.
 */
function wireRateLimitsToCamel(value: unknown): Record<string, RateLimitInfo> | undefined {
  if (!isObject(value))
    return undefined
  const out: Record<string, RateLimitInfo> = {}
  for (const [key, tier] of Object.entries(value)) {
    if (!isObject(tier))
      continue
    // One checked line per field. Each picker states the type it accepts, and
    // `assignDefined` leaves the key ABSENT when the tier omits it or carries the
    // wrong type -- which matters beyond tidiness: agentSession.store compares a
    // tier with `shallowEqual`, which reads key COUNTS first. A form that wrote
    // all eight keys, with `undefined` for the ones the payload omits, would
    // compare unequal against the STORED copy on every broadcast, because the
    // two are not built the same way: the stored one is whatever the serializer
    // left behind, and this one is whatever the payload carried.
    //
    // Every picker's fallback is an explicit `undefined`, so a field's type still
    // comes from RateLimitInfo and a mismatched picker fails to compile.
    const info: RateLimitInfo = {}
    assignDefined(info, 'rateLimitType', pickString(tier, RATE_LIMIT_FIELD.RateLimitType, undefined))
    assignDefined(info, 'status', pickString(tier, RATE_LIMIT_FIELD.Status, undefined))
    assignDefined(info, 'utilization', pickNumber(tier, RATE_LIMIT_FIELD.Utilization, undefined))
    assignDefined(info, 'resetsAt', pickNumber(tier, RATE_LIMIT_FIELD.ResetsAt, undefined))
    assignDefined(info, 'surpassedThreshold', pickNumber(tier, RATE_LIMIT_FIELD.SurpassedThreshold, undefined))
    assignDefined(info, 'overageStatus', pickString(tier, RATE_LIMIT_FIELD.OverageStatus, undefined))
    assignDefined(info, 'overageResetsAt', pickNumber(tier, RATE_LIMIT_FIELD.OverageResetsAt, undefined))
    assignDefined(info, 'isUsingOverage', pickBoolean(tier, RATE_LIMIT_FIELD.IsUsingOverage, undefined))
    out[key] = info
  }
  return out
}

/**
 * Translate an `agent_session_info` wire payload (provider-agnostic snake_case)
 * into the store's camelCase `AgentSessionInfo` updates. Each field carries its
 * own predicate + transform and is included only when present/valid, so a
 * provider that omits keys (or sends a dropped-only payload) produces an empty
 * object and the caller skips the store write. Pure and exported so the
 * wire->camel boundary can be unit-tested directly without a live connection.
 */
export function wireSessionInfoToUpdates(
  info: Record<string, unknown> | undefined,
): Record<string, unknown> {
  const updates: Record<string, unknown> = {}
  if (!info)
    return updates
  // Each value binds to a local ONCE. A `typeof` guard on `info[K]` does not
  // narrow a SECOND read of the same index expression -- element-access
  // narrowing needs a literal or a `const` key, and a property of an `as const`
  // table is neither -- so a guard-then-index form would assign `unknown` and
  // the type checker would stop catching a mismatch here.
  //
  // wireRateLimitsToCamel solves the same problem the other way, with a picker
  // per field. These five keep the inline guards because each lands on a
  // differently-typed field of an untyped `updates` record, where a picker buys
  // nothing.
  const totalCostUsd = info[SESSION_INFO_KEY.TotalCostUsd]
  if (typeof totalCostUsd === 'number')
    updates.totalCostUsd = totalCostUsd
  const contextUsage = normalizeContextUsage(info[SESSION_INFO_KEY.ContextUsage])
  if (contextUsage)
    updates.contextUsage = contextUsage
  const rateLimits = info[SESSION_INFO_KEY.RateLimits]
  if (rateLimits !== undefined)
    updates.rateLimits = wireRateLimitsToCamel(rateLimits)
  // Only positive estimates: `> 0` rejects both the zero-estimate first delta
  // (nothing to show yet) and a NaN a future provider might emit (NaN > 0 is
  // false), so the indicator never has to defend against "0 tokens" or a NaN
  // serialized to null in storage.
  const thinkingTokens = info[SESSION_INFO_KEY.ThinkingTokens]
  if (typeof thinkingTokens === 'number' && thinkingTokens > 0)
    updates.thinkingTokens = thinkingTokens
  const outputBytes = info[SESSION_INFO_KEY.OutputBytes]
  if (typeof outputBytes === 'number' && outputBytes > 0) {
    updates.outputBytes = outputBytes
    const outputBytesMinimum = info[SESSION_INFO_KEY.OutputBytesMinimum]
    if (typeof outputBytesMinimum === 'boolean')
      updates.outputBytesMinimum = outputBytesMinimum
  }
  return updates
}

/**
 * Translate one `running_tool` broadcast into the span-keyed update the chat
 * store merges. Pure and exported so the shape rules are unit-testable at the
 * wire boundary, the role wireSessionInfoToUpdates plays for the scalar keys.
 *
 * It deliberately does NOT go through wireSessionInfoToUpdates: that function
 * returns scalar `AgentSessionInfo` fields, and this is span-keyed accumulating
 * state that lives in the chat store.
 *
 * Every field but `span_id` is optional, because the worker forwards two
 * families that report disjoint facts (see chatToolProgress). `retry` keeps its
 * three states: absent leaves the entry's retry alone, an object sets it, and an
 * explicit null clears it -- the agent's only "the retry resolved" signal.
 *
 * It carries ONLY what the badge renders. The payload also states the tool's
 * name and a subagent's type, and the card already has both from the tool_use
 * row, so this drops them rather than storing a value nothing reads.
 */
export function wireRunningToolToUpdate(value: unknown): ToolProgressUpdate | undefined {
  if (!isObject(value))
    return undefined
  const spanId = pickString(value, RUNNING_TOOL_FIELD.SpanId)
  if (spanId === '')
    return undefined

  const update: ToolProgressUpdate = { spanId }
  // pickCounter, not a bare pickNumber: a NaN or an Infinity reaches the
  // duration formatter and renders as "NaNs" on the card, and a negative
  // elapsed time is not a duration at all.
  const elapsed = pickCounter(value, RUNNING_TOOL_FIELD.ElapsedSeconds)
  if (elapsed !== undefined)
    update.elapsedSeconds = elapsed
  if (RUNNING_TOOL_FIELD.Retry in value) {
    const retry = wireRunningToolRetry(value[RUNNING_TOOL_FIELD.Retry])
    // An unreadable retry leaves `retry` OFF the update, so the entry keeps
    // whatever it held. Only an explicit null reaches the store as a clear.
    if (retry !== undefined)
      update.retry = retry
  }
  return update
}

/**
 * The `goal_progress` payload as the goal store holds it, or undefined when the
 * broadcast carried no readable counter.
 *
 * Each field is read INDEPENDENTLY and omitted when absent. No two providers
 * report the same set -- Codex has no iteration count, ZCode and Claude Code no
 * token usage -- so a missing field must stay missing: a zero here renders as
 * "0 tokens used", a number the provider never gave.
 *
 * Finite and non-negative for the same reason the running-tool elapsed time is:
 * a NaN reaches the formatter and renders as "NaN", and a negative count is not
 * a count.
 */
export function wireGoalProgressToUpdate(value: unknown): GoalProgress | undefined {
  if (!isObject(value))
    return undefined
  const update: GoalProgress = {}
  assignDefined(update, 'tokensUsed', pickCounter(value, GOAL_PROGRESS_FIELD.TokensUsed))
  assignDefined(update, 'tokenBudget', pickCounter(value, GOAL_PROGRESS_FIELD.TokenBudget))
  assignDefined(update, 'timeUsedSeconds', pickCounter(value, GOAL_PROGRESS_FIELD.TimeUsedSeconds))
  assignDefined(update, 'iterations', pickCounter(value, GOAL_PROGRESS_FIELD.Iterations))
  return Object.keys(update).length > 0 ? update : undefined
}

/**
 * The `retry` member of a running_tool update, or undefined when the payload
 * carries a retry this cannot read.
 *
 * The three answers are distinct on purpose. `null` is the agent's resolved
 * signal and CLEARS the badge. An object sets it. `undefined` leaves the badge
 * alone -- a payload whose shape changed must not erase a live retry, and a
 * partial badge would read "Retrying 0/0", which is worse than the last known
 * attempt.
 */
function wireRunningToolRetry(value: unknown): ToolProgressRetry | null | undefined {
  if (value === null)
    return null
  if (!isObject(value))
    return undefined
  const attempt = pickNumber(value, RUNNING_TOOL_RETRY_FIELD.Attempt, undefined)
  const maxRetries = pickNumber(value, RUNNING_TOOL_RETRY_FIELD.MaxRetries, undefined)
  if (attempt === undefined || maxRetries === undefined)
    return undefined
  return {
    attempt,
    maxRetries,
    retryDelayMs: pickNumber(value, RUNNING_TOOL_RETRY_FIELD.RetryDelayMs, 0),
    errorStatus: pickNumber(value, RUNNING_TOOL_RETRY_FIELD.ErrorStatus),
    errorCategory: pickString(value, RUNNING_TOOL_RETRY_FIELD.ErrorCategory),
  }
}

/**
 * The hook-scoped stores the agentMessage sub-handlers below write to. Passed
 * explicitly so each handler is a module-level unit (no closure over the hook), which
 * is what makes the three concerns of the agentMessage case independently testable.
 */
export interface AgentMessageStores {
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  chatStore: ReturnType<typeof createChatStore>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  getActiveWorkspaceId: () => string | null
}

/**
 * Intercept an ephemeral agent_session_info message (broadcast by the Worker without
 * persisting). The broadcast wire is snake_case across all providers; translate to the
 * frontend store's camelCase shape at this boundary so JS consumers (RateLimitInfo,
 * ContextUsageInfo, AgentSessionInfo) can stay idiomatic without forcing snake_case
 * identifiers throughout the frontend. Returns true when it consumed the message, so
 * the agentMessage case breaks before the persisted-message processing below.
 */
export function handleAgentSessionInfo(
  agentId: string,
  parsed: ParsedMessageContent,
  stores: Pick<AgentMessageStores, 'agentSessionStore' | 'chatStore'>,
): boolean {
  if (!(parsed.topLevel !== null && !parsed.wrapper && parsed.topLevel.type === NOTIFICATION_TYPE.AgentSessionInfo))
    return false
  const { agentSessionStore, chatStore } = stores
  const info = parsed.topLevel.info as Record<string, unknown> | undefined
  const updates = wireSessionInfoToUpdates(info)
  // A non-positive value is the Worker's explicit lifecycle clear.
  // wireSessionInfoToUpdates forwards positive values only.
  const thinkingTokens = info?.[SESSION_INFO_KEY.ThinkingTokens]
  if (typeof thinkingTokens === 'number' && thinkingTokens <= 0)
    agentSessionStore.clearThinkingTokens(agentId)
  const outputBytes = info?.[SESSION_INFO_KEY.OutputBytes]
  if (typeof outputBytes === 'number' && outputBytes <= 0)
    agentSessionStore.clearOutputBytes(agentId)
  // running_tool is span-keyed accumulating state, not an AgentSessionInfo field,
  // so it goes to the chat store rather than through
  // wireSessionInfoToUpdates.
  const runningTool = wireRunningToolToUpdate(info?.[SESSION_INFO_KEY.RunningTool])
  if (runningTool)
    chatStore.applyToolProgress(agentId, runningTool)
  // goal_progress is the VOLATILE half of the session goal and belongs beside
  // the goal itself in the chat store, not among the scalar AgentSessionInfo
  // fields -- same reason running_tool takes its own path above. It rides this
  // channel rather than AgentGoalChanged because Codex advances these counters
  // after every completed tool call, and the goal event fires only on a real
  // transition.
  const goalProgress = wireGoalProgressToUpdate(info?.[SESSION_INFO_KEY.GoalProgress])
  if (goalProgress)
    chatStore.goal.setProgress(agentId, goalProgress)
  // Pi (and any future provider) may broadcast session_info payloads whose keys are all
  // dropped here -- skip the store write so reactive consumers aren't woken for nothing.
  if (Object.keys(updates).length > 0)
    agentSessionStore.updateInfo(agentId, updates)
  return true
}

/**
 * Pull notification metadata out of any message regardless of source -- Codex
 * token-usage / rate-limit notifications arrive as AGENT, while LeapMux-injected
 * settings_changed / context_cleared arrive as LEAPMUX. Each branch self-gates so an
 * unrelated message (e.g. a Pi assistant message) falls through cheaply: the
 * context_cleared / settings_changed / plan branches match on the inner type, the
 * provider usage/rate-limit hooks return null for a frame they don't recognize, the
 * compaction scan self-filters by shape (isCompactBoundary), and usage folding
 * additionally requires an AGENT-source row.
 */
/**
 * Whether an agent event is being delivered LIVE or replayed during catch-up.
 *
 * Every imperative side effect in this module depends on it: replaying a
 * historical `plan_updated` used to re-apply the plan-derived title on each
 * page load, silently overwriting a tab the user had renamed by hand. It is a
 * REQUIRED parameter everywhere rather than one defaulting to 'live', so a
 * caller that forgets to thread it fails to compile instead of reintroducing
 * exactly that bug.
 */
export type CatchUpPhase = 'catchingUp' | 'live'

export function applyNotificationMetadata(agentId: string, msg: AgentChatMessage, parsed: ParsedMessageContent, stores: AgentMessageStores, catchUpPhase: CatchUpPhase): void {
  if (parsed.topLevel === null)
    return
  const { agentSessionStore, chatStore, metadata } = stores
  const plugin = providerFor(msg.agentProvider)
  const innerMsg = getInnerMessage(parsed)
  const innerType = innerMsg?.type as string | undefined

  if (innerType === NOTIFICATION_TYPE.ContextCleared) {
    agentSessionStore.clearContextUsage(agentId)
    chatStore.todos.clear(agentId)
    // The conversation was wiped, so every live indicator on it goes too. The
    // backend resets its own thinking estimator on a context clear, but that reset
    // is in-memory only (no broadcast); and the rows the running-tool badges were
    // attached to are gone. Both would otherwise linger frozen on their last value
    // until the next turn produces a delta or a clear of its own.
    clearPerTurnLiveState(agentId, stores)
  }

  // Rate limits and Codex token usage self-gate in the provider plugin (they return null for a
  // frame they don't recognize), so no rate_limit_event / account-rateLimits / tokenUsage wire
  // token is matched here.
  const rls = plugin?.rateLimitsFromMessage?.(parsed)
  if (rls && rls.length > 0) {
    const rateLimits: Record<string, RateLimitInfo> = {}
    for (const rl of rls)
      rateLimits[rl.key] = rl.info
    agentSessionStore.updateInfo(agentId, { rateLimits })
  }

  // Usage metadata (context usage + cumulative cost) for every AGENT-source message, in one pass:
  // the neutral wrapper owns the subagent-skip / cost / normalized-context_usage guards and delegates
  // the raw per-provider shape (Codex tokenUsage notification, Claude/Pi message.usage) to the plugin.
  // This is the sole call site, so a provider implements contextUsageFromMessage once and the guards
  // never live in a plugin. The AGENT-source gate is authoritative: every provider's usage frame
  // (Claude assistant, Pi message_end, Codex thread/tokenUsage/updated) is persisted AGENT-source, so
  // a USER/LEAPMUX row that happens to carry total_cost_usd / context_usage / message.usage must not
  // fold -- the same guard the old applyAgentLifecycleAndUsage enforced before this extraction moved.
  if (msg.source === MessageSource.AGENT) {
    const usage = extractContextUsage(parsed, p => plugin?.contextUsageFromMessage?.(p) ?? null)
    if (usage)
      agentSessionStore.updateInfo(agentId, usage)
  }

  // A completed compaction boundary makes the prior context-usage reading stale: the
  // grid would keep showing the pre-compaction size until the next assistant/result
  // message overwrites it. Refresh it straight from the boundary's post-compaction
  // token count (post_tokens, or pre - tokens_saved), and reset the component fields
  // since the boundary carries no input/cache breakdown -- contextTokens is
  // authoritative for the grid. Preserve the known context window so the percentage
  // denominator survives. isCompactBoundary is a neutral shape-based scan; it returns
  // undefined (a no-op) for the common assistant message that carries no boundary.
  const postTokens = extractCompactionContextTokens(parsed)
  if (postTokens !== undefined) {
    const existing = agentSessionStore.getInfo(agentId).contextUsage
    agentSessionStore.updateInfo(agentId, {
      contextUsage: compactionContextUsage(postTokens, existing),
    })
  }

  if (innerType === NOTIFICATION_TYPE.SettingsChanged) {
    const sc = extractSettingsChanges(parsed)
    if (sc)
      emitSettingsChanged(sc)
  }

  // plan_execution / plan_updated may also appear inside a notification wrapper that
  // holds multiple message types, so wrapper messages always run the walk; non-wrapper
  // messages gate on the inner type to skip the call entirely.
  if (parsed.wrapper !== null || innerType === NOTIFICATION_TYPE.PlanExecution) {
    const planFile = extractPlanFilePath(parsed)
    if (planFile)
      agentSessionStore.updateInfo(agentId, { planFilePath: planFile })
  }
  if (parsed.wrapper !== null || innerType === NOTIFICATION_TYPE.PlanUpdated) {
    const planUpdate = extractPlanUpdated(parsed)
    if (planUpdate) {
      if (planUpdate.planFilePath)
        agentSessionStore.updateInfo(agentId, { planFilePath: planUpdate.planFilePath })
      // Live only. The tab title is USER-EDITABLE, and this is the one branch
      // here that writes over a user's own choice: replaying history on reload
      // re-applied the plan's title and silently undid a manual rename. Every
      // other side effect in this function is derived state that catch-up
      // should restore, which is why only this one is restricted. (planFilePath
      // above is derived, so it still restores.)
      if (catchUpPhase === 'live' && planUpdate.updateAgentTitle && planUpdate.planTitle)
        metadata.patch(agentId, { title: planUpdate.planTitle })
    }
  }
}

/**
 * Handle a turn-end result divider (the caller gates on category.kind ===
 * 'result_divider'). Clears live per-turn state and rehydrates
 * contextWindow / total_cost_usd. Turn-end sound and tab badging are owned by
 * the worker's busy -> idle edge (`handleAgentSettled`), not this divider — leaving
 * both would ring a visible tab twice.
 *
 * Each provider plugin classifies its FINAL envelope (Claude type:"result",
 * Codex turn/completed, ACP stopReason, Pi agent_end) as `result_divider`, so
 * this is provider-agnostic.
 */
export function handleResultDivider(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  stores: AgentMessageStores,
  catchUpPhase: CatchUpPhase,
): void {
  const { agentSessionStore, view } = stores
  // Clear every live indicator on the turn-end divider itself, not just via the
  // per-message clear above. The divider is the structural turn boundary for every
  // provider; a clear that depended on message source or status would miss a FINAL envelope
  // whose source is not AGENT, or a catch-up replay where the INACTIVE-driven
  // cleanup is skipped. It is also the backstop for a tool whose result row never
  // arrived (an interrupt, a crashed CLI), whose badge would otherwise stay on that
  // card for the rest of the session.
  clearPerTurnLiveState(agentId, stores)
  // Resolve the context-window hint from the CONFIRMED catalog current value, not the
  // optimistic optionValues: a result divider is post-relaunch ground truth for a turn
  // that already ran, so a mid-switch optimistic value (the "default" sentinel, or a
  // not-yet-relaunched id) would mis-key the primary-model lookup. The confirmed
  // currentValue is the model the completed turn actually used.
  const plugin = providerFor(msg.agentProvider)
  const modelId = optionGroup(view.getAgentTab(agentId)?.optionGroups, OPTION_ID_MODEL)?.currentValue
  const meta = extractResultMetadata(parsed, modelId, p => plugin?.resultSubtype?.(p))
  if (!meta)
    return
  if (meta.subtype && catchUpPhase === 'live') {
    // No alert here. The Worker owns it, from its busy -> idle edge
    // (handleAgentSettled) -- a divider is a turn boundary, and a turn that
    // leaves a subagent running has not settled the agent.
  }
  if (meta.contextUsage) {
    agentSessionStore.updateInfo(agentId, { contextUsage: meta.contextUsage })
  }
  else if (meta.contextWindow !== undefined) {
    const existingUsage = agentSessionStore.getInfo(agentId).contextUsage
    if (existingUsage) {
      agentSessionStore.updateInfo(agentId, {
        contextUsage: { ...existingUsage, contextWindow: meta.contextWindow },
      })
    }
  }
  if (meta.totalCostUsd !== undefined) {
    agentSessionStore.updateInfo(agentId, { totalCostUsd: meta.totalCostUsd })
  }
}

/**
 * Drop a span's live tool progress once its RESULT row lands: the tool finished,
 * so its card must stop showing an elapsed time.
 *
 * The frontend owns this because the worker cannot see it. Claude Code emits a
 * heartbeat every 30 seconds while a tool runs and NOTHING when it stops -- the
 * CLI just clears its own timer -- so a provider has no end message to forward.
 *
 * The result row is identified by the plugin's existing `spanRole` hook rather
 * than by any provider's own envelope shape, so this stays provider-neutral.
 * It runs for a row OUTSIDE the loaded window too: an entry whose row was
 * trimmed still has to be reclaimed, and the badge it feeds is not rendered
 * there anyway. Turn end / agent-inactive / context-cleared clear whatever this
 * misses.
 */
export function dropFinishedToolProgress(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  chatStore: AgentMessageStores['chatStore'],
): void {
  if (!msg.spanId)
    return
  if (providerFor(msg.agentProvider)?.spanRole?.(parsed) === 'result')
    chatStore.dropToolProgress(agentId, msg.spanId)
}

/**
 * Drop live per-turn state after a lifecycle event or a lost connection.
 * The Worker normally sends explicit counter clears. This cleanup also handles
 * a connection that ends before those clears arrive.
 */
export function clearPerTurnLiveState(
  agentId: string,
  stores: Pick<AgentMessageStores, 'agentSessionStore' | 'chatStore'>,
): void {
  stores.agentSessionStore.clearThinkingTokens(agentId)
  stores.agentSessionStore.clearOutputBytes(agentId)
  stores.chatStore.clearToolProgress(agentId)
}

/**
 * Process one persisted `agentMessage` frame as a sequence of named steps: the ephemeral
 * session-info short-circuit, notification metadata, the windowed append,
 * background trim, tool-progress cleanup, and the turn-end result divider. Extracted from the
 * switch case so the pipeline matches the sibling extractions (handleAgentSessionInfo /
 * applyNotificationMetadata / handleResultDivider) instead of one case that dwarfs the rest.
 * The caller marks the agent live BEFORE this (that step is shared with the other cases).
 */
export function handleAgentMessage(
  agentId: string,
  msg: AgentChatMessage,
  stores: AgentMessageStores,
  catchUpPhase: CatchUpPhase,
): void {
  const { chatStore, view, selection } = stores

  // Single decompress-and-parse pass shared across the metadata, span-cleanup,
  // assistant-usage, and result-divider branches below. parseMessageContent never throws
  // — failures yield EMPTY_PARSED (topLevel null), which causes each branch to no-op cleanly.
  const parsed = parseMessageContent(msg)

  // Ephemeral agent_session_info: translated + applied, then short-circuit (it is
  // never persisted, so none of the message processing below applies).
  if (handleAgentSessionInfo(agentId, parsed, stores))
    return

  // Notification metadata (context_cleared / rate_limit / token-usage / compaction
  // / settings_changed / plan), independent of the persisted-message handling.
  applyNotificationMetadata(agentId, msg, parsed, stores, catchUpPhase)

  chatStore.addMessage(agentId, msg)
  // A tool's result row means it stopped running, so its badge goes with it.
  dropFinishedToolProgress(agentId, msg, parsed, chatStore)
  // Trim only tabs the user is NOT looking at. This must compare against THIS
  // agent's own key: `activeKeyForTile` returns whichever tab the tile has
  // active, so a bare truthiness test is true for any tile holding any tab —
  // including this one — and the cap would never apply to anything.
  if (
    selection.activeKeyForTile(view.getAgentTab(agentId)?.tileId ?? '')
    !== tabKey({ type: TabType.AGENT, id: agentId })
    && chatStore.getMessages(agentId).length > MAX_BACKGROUND_CHAT_MESSAGES
  ) {
    chatStore.trimOldestEnd(agentId, MAX_BACKGROUND_CHAT_MESSAGES)
  }
  // Classify once and reuse across the per-message gates below.
  const category = classifyAgentMessage(msg)

  // Play turn-end sound when a result divider (with subtype) arrives, and
  // rehydrate contextWindow / total_cost_usd. Each provider plugin classifies its
  // final envelope (Claude type:"result", Codex turn/completed, ACP stopReason,
  // Pi agent_end) as `result_divider`, so this gate is provider-agnostic.
  if (category.kind === 'result_divider')
    handleResultDivider(agentId, msg, parsed, stores, catchUpPhase)
}

/**
 * For each axis the agent is ACTIVELY changing (pendingAxes), keep the tab's
 * OPTIMISTIC optionValue rather than absorbing the server's (in-flight-stale) one;
 * every other axis takes the server value. A pending axis ABSENT from `prevValues`
 * is an in-flight CLEAR (useAgentOperations deletes a cleared key before marking the
 * axis pending), so it stays absent rather than re-absorbing the server value.
 * Returns `serverValues` unchanged (same reference) when nothing is pending, so the
 * caller's downstream ref-reuse check can short-circuit. Pure.
 */
export function applyPendingAxisSuppression(
  serverValues: Record<string, string>,
  prevValues: Record<string, string> | undefined,
  pendingAxes: ReadonlySet<string>,
): Record<string, string> {
  if (pendingAxes.size === 0 || !prevValues)
    return serverValues
  const merged: Record<string, string> = { ...serverValues }
  for (const axis of pendingAxes) {
    const optimistic = prevValues[axis]
    if (optimistic !== undefined)
      merged[axis] = optimistic
    else
      delete merged[axis]
  }
  return merged
}

/**
 * Reconcile a status push's option-group catalog into the per-axis tab fields,
 * preserving reference stability and the user's in-flight optimistic edits:
 *  - derive the catalog + current values from the reported groups (empty groups =
 *    "unchanged", so an empty push returns {} and leaves the existing fields intact);
 *  - reuse each unchanged group's previous reference (mergeStableOptionGroupRefs) so a
 *    re-broadcast of the full catalog doesn't churn the settings popover's rows;
 *  - keep the optimistic value for each pending axis (applyPendingAxisSuppression).
 *
 * Suppressing an equal-but-fresh optionValues is deliberately NOT done here:
 * `tabMetadata.patch` compares every object-valued field against what is stored
 * (see `sameStoredValue`) and drops the write, which covers this producer, the
 * other ones, and any written later. mergeStableOptionGroupRefs stays because it
 * is not that rule -- it stabilizes each ELEMENT inside a changed array, which a
 * whole-value compare at the write point cannot do.
 *
 * Pure: the label-cache priming side effect (updateSettingsLabelCache) stays at the
 * call site -- it is the data-ingestion boundary, not part of deriving the tab fields.
 */
export function resolveSettingsTabFields(
  prev: AgentTab | undefined,
  optionGroups: AvailableOptionGroup[],
  pendingAxes: ReadonlySet<string>,
): Partial<AgentTab> {
  if (optionGroups.length === 0)
    return {}
  const fields = deriveOptionGroupTabFields(optionGroups)
  // The worker re-broadcasts the full catalog on every status push, re-decoded into
  // fresh proto objects; reuse each unchanged group's prior reference (per group, so a
  // single changed group like effort doesn't churn the untouched model list either).
  if (fields.optionGroups && prev?.optionGroups)
    fields.optionGroups = mergeStableOptionGroupRefs(fields.optionGroups, prev.optionGroups)
  // The catalog (optionGroups) is never optimistic and always applies; the per-axis
  // current values keep the user's in-flight optimistic edits (see the helper).
  if (fields.optionValues)
    fields.optionValues = applyPendingAxisSuppression(fields.optionValues, prev?.optionValues, pendingAxes)
  return fields
}

/**
 * Assemble the single consolidated tab update for an agent statusChange: status +
 * session id (only when status is SET, so a git-only push can't overwrite valid state
 * with proto3's UNSPECIFIED default and make the agent unwatchable), the startupError /
 * startupMessage transitions, the already-reconciled per-axis settings fields, and the
 * git fields. Pure; the caller applies the whole set in ONE `metadata.patch` so a status
 * push is a single write (vs. the historical split that walked the tab list twice).
 *
 * Takes no pre-update tab, and none of the groups below compares against one. The
 * worker re-ships its whole payload on every push, so an unchanged field does arrive
 * as an equal-but-fresh object -- but suppressing that write is `tabMetadata.patch`'s
 * job now, at the single write point (see `sameStoredValue`), not each producer's.
 */
export function buildAgentStatusTabUpdate(
  sc: AgentStatusChange,
  hasStatus: boolean,
  settingsFields: Partial<AgentTab>,
): Partial<AgentTab> {
  return {
    ...(hasStatus ? { agentStatus: sc.status, agentSessionId: sc.agentSessionId } : {}),
    ...(hasStatus ? { supportsSteering: sc.supportsSteering } : {}),
    // Carry startupError alongside status transitions so the in-tab error view can
    // render the server-formatted message; only on the failed/cleared transitions, so
    // an unrelated status (e.g. INACTIVE from turn end) leaves it alone.
    ...(sc.status === AgentStatus.STARTUP_FAILED ? { startupError: sc.startupError } : {}),
    ...(sc.status === AgentStatus.ACTIVE ? { startupError: '' } : {}),
    // Carry startupMessage while STARTING so the startup panel shows the current phase;
    // clear it on any terminal transition; ignore status-less events (catch-up
    // sentinels, git-only updates) so an unrelated event doesn't wipe a live label.
    ...(sc.status === AgentStatus.STARTING
      ? { startupMessage: sc.startupMessage }
      : hasStatus ? { startupMessage: '' } : {}),
    // The reconciled catalog (never optimistic) + per-axis-suppressed current values.
    ...settingsFields,
    // Repo identity only on the tab; full git state lives in repoGitStore.
    ...(sc.gitStatus?.toplevel ? { gitToplevel: sc.gitStatus.toplevel } : {}),
  }
}

/**
 * INACTIVE cleanup: the agent subprocess stopped. Clear stale control requests (so the
 * user can send a regular message that auto-starts the agent instead of being stuck on
 * an unanswerable prompt) and clear the live per-turn state. A live event also signals
 * the turn end.
 */
export function handleAgentInactive(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: CatchUpPhase,
  // The shared message-stores bag plus the controlStore only this handler needs --
  // reuse AgentMessageStores rather than re-spelling its three members inline.
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
): void {
  stores.controlStore.clearAgent(agentId)
  clearPerTurnLiveState(agentId, stores)
  // No alert here: a process exit drives the Worker's busy state to false, and
  // handleAgentSettled owns every settle. See its doc comment.
  //
  // No git refresh here either. The refresh belongs to the TURN boundary, which
  // `onTurnEndRefresh` owns, and an INACTIVE status change is not one: the two
  // live broadcasters of this status are an archive teardown and a failed
  // startup relaunch, and both pass a nil gitStatus (see
  // broadcastAgentInactive). Neither ran the agent, so neither changed the
  // working tree.
}

/**
 * The `controlRequest` case: register a pending control prompt (permission / plan), and --
 * only on a LIVE frame -- badge a backgrounded tab and end the turn (the agent paused to
 * wait on the user, which may produce no agent message and no INACTIVE). During catch-up a
 * replayed request for an already-INACTIVE agent is skipped so the user isn't stuck on an
 * unanswerable prompt, and the live-only side effects are restricted so a page-reload replay of
 * a still-pending row doesn't re-alert. The caller marks the agent live BEFORE this.
 */
export function handleControlRequest(
  agentId: string,
  cr: AgentControlRequest,
  catchUpPhase: CatchUpPhase,
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
): void {
  const { view, metadata, selection, getActiveWorkspaceId, controlStore } = stores
  // During catch-up, the INACTIVE statusChange may have already been processed before
  // this replayed controlRequest arrives. Skip adding the request so the user isn't
  // stuck on an unanswerable prompt.
  const agentEntry = view.getAgentTab(cr.agentId)
  if (catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)
    return
  let payload: Record<string, unknown>
  try {
    const parsed = JSON.parse(TEXT_DECODER.decode(cr.payload)) as unknown
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      log.warn('Ignoring non-object control request payload', { agentId: cr.agentId, requestId: cr.requestId })
      return
    }
    payload = parsed as Record<string, unknown>
  }
  catch (err) {
    log.warn('Ignoring malformed control request payload', { agentId: cr.agentId, requestId: cr.requestId, err })
    return
  }
  controlStore.addRequest(cr.agentId, { requestId: cr.requestId, agentId: cr.agentId, payload, claimToken: cr.claimToken })
  if (catchUpPhase === 'live') {
    // Light up the tab badge so a user looking at a sibling tab knows the background
    // agent is now waiting on them. Match FULL's on-screen rule (tile-active).
    if (!isAgentTabOnScreen(cr.agentId, view, selection, getActiveWorkspaceId))
      metadata.patch(cr.agentId, { hasNotification: true })
    // No alert here. A pending control request drives the Worker's busy state to
    // false, so handleAgentSettled raises it from the one edge -- and raising it
    // in both places rang twice for one pause.
    //
    // No git / directory-tree refresh either, which this site used to trigger as
    // a side effect of the same call. That refresh now belongs to the TURN
    // boundary alone (onTurnEndRefresh): a prompt arrives mid-turn, so the tree
    // it would show is half-written, and the turn end that follows refreshes it
    // for real.
  }
}

/**
 * Whether this agent's tab is the one the user is looking at: it exists, it is
 * in the active workspace, and it is the selected tab of its tile.
 *
 * "On screen" is a property of the TILE's selection, not of the tab alone --
 * a tab in a background tile is mounted and invisible.
 */
export function isAgentTabOnScreen(
  agentId: string,
  view: Pick<TabView, 'getAgentTab'>,
  selection: TabSelectionStore,
  getActiveWorkspaceId: () => string | null,
): boolean {
  return isTabOnScreen(view.getAgentTab(agentId), getActiveWorkspaceId(), tileId => selection.activeKeyForTile(tileId))
}

/**
 * The AgentActivityChanged branch: store the Worker's answer, and alert on the
 * edge.
 *
 * One function so the store write and the alert cannot separate. The store
 * answers whether this write was the busy -> idle EDGE, which is not the same
 * as an idle report arriving: the same value reaches a client twice when a
 * catch-up replay lands beside a live event, and an idle report can arrive for
 * an agent this client never saw working. See AgentActivityStore.apply.
 *
 * Every AgentActivityChanged is a TRANSITION, so this runs in every catch-up
 * phase. A settle that lands while the tab replays is a live settle -- the agent
 * finished while the burst drained -- and it must ring. The catch-up BASELINE is
 * a level and arrives on CatchUpStart instead; AgentActivityStore.seedPublished
 * takes it.
 */
export function handleActivityChanged(
  agentId: string,
  value: { state: AgentActivityState, numToolUses?: number },
  stores: Pick<AgentMessageStores, 'metadata' | 'selection' | 'getActiveWorkspaceId' | 'view'> & {
    agentActivityStore: AgentActivityStore
    onAgentSettled?: (agentId: string, numToolUses?: number) => void
  },
): void {
  if (stores.agentActivityStore.apply(agentId, value.state))
    handleAgentSettled(agentId, value.numToolUses, stores)
}

/**
 * The agent settled: badge an off-screen tab and play the turn-end sound.
 *
 * Driven by the WORKER's busy -> idle edge, not by a turn end. Those differ, and
 * the difference is the point: a turn that spawns a subagent ends while the
 * subagent keeps working, and ringing there told the user their agent was done
 * while it was still running. A settle means the turn ended AND no background
 * task of this agent's is still going.
 *
 * This one edge also subsumes the two alerts that used to be raised separately.
 * A pending control request drives busy to false, so a permission prompt still
 * rings; a process exit drives it to false, so a crash still rings.
 *
 * `numToolUses` is the count of the turn this settle completes, when a turn
 * completed it and the provider reported one. Unset means "ring" -- a settle
 * caused by a prompt or an exit carries no count, and neither does a provider
 * that cannot report one. Explicit 0 means the turn did nothing worth
 * interrupting the user for.
 *
 * Runs in every catch-up phase, because its one caller only reaches it on a
 * transition. The phase test that used to stand here dropped a settle that
 * merely RACED a replay, such as a background task ending while the
 * burst drained. That settle is the one the user waits for. The baseline the phase test
 * existed to silence no longer arrives as a transition at all: it rides
 * CatchUpStart, and AgentActivityStore.seedPublished raises nothing for it.
 */
export function handleAgentSettled(
  agentId: string,
  numToolUses: number | undefined,
  stores: Pick<AgentMessageStores, 'metadata' | 'selection' | 'getActiveWorkspaceId' | 'view'> & {
    onAgentSettled?: (agentId: string, numToolUses?: number) => void
  },
): void {
  const { metadata, selection, getActiveWorkspaceId, view } = stores
  if (!view.getAgentTab(agentId))
    return
  if (!isAgentTabOnScreen(agentId, view, selection, getActiveWorkspaceId))
    metadata.patch(agentId, { hasNotification: true })
  stores.onAgentSettled?.(agentId, numToolUses)
}

/**
 * The `statusChange` case: apply a worker status snapshot to the agent tab. Skips a
 * payload-less catch-up sentinel; otherwise reconciles the reported option-group
 * catalog into the tab (with per-axis optimistic suppression), consolidates every field
 * into ONE metadata patch, stops the aggregate settings spinner when nothing's pending, and
 * runs the INACTIVE turn-end cleanup. The worker-online flag is authoritative only on a
 * full status snapshot. Orchestration over the already-extracted pure helpers
 * (resolveSettingsTabFields / buildAgentStatusTabUpdate / handleAgentInactive);
 * `setWorkerOnline` is the hook's signal setter.
 *
 * A STARTING->ACTIVE transition no longer drains anything here. The Worker owns the
 * durable input queue, so it dispatches a queued input itself once the agent runs.
 */
export function handleAgentStatusChange(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: CatchUpPhase,
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore>, repoGitStore: ReturnType<typeof createRepoGitStore> },
  settingsLoading: ReturnType<typeof createLoadingSignal>,
  setWorkerOnline: (online: boolean) => void,
  streamWorkerId = '',
): void {
  const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
  // `workerOnline` is only authoritative on full status snapshots. Status-less partial
  // updates may carry proto3's default `false` from older backends or sparse producers.
  if (hasStatus)
    setWorkerOnline(sc.workerOnline)

  // Skip events that carry no status, git, or settings payload -- they only surface as
  // catch-up sentinels (the forward-fill they used to drive now runs from the continuous
  // reconcileLaggingTails effect) and would otherwise allocate a full updates object and
  // iterate every reactive reader for a no-op.
  const hasPayload = hasStatus || sc.gitStatus !== undefined || sc.optionGroups.length > 0
  if (!hasPayload)
    return

  // Whether THIS agent has any settings change in flight -- gates only the aggregate
  // spinner stop below; the optimistic-value suppression is per-AXIS (pendingAxes).
  const pendingSettings = settingsLoading.isPending(sc.agentId)
  applyAgentStatusTabUpdate(sc, stores, settingsLoading, streamWorkerId)
  if (!pendingSettings)
    settingsLoading.stop()
  if (sc.status === AgentStatus.INACTIVE)
    handleAgentInactive(agentId, sc, catchUpPhase, stores)
}

/**
 * Everything a status push writes onto an agent's tab row.
 */
function applyAgentStatusTabUpdate(
  sc: AgentStatusChange,
  stores: Pick<AgentMessageStores, 'chatStore' | 'view' | 'metadata'> & { repoGitStore: ReturnType<typeof createRepoGitStore> },
  settingsLoading: ReturnType<typeof createLoadingSignal>,
  streamWorkerId = '',
): void {
  const { view, metadata, repoGitStore } = stores
  const prev = view.getAgentTab(sc.agentId)
  if (sc.optionGroups.length > 0)
    updateSettingsLabelCache(sc.agentProvider, sc.optionGroups)
  const workerId = prev?.workerId || streamWorkerId || ''
  upsertRepoGitFromProtoStatus(repoGitStore, workerId, sc.gitStatus, {
    migrateErrorHintFrom: prev
      ? migrateErrorHintFromForResolvedRepo(workerId, prev, sc.gitStatus)
      : undefined,
  })
  const settingsFields = resolveSettingsTabFields(prev, sc.optionGroups, settingsLoading.pendingAxes(sc.agentId))
  // Consolidate every per-status field into one patch so the row is written once.
  metadata.patch(sc.agentId, buildAgentStatusTabUpdate(sc, sc.status !== AgentStatus.UNSPECIFIED, settingsFields))
}
