/**
 * The agent-event pipeline, as module-level units.
 *
 * Every handler here takes its stores as an explicit deps bag rather than
 * closing over the connection hook -- which is what makes the arms of
 * `agentMessage` independently testable, and what let this move out of a
 * 1700-line module without changing a line of behaviour.
 */
import type { Provider } from '~/components/chat/providers/registry'
import type { AgentChatMessage, AgentControlRequest, AgentStatusChange, AgentStreamChunk, AgentStreamEnd, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { createAgentSessionStore, RateLimitInfo } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { AgentTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { sendAgentMessage } from '~/api/workerRpc'
import { classifyAgentMessage, shouldClearStreamingText } from '~/components/chat/messageClassification'
import { pluginFor, providerFor } from '~/components/chat/providers/registry'
import { mergeStableOptionGroupRefs, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { AgentStatus, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { isTabOnScreen } from '~/hooks/watchPlan'
import { createLogger } from '~/lib/logger'
import { extractCompactionContextTokens, extractContextUsage, extractPlanFilePath, extractPlanUpdated, extractResultMetadata, extractSettingsChanges, getInnerMessage, normalizeContextUsage, parseMessageContent } from '~/lib/messageParser'
import { emitSettingsChanged } from '~/lib/settingsChangedEvent'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { compactionContextUsage } from '~/stores/agentSession.store'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { protoToRepoGitPatch, repoKeyFromStatus } from '~/stores/repoGit'
import { deriveOptionGroupTabFields, tabKey } from '~/stores/tab.helpers'

const log = createLogger('agentEvents')

/** Shared across the stream-chunk and control-request decoders. */
const TEXT_DECODER = new TextDecoder()

/**
 * Translate a snake_case `rate_limits` broadcast payload to the camelCase
 * `RateLimitInfo` shape that the agent-session store and rate-limit utils
 * consume. The wire format is provider-agnostic snake_case (Claude/Codex
 * both emit it that way); the frontend keeps idiomatic camelCase types.
 */
function wireRateLimitsToCamel(value: unknown): Record<string, RateLimitInfo> | undefined {
  if (typeof value !== 'object' || value === null)
    return undefined
  const out: Record<string, RateLimitInfo> = {}
  for (const [key, raw] of Object.entries(value as Record<string, unknown>)) {
    if (typeof raw !== 'object' || raw === null)
      continue
    const tier = raw as Record<string, unknown>
    const info: RateLimitInfo = {}
    if (typeof tier.rate_limit_type === 'string')
      info.rateLimitType = tier.rate_limit_type
    if (typeof tier.status === 'string')
      info.status = tier.status
    if (typeof tier.utilization === 'number')
      info.utilization = tier.utilization
    if (typeof tier.resets_at === 'number')
      info.resetsAt = tier.resets_at
    if (typeof tier.surpassed_threshold === 'number')
      info.surpassedThreshold = tier.surpassed_threshold
    if (typeof tier.overage_status === 'string')
      info.overageStatus = tier.overage_status
    if (typeof tier.overage_resets_at === 'number')
      info.overageResetsAt = tier.overage_resets_at
    if (typeof tier.is_using_overage === 'boolean')
      info.isUsingOverage = tier.is_using_overage
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
  if (typeof info.total_cost_usd === 'number')
    updates.totalCostUsd = info.total_cost_usd
  const contextUsage = normalizeContextUsage(info.context_usage)
  if (contextUsage)
    updates.contextUsage = contextUsage
  if (info.rate_limits !== undefined)
    updates.rateLimits = wireRateLimitsToCamel(info.rate_limits)
  if (info.codex_turn_id !== undefined)
    updates.codexTurnId = info.codex_turn_id as string
  if (info.streaming_type !== undefined)
    updates.streamingType = info.streaming_type as string
  // Only positive estimates: `> 0` rejects both the zero-estimate first delta
  // (nothing to show yet) and a NaN a future provider might emit (NaN > 0 is
  // false), so the indicator never has to defend against "0 tokens" or a NaN
  // serialized to null in storage.
  if (typeof info.thinking_tokens === 'number' && info.thinking_tokens > 0)
    updates.thinkingTokens = info.thinking_tokens
  return updates
}

// shouldClearThinkingTokensForMessage decides whether a persisted message should
// drop the live thinking-token estimate. Non-AGENT entries (user echoes such as
// queued input or tool_result, and LeapMux notifications) can land mid-think and
// must never clear a climbing counter, so they are rejected here universally. For
// AGENT messages the per-provider policy is delegated to the provider plugin's
// clearsThinkingTokensForMessage hook; the default (no hook) is "main-scope only"
// -- clear when parentSpanId === '' -- so a collab subagent's nested commit does
// not reset the primary counter (Claude overrides to always clear). The resolved
// plugin is passed in so this stays a pure, registry-free unit.
export function shouldClearThinkingTokensForMessage(
  msg: { source: MessageSource, parentSpanId: string },
  plugin: Pick<Provider, 'clearsThinkingTokensForMessage'> | undefined,
): boolean {
  if (msg.source !== MessageSource.AGENT)
    return false
  if (plugin?.clearsThinkingTokensForMessage)
    return plugin.clearsThinkingTokensForMessage(msg)
  return msg.parentSpanId === ''
}

/**
 * The hook-scoped stores the agentMessage sub-handlers below write to. Passed
 * explicitly so each handler is a module-level unit (no closure over the hook), which
 * is what makes the three concerns of the agentMessage arm independently testable.
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
 * the agentMessage arm breaks before the persisted-message processing below.
 */
export function handleAgentSessionInfo(
  agentId: string,
  parsed: ParsedMessageContent,
  agentSessionStore: AgentMessageStores['agentSessionStore'],
): boolean {
  if (!(parsed.topLevel !== null && !parsed.wrapper && parsed.topLevel.type === 'agent_session_info'))
    return false
  const info = parsed.topLevel.info as Record<string, unknown> | undefined
  const updates = wireSessionInfoToUpdates(info)
  // A zero (or, defensively, negative) thinking-token estimate is the backend's
  // per-phase reset signal -- the first delta of a thinking phase reports 0. Honor it
  // as a clear so a stale count from a prior phase/turn can't linger; the positive path
  // keeps streaming via `updates`. wireSessionInfoToUpdates only forwards positive
  // estimates, so a 0 never arrives as an update and must be handled here.
  if (typeof info?.thinking_tokens === 'number' && info.thinking_tokens <= 0)
    agentSessionStore.clearThinkingTokens(agentId)
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
 * Every imperative side effect in this module is gated on it: replaying a
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

  if (innerType === 'context_cleared') {
    agentSessionStore.clearContextUsage(agentId)
    chatStore.todos.clear(agentId)
    // The conversation was wiped; drop any in-flight thinking-token estimate too. The
    // backend resets its own estimator on a context clear, but that reset is in-memory
    // only (no broadcast), so the counter would otherwise linger frozen on its last
    // value until the next turn produces a delta or a clear of its own.
    agentSessionStore.clearThinkingTokens(agentId)
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

  if (innerType === 'settings_changed') {
    const sc = extractSettingsChanges(parsed)
    if (sc)
      emitSettingsChanged(sc)
  }

  // plan_execution / plan_updated may also appear inside a notification wrapper that
  // holds multiple message types, so wrapper messages always run the walk; non-wrapper
  // messages gate on the inner type to skip the call entirely.
  if (parsed.wrapper !== null || innerType === 'plan_execution') {
    const planFile = extractPlanFilePath(parsed)
    if (planFile)
      agentSessionStore.updateInfo(agentId, { planFilePath: planFile })
  }
  if (parsed.wrapper !== null || innerType === 'plan_updated') {
    const planUpdate = extractPlanUpdated(parsed)
    if (planUpdate) {
      if (planUpdate.planFilePath)
        agentSessionStore.updateInfo(agentId, { planFilePath: planUpdate.planFilePath })
      // Live only. The tab title is USER-EDITABLE, and this is the one branch
      // here that writes over a user's own choice: replaying history on reload
      // re-applied the plan's title and silently undid a manual rename. Every
      // other side effect in this function is derived state that catch-up
      // should restore, which is why only this one is gated. (planFilePath
      // above is derived, so it still restores.)
      if (catchUpPhase === 'live' && planUpdate.updateAgentTitle && planUpdate.planTitle)
        metadata.patch(agentId, { title: planUpdate.planTitle })
    }
  }
}

/**
 * Handle a turn-end result divider (the caller gates on category.kind ===
 * 'result_divider'). Clears the per-turn thinking-token estimate and rehydrates
 * contextWindow / total_cost_usd. Turn-end sound and tab badging are owned by
 * the worker's AgentTurnEnd event (`handleTurnEnd`), not this divider — leaving
 * both would ring a visible tab twice.
 *
 * Each provider plugin classifies its terminal envelope (Claude type:"result",
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
  const { agentSessionStore, chatStore, view } = stores
  // Clear the per-turn thinking-token estimate on the turn-end divider itself, not just
  // via the AGENT-message clear above. The divider is the structural turn boundary for
  // every provider; gating the clear on message source/status would miss a terminal
  // envelope whose source is not AGENT, or a catch-up replay where the INACTIVE-driven
  // onTurnEnd is skipped -- leaving the counter frozen on its last value.
  agentSessionStore.clearThinkingTokens(agentId)
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
  // A persisted turn-end result divider clears the provider's tracked live turn-id
  // (only Codex tracks one), so the thinking indicator stops after a reconnect or
  // missed live event. The provider plugin owns WHICH subtype ends a turn; the hook
  // owns the action (clearing the session-info field).
  if (plugin?.resultDividerEndsActiveTurn?.(meta.subtype)) {
    agentSessionStore.updateInfo(agentId, { codexTurnId: '' })
  }
  if (meta.subtype && catchUpPhase === 'live') {
    // Turn-end sound is owned by AgentTurnEnd (worker NOTIFY event); do not
    // also ring here or a visible tab dings twice.
    chatStore.sweepOrphanedBufferedSpans(agentId)
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
 * Reclaim a span's buffered command stream once its persisted row reports the span
 * COMPLETED: a finished commandExecution/fileChange, or a reasoning block that now
 * carries summary/content. The persisted row supersedes the in-flight stream, so its
 * buffered segments are no longer needed. No-op for a non-span row, a non-AGENT
 * source, or a still-in-progress span (its stream stays live).
 */
export function clearCompletedSpanStream(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  chatStore: AgentMessageStores['chatStore'],
): void {
  // The neutral gate is just "an AGENT-source span row"; the provider plugin owns whether the row's
  // item shape marks the span COMPLETED (Codex: a commandExecution/fileChange with completed status,
  // or a reasoning item that now carries summary/content). Delegating the span-type vocabulary to the
  // hook -- rather than duplicating Codex's commandExecution/fileChange/reasoning names here -- means a
  // future provider that gains command streams needs only its own commandSpanSuperseded, not an edit
  // to a shared allowlist that would silently skip it. commandSpanSuperseded already returns true only
  // for those item shapes, so the removed allowlist was redundant with the hook's own check.
  if (msg.spanId && msg.source === MessageSource.AGENT) {
    if (providerFor(msg.agentProvider)?.commandSpanSuperseded?.(parsed))
      chatStore.clearCommandStream(agentId, msg.spanId)
  }
}

/**
 * Method-specific lifecycle handling for a persisted message. Gated on AGENT source rather than
 * category because some lifecycle items (e.g. Codex `thread/started`) classify as `hidden` -- a
 * category-only gate would silently skip them. Clears a stale Codex turn id on thread/started and
 * dismisses the plan streaming UI on a plan item (the general streaming-clear already dropped the
 * text buffer). Usage/cost extraction is NOT here -- it runs once for every message in
 * applyNotificationMetadata (extractContextUsage), the single home for session usage metadata.
 */
export function applyAgentLifecycle(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  agentSessionStore: AgentMessageStores['agentSessionStore'],
): void {
  if (msg.source !== MessageSource.AGENT)
    return
  // The provider plugin owns the lifecycle frames (Codex clears its live turn id on thread/started
  // and the plan streaming indicator on a plan item); the service just applies the returned patch.
  const lifecyclePatch = providerFor(msg.agentProvider)?.lifecycleSessionInfo?.(parsed)
  if (lifecyclePatch)
    agentSessionStore.updateInfo(agentId, lifecyclePatch)
}

/**
 * Process one persisted `agentMessage` frame as a sequence of named steps: the ephemeral
 * session-info short-circuit, notification metadata, the windowed append + thinking-token
 * / streaming-text clears + background trim, the completed-span stream reclaim, the
 * method-specific lifecycle, and the turn-end result divider. Extracted from the
 * switch arm so the pipeline matches the sibling extractions (handleAgentSessionInfo /
 * applyNotificationMetadata / handleResultDivider) instead of one arm dwarfing the rest.
 * The caller marks the agent live BEFORE this (that step is shared with the other arms).
 */
export function handleAgentMessage(
  agentId: string,
  msg: AgentChatMessage,
  stores: AgentMessageStores,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
  catchUpPhase: CatchUpPhase,
): void {
  const { agentSessionStore, chatStore, view, selection } = stores

  // Single decompress-and-parse pass shared across the metadata, span-cleanup,
  // assistant-usage, and result-divider branches below. parseMessageContent never throws
  // — failures yield EMPTY_PARSED (topLevel null), which causes each branch to no-op cleanly.
  const parsed = parseMessageContent(msg)

  // Ephemeral agent_session_info: translated + applied, then short-circuit (it is
  // never persisted, so none of the message processing below applies).
  if (handleAgentSessionInfo(agentId, parsed, agentSessionStore))
    return

  // Notification metadata (context_cleared / rate_limit / token-usage / compaction
  // / settings_changed / plan), independent of the persisted-message handling.
  applyNotificationMetadata(agentId, msg, parsed, stores, catchUpPhase)

  const messageInWindow = chatStore.addMessage(agentId, msg)
  // Main-agent output means the current thinking phase produced something,
  // so drop the live thinking-token estimate — otherwise the counter lingers
  // beside the indicator (frozen on its last value) until turn end, and the
  // next thinking phase would briefly flash the stale total before its own
  // deltas arrive. No-op when no estimate is set.
  //
  // INTENTIONAL per-phase reset: this also fires on an intermediate persisted
  // reasoning block (Claude `assistant_thinking`) during interleaved thinking
  // (think -> tool -> think), so the counter restarts from each new phase's
  // first delta rather than accumulating across a whole turn. That per-phase
  // semantics is the desired behavior — do not "fix" it to only clear at true
  // turn boundaries. See shouldClearThinkingTokensForMessage for the
  // source/subagent/Claude gating rationale.
  if (shouldClearThinkingTokensForMessage(msg, providerFor(msg.agentProvider)))
    agentSessionStore.clearThinkingTokens(agentId)
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

  // Any persisted assistant text, tool use/result, thinking block,
  // or turn-end divider ends the in-flight streaming text. The
  // streamed deltas have either been promoted to a persisted text
  // block (Codex agentMessage, Pi message_end, ACP text) or the
  // agent has transitioned to a tool/span message that implicitly
  // closes the prior text block — without clearing here,
  // subsequent text deltas concatenate onto the previous block
  // into one wall of text. Notification-thread rows and meta
  // categories never close the streaming buffer.
  if (shouldClearStreamingText(msg, parsed, category))
    chatStore.streamingText.clear(agentId)

  // A completed span's persisted row supersedes its in-flight command stream;
  // reclaim the buffered segments (no-op while the span is still in progress).
  if (messageInWindow)
    clearCompletedSpanStream(agentId, msg, parsed, chatStore)

  // Method-specific lifecycle handling (self-gated on AGENT source so a lifecycle item that
  // classifies as `hidden` isn't skipped). Usage/cost already folded in applyNotificationMetadata.
  applyAgentLifecycle(agentId, msg, parsed, agentSessionStore)

  // Play turn-end sound when a result divider (with subtype) arrives, and
  // rehydrate contextWindow / total_cost_usd. Each provider plugin classifies its
  // terminal envelope (Claude type:"result", Codex turn/completed, ACP stopReason,
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
 * Drain the per-agent pending-outbound queue on a STARTING -> ACTIVE / STARTUP_FAILED
 * transition. Messages composed while the subprocess was still starting were queued
 * (chatStore.pendingOutbound); on ACTIVE they are sent in order (a send failure surfaces
 * a per-message "Failed to deliver"), on STARTUP_FAILED every queued message surfaces an
 * "Agent failed to start" error. A no-op unless the PRIOR status was STARTING and the
 * queue is non-empty. `prev` is the pre-update tab (its status + worker id).
 */
export function drainPendingOutboundOnStart(
  sc: AgentStatusChange,
  prev: AgentTab | undefined,
  chatStore: ReturnType<typeof createChatStore>,
): void {
  if (prev?.agentStatus !== AgentStatus.STARTING)
    return
  // Pure status -> action dispatch; the store owns the queue drain, the per-message
  // pending-label/error side-state, and the fire-and-forget send loop (with the
  // transport injected here so the store stays I/O-free).
  if (sc.status === AgentStatus.ACTIVE) {
    const wid = prev.workerId ?? ''
    chatStore.resendPendingOutbound(sc.agentId, m =>
      sendAgentMessage(wid, { agentId: sc.agentId, content: m.content, attachments: m.attachments }))
  }
  else if (sc.status === AgentStatus.STARTUP_FAILED) {
    chatStore.failPendingOutbound(sc.agentId, 'Agent failed to start')
  }
}

/**
 * INACTIVE cleanup: the agent subprocess stopped. Clear stale control requests (so the
 * user can send a regular message that auto-starts the agent instead of being stuck on
 * an unanswerable prompt) and the per-turn thinking estimate. While LIVE, the turn is
 * definitively over -- reclaim any command-stream buffer a mid-stream trim spared as
 * orphaned (an agent that exits mid-turn emits INACTIVE but no result divider, so the
 * divider's turn-end sweep never fires for it, leaking the buffer) and signal turn-end.
 * Both 'live'-gated like the result-divider sweep; the catch-up phase is reclaimed by
 * the catchUpComplete sweep instead.
 */
export function handleAgentInactive(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: CatchUpPhase,
  // The shared message-stores bag plus the controlStore only this handler needs --
  // reuse AgentMessageStores rather than re-spelling its three members inline.
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
  onTurnEnd: ((agentId: string) => void) | undefined,
): void {
  stores.controlStore.clearAgent(agentId)
  stores.agentSessionStore.clearThinkingTokens(agentId)
  if (catchUpPhase === 'live')
    stores.chatStore.sweepOrphanedBufferedSpans(agentId)
  if (catchUpPhase === 'live' && sc.agentSessionId && stores.view.getAgentTab(agentId))
    onTurnEnd?.(agentId)
}

/**
 * The `streamChunk` arm of handleAgentEvent: route a streaming-text delta to its
 * command-stream buffer (when it carries a spanId) or the agent's free-form streaming
 * text. Extracted as a module-level handler -- with the sibling handlers below -- so the
 * dispatcher reads as a routing table and each arm is independently unit-testable (the
 * dispatcher closure itself is driven only by gRPC streams). The caller marks the agent
 * live BEFORE this (mirrors the other live arms).
 */
export function handleStreamChunk(agentId: string, value: AgentStreamChunk, chatStore: ReturnType<typeof createChatStore>): void {
  const text = TEXT_DECODER.decode(value.delta)
  if (value.spanId) {
    // The provider plugin maps its delta method to a segment kind (Codex `item/...` methods);
    // unknown methods default to plain output. Dispatch on the chunk's OWN authoritative provider
    // (the backend stamps AgentStreamChunk.agentProvider on every chunk) -- never a tab lookup,
    // which can still be undefined while a tab is bare from the reconciler, silently degrading
    // every Codex delta to `output`.
    const segmentKind = pluginFor(value.agentProvider)?.commandStreamSegmentKind?.(value.method) ?? 'output'
    chatStore.appendCommandStream(agentId, value.spanId, segmentKind, text)
  }
  else {
    chatStore.streamingText.set(agentId, chatStore.streamingText.get(agentId) + text)
  }
}

/**
 * The `streamEnd` arm: close the streaming buffer (command stream or free-form
 * text). Tab badging for a finished turn is owned by `handleTurnEnd`.
 */
export function handleStreamEnd(agentId: string, value: AgentStreamEnd, stores: Pick<AgentMessageStores, 'chatStore'>): void {
  const { chatStore } = stores
  if (value.spanId)
    chatStore.clearCommandStream(agentId, value.spanId)
  else
    chatStore.streamingText.clear(agentId)
}

/**
 * The `controlRequest` arm: register a pending control prompt (permission / plan), and --
 * only on a LIVE frame -- badge a backgrounded tab and end the turn (the agent paused to
 * wait on the user, which may produce no agent message and no INACTIVE). During catch-up a
 * replayed request for an already-INACTIVE agent is skipped so the user isn't stuck on an
 * unanswerable prompt, and the live-only side effects are gated so a page-reload replay of
 * a still-pending row doesn't re-alert. The caller marks the agent live BEFORE this.
 */
export function handleControlRequest(
  agentId: string,
  cr: AgentControlRequest,
  catchUpPhase: CatchUpPhase,
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
): void {
  const { view, metadata, selection, getActiveWorkspaceId, controlStore, agentSessionStore } = stores
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
    // The agent paused mid-turn to wait on the user; it is no longer thinking, and this
    // pause may produce no agent message and no INACTIVE, so drop the per-turn estimate
    // here too -- otherwise the counter lingers frozen until the next turn.
    agentSessionStore.clearThinkingTokens(agentId)
    onTurnEnd?.(agentId)
  }
}

/**
 * AgentTurnEnd NOTIFY event: badge when the tab is not on-screen (tile-active),
 * then play the turn-end sound (unless numToolUses is explicitly zero).
 */
export function isAgentTabOnScreen(
  agentId: string,
  view: Pick<TabView, 'getAgentTab'>,
  selection: TabSelectionStore,
  getActiveWorkspaceId: () => string | null,
): boolean {
  return isTabOnScreen(view.getAgentTab(agentId), getActiveWorkspaceId(), tileId => selection.activeKeyForTile(tileId))
}

export function handleTurnEnd(
  agentId: string,
  value: { numToolUses?: number },
  stores: Pick<AgentMessageStores, 'metadata' | 'selection' | 'getActiveWorkspaceId' | 'view'>,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
): void {
  const { metadata, selection, getActiveWorkspaceId, view } = stores
  if (!isAgentTabOnScreen(agentId, view, selection, getActiveWorkspaceId))
    metadata.patch(agentId, { hasNotification: true })
  const uses = value.numToolUses
  onTurnEnd?.(agentId, uses === undefined ? undefined : uses)
}

/**
 * The `statusChange` arm: apply a worker status snapshot to the agent tab. Skips a
 * payload-less catch-up sentinel; otherwise drains the pending-outbound queue on a
 * STARTING->ACTIVE/STARTUP_FAILED transition, reconciles the reported option-group
 * catalog into the tab (with per-axis optimistic suppression), consolidates every field
 * into ONE metadata patch, stops the aggregate settings spinner when nothing's pending, and
 * runs the INACTIVE turn-end cleanup. The worker-online flag is authoritative only on a
 * full status snapshot. Orchestration over the already-extracted pure helpers
 * (drainPendingOutboundOnStart / resolveSettingsTabFields / buildAgentStatusTabUpdate /
 * handleAgentInactive); `setWorkerOnline` is the hook's signal setter.
 */
export function handleAgentStatusChange(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: CatchUpPhase,
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore>, repoGitStore: ReturnType<typeof createRepoGitStore> },
  settingsLoading: ReturnType<typeof createLoadingSignal>,
  setWorkerOnline: (online: boolean) => void,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
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
  applyAgentStatusTabUpdate(sc, stores, settingsLoading)
  if (!pendingSettings)
    settingsLoading.stop()
  if (sc.status === AgentStatus.INACTIVE)
    handleAgentInactive(agentId, sc, catchUpPhase, stores, onTurnEnd)
}

/**
 * Everything a status push writes onto an agent's tab row.
 */
function applyAgentStatusTabUpdate(
  sc: AgentStatusChange,
  stores: Pick<AgentMessageStores, 'chatStore' | 'view' | 'metadata'> & { repoGitStore: ReturnType<typeof createRepoGitStore> },
  settingsLoading: ReturnType<typeof createLoadingSignal>,
): void {
  const { chatStore, view, metadata, repoGitStore } = stores
  const prev = view.getAgentTab(sc.agentId)
  drainPendingOutboundOnStart(sc, prev, chatStore)
  if (sc.optionGroups.length > 0)
    updateSettingsLabelCache(sc.agentProvider, sc.optionGroups)
  const workerId = prev?.workerId ?? ''
  const patch = protoToRepoGitPatch(workerId, sc.gitStatus)
  const key = workerId ? repoKeyFromStatus(workerId, sc.gitStatus) : undefined
  if (patch && key)
    repoGitStore.upsert(key, patch)
  const settingsFields = resolveSettingsTabFields(prev, sc.optionGroups, settingsLoading.pendingAxes(sc.agentId))
  // Consolidate every per-status field into one patch so the row is written once.
  metadata.patch(sc.agentId, buildAgentStatusTabUpdate(sc, sc.status !== AgentStatus.UNSPECIFIED, settingsFields))
}
