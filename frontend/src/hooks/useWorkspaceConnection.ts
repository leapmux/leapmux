import type { Provider } from '~/components/chat/providers/registry'
import type { AgentChatMessage, AgentControlRequest, AgentStatusChange, AgentStreamChunk, AgentStreamEnd, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { AgentEvent, TerminalEvent, WatchAgentEntry } from '~/generated/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { createAgentSessionStore, RateLimitInfo } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createTabStore } from '~/stores/tab.store'
import type { AgentTab, Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { batch, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { sendAgentMessage, watchEventsViaChannel } from '~/api/workerRpc'
import { classifyAgentMessage, shouldClearStreamingText } from '~/components/chat/messageClassification'
import { providerFor } from '~/components/chat/providers/registry'
import { mergeStableOptionGroupRefs, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { showWarnToast } from '~/components/common/Toast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus, MessageSource, WatchReplayMode } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { waitForStreamCompletion } from '~/hooks/streamCompletion'
import { ChannelError } from '~/lib/channel'
import { createLogger } from '~/lib/logger'
import { extractAssistantUsage, extractCodexTokenUsage, extractCompactionContextTokens, extractPlanFilePath, extractPlanUpdated, extractRateLimitInfo, extractResultMetadata, extractSettingsChanges, getInnerMessage, normalizeContextUsage, parseMessageContent } from '~/lib/messageParser'
import { CODEX_RATE_LIMITS_METHOD } from '~/lib/rateLimitUtils'
import { createExponentialBackoff } from '~/lib/retry'
import { emitSettingsChanged } from '~/lib/settingsChangedEvent'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { shallowEqual } from '~/lib/shallowEqual'
import { applyTerminalData, bufferHasVisibleContent } from '~/lib/terminal'
import { compactionContextUsage } from '~/stores/agentSession.store'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { deriveOptionGroupTabFields, gitTabFieldsDiffer, spliceTabGitFields, tabKey, toGitTabFields } from '~/stores/tab.helpers'
import { isAgentTab, isTerminalTab } from '~/stores/tab.types'

const log = createLogger('workspace')
const TEXT_DECODER = new TextDecoder()

/**
 * Build a WatchEvents agent entry from a resume cursor. A resume seq of 0n means
 * nothing has been observed yet, so subscribe fresh (LATEST: replay the most
 * recent page); a positive seq resumes AFTER_CURSOR from it. Mirrors the worker
 * and CLI `AgentWatchEntry` mapping so the wire request is explicit -- no 0/sign
 * overload disambiguating fresh from resume.
 */
export function agentWatchEntry(agentId: string, resumeSeq: bigint): WatchAgentEntry {
  return (resumeSeq > 0n
    ? { agentId, replay: WatchReplayMode.AFTER_CURSOR, cursorSeq: resumeSeq }
    : { agentId, replay: WatchReplayMode.LATEST, cursorSeq: BigInt(0) }) as WatchAgentEntry
}

export function buildWatchTargetsKey(
  workerId: string,
  agentEntries: readonly WatchAgentEntry[],
  terminalIds: readonly string[],
  nonActiveAgentIds: ReadonlySet<string>,
  nonActiveTerminalIds: ReadonlySet<string>,
): string {
  if (!workerId)
    return ''
  const activeAgentIds = agentEntries
    .map(e => e.agentId)
    .filter(id => !nonActiveAgentIds.has(id))
    .toSorted()
  const passiveAgentIds = agentEntries
    .map(e => e.agentId)
    .filter(id => nonActiveAgentIds.has(id))
    .toSorted()
  const activeTerminalIds = terminalIds
    .filter(id => !nonActiveTerminalIds.has(id))
    .toSorted()
  const passiveTerminalIds = terminalIds
    .filter(id => nonActiveTerminalIds.has(id))
    .toSorted()
  return `${workerId}|aa:${activeAgentIds.join(',')}|pa:${passiveAgentIds.join(',')}|at:${activeTerminalIds.join(',')}|pt:${passiveTerminalIds.join(',')}`
}

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
  tabStore: ReturnType<typeof createTabStore>
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
 * settings_changed / context_cleared arrive as LEAPMUX. Each branch is gated on the
 * inner type/method so a Pi assistant message doesn't pay for five sequential
 * extractors that can never match.
 */
export function applyNotificationMetadata(agentId: string, parsed: ParsedMessageContent, stores: AgentMessageStores): void {
  if (parsed.topLevel === null)
    return
  const { agentSessionStore, chatStore, tabStore } = stores
  const innerMsg = getInnerMessage(parsed)
  const innerType = innerMsg?.type as string | undefined
  const innerMethod = innerMsg?.method as string | undefined

  if (innerType === 'context_cleared') {
    agentSessionStore.clearContextUsage(agentId)
    chatStore.todos.clear(agentId)
    // The conversation was wiped; drop any in-flight thinking-token estimate too. The
    // backend resets its own estimator on a context clear, but that reset is in-memory
    // only (no broadcast), so the counter would otherwise linger frozen on its last
    // value until the next turn produces a delta or a clear of its own.
    agentSessionStore.clearThinkingTokens(agentId)
  }

  if (innerType === 'rate_limit_event' || innerMethod === CODEX_RATE_LIMITS_METHOD) {
    const rls = extractRateLimitInfo(parsed)
    if (rls.length > 0) {
      const rateLimits: Record<string, RateLimitInfo> = {}
      for (const rl of rls)
        rateLimits[rl.key] = rl.info
      agentSessionStore.updateInfo(agentId, { rateLimits } as Record<string, unknown>)
    }
  }

  if (innerMethod === 'thread/tokenUsage/updated') {
    const codexUsage = extractCodexTokenUsage(parsed)
    if (codexUsage)
      agentSessionStore.updateInfo(agentId, codexUsage as Record<string, unknown>)
  }

  // A completed compaction boundary makes the prior context-usage reading stale: the
  // grid would keep showing the pre-compaction size until the next assistant/result
  // message overwrites it. Refresh it straight from the boundary's post-compaction
  // token count (post_tokens, or pre - tokens_saved), and reset the component fields
  // since the boundary carries no input/cache breakdown -- contextTokens is
  // authoritative for the grid. Preserve the known context window so the percentage
  // denominator survives. Boundaries arrive consolidated (wrapper) or, when live and
  // standalone, as a bare system message; gate on those shapes so common assistant
  // messages skip the scan.
  if (parsed.wrapper !== null || innerType === 'system' || innerMethod === 'thread/compacted') {
    const postTokens = extractCompactionContextTokens(parsed)
    if (postTokens !== undefined) {
      const existing = agentSessionStore.getInfo(agentId).contextUsage
      agentSessionStore.updateInfo(agentId, {
        contextUsage: compactionContextUsage(postTokens, existing),
      })
    }
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
      if (planUpdate.updateAgentTitle && planUpdate.planTitle)
        tabStore.updateTabTitle(TabType.AGENT, agentId, planUpdate.planTitle)
    }
  }
}

/**
 * Handle a turn-end result divider (the caller gates on category.kind ===
 * 'result_divider'). Play the turn-end sound via onTurnEnd, clear the per-turn
 * thinking-token estimate, and rehydrate contextWindow / total_cost_usd. Each provider
 * plugin classifies its terminal envelope (Claude type:"result", Codex turn/completed,
 * ACP stopReason, Pi agent_end) as `result_divider`, so this is provider-agnostic.
 */
export function handleResultDivider(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  stores: AgentMessageStores,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
  catchUpPhase: 'catchingUp' | 'live',
): void {
  const { agentSessionStore, chatStore, tabStore } = stores
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
  const modelId = optionGroup(tabStore.getAgentTab(agentId)?.optionGroups, OPTION_ID_MODEL)?.currentValue
  const meta = extractResultMetadata(parsed, modelId)
  if (!meta)
    return
  // A persisted turn-end result divider clears the provider's tracked live turn-id
  // (only Codex tracks one), so the thinking indicator stops after a reconnect or
  // missed live event. The provider plugin owns WHICH subtype ends a turn; the hook
  // owns the action (clearing the session-info field).
  if (providerFor(msg.agentProvider)?.resultDividerEndsActiveTurn?.(meta.subtype)) {
    agentSessionStore.updateInfo(agentId, { codexTurnId: '' })
  }
  if (meta.subtype && catchUpPhase === 'live') {
    onTurnEnd?.(agentId, meta.numToolUses)
    // Turn boundary: reclaim any command stream orphaned by a mid-stream delete that
    // never received its own stream-end (its buffer was spared to keep in-flight
    // segments; by the turn's end a still-buffered orphan is genuinely stuck). No-op
    // when nothing was orphaned.
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
  if (msg.spanId && (msg.spanType === 'commandExecution' || msg.spanType === 'fileChange' || msg.spanType === 'reasoning') && msg.source === MessageSource.AGENT) {
    const item = parsed.parentObject?.item as Record<string, unknown> | undefined
    const isCompletedReasoning = item?.type === 'reasoning'
      && (((item.summary as unknown[] | undefined)?.length ?? 0) > 0 || ((item.content as unknown[] | undefined)?.length ?? 0) > 0)
    if ((item?.type === 'commandExecution' || item?.type === 'fileChange') && item.status === 'completed') {
      chatStore.clearCommandStream(agentId, msg.spanId)
    }
    else if (isCompletedReasoning) {
      chatStore.clearCommandStream(agentId, msg.spanId)
    }
  }
}

/**
 * Method-specific lifecycle handling + assistant-usage extraction for a persisted
 * message. Gated on AGENT source rather than category because some lifecycle items
 * (e.g. Codex `thread/started`) classify as `hidden` -- a category-only gate would
 * silently skip them. Clears a stale Codex turn id on thread/started, dismisses the
 * plan streaming UI on a plan item (the general streaming-clear already dropped the
 * text buffer), and folds any extracted token usage into the session info.
 */
export function applyAgentLifecycleAndUsage(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  agentSessionStore: AgentMessageStores['agentSessionStore'],
): void {
  if (msg.source !== MessageSource.AGENT)
    return
  const method = parsed.parentObject?.method as string | undefined
  const item = parsed.parentObject?.item as Record<string, unknown> | undefined
  if (method === 'thread/started') {
    // A new Codex thread starts idle. Clear any stale turn ID that may have been
    // restored from localStorage so the chat can show its empty state instead of a
    // phantom thinking indicator.
    agentSessionStore.updateInfo(agentId, { codexTurnId: '' })
  }
  if (item?.type === 'plan') {
    agentSessionStore.updateInfo(agentId, { streamingType: '' })
  }
  const usage = extractAssistantUsage(parsed)
  if (usage) {
    agentSessionStore.updateInfo(agentId, usage as Record<string, unknown>)
  }
}

/**
 * Process one persisted `agentMessage` frame as a sequence of named steps: the ephemeral
 * session-info short-circuit, notification metadata, the windowed append + thinking-token
 * / streaming-text clears + background trim, the completed-span stream reclaim, the
 * method-specific lifecycle/usage, and the turn-end result divider. Extracted from the
 * switch arm so the pipeline matches the sibling extractions (handleAgentSessionInfo /
 * applyNotificationMetadata / handleResultDivider) instead of one arm dwarfing the rest.
 * The caller marks the agent live BEFORE this (that step is shared with the other arms).
 */
export function handleAgentMessage(
  agentId: string,
  msg: AgentChatMessage,
  stores: AgentMessageStores,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
  catchUpPhase: 'catchingUp' | 'live',
): void {
  const { agentSessionStore, chatStore, tabStore } = stores

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
  applyNotificationMetadata(agentId, parsed, stores)

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
  if (
    !tabStore.isTabActiveAnywhere(TabType.AGENT, agentId)
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

  // Method-specific lifecycle handling and assistant-usage extraction (self-gated
  // on AGENT source so a lifecycle item that classifies as `hidden` isn't skipped).
  applyAgentLifecycleAndUsage(agentId, msg, parsed, agentSessionStore)

  // Play turn-end sound when a result divider (with subtype) arrives, and
  // rehydrate contextWindow / total_cost_usd. Each provider plugin classifies its
  // terminal envelope (Claude type:"result", Codex turn/completed, ACP stopReason,
  // Pi agent_end) as `result_divider`, so this gate is provider-agnostic.
  if (category.kind === 'result_divider')
    handleResultDivider(agentId, msg, parsed, stores, onTurnEnd, catchUpPhase)
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
 *  - keep the optimistic value for each pending axis (applyPendingAxisSuppression);
 *  - reuse the prior optionValues reference when the merged result is unchanged, so a
 *    re-broadcast that changed no current value doesn't trip every reactive reader.
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
  // Reuse the prior optionValues reference when the (possibly per-axis-merged) content
  // is unchanged so a no-op re-broadcast doesn't wake every reader of optionValues.
  if (fields.optionValues && prev?.optionValues && shallowEqual(fields.optionValues, prev.optionValues))
    fields.optionValues = prev.optionValues
  return fields
}

/**
 * Assemble the single consolidated tab update for an agent statusChange: status +
 * session id (only when status is SET, so a git-only push can't overwrite valid state
 * with proto3's UNSPECIFIED default and make the agent unwatchable), the startupError /
 * startupMessage transitions, the already-reconciled per-axis settings fields, and the
 * git fields. Pure; the caller applies it in ONE tabStore.updateTab so the store walks
 * state.tabs once (vs. the historical split that walked it twice per push).
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
    ...(sc.gitStatus
      ? {
          agentGitStatus: sc.gitStatus,
          ...toGitTabFields(sc.gitStatus.branch, sc.gitStatus.originUrl, sc.gitStatus.toplevel, sc.gitStatus.isWorktree),
        }
      : {}),
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
  catchUpPhase: 'catchingUp' | 'live',
  // The shared message-stores bag plus the controlStore only this handler needs --
  // reuse AgentMessageStores rather than re-spelling its three members inline.
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
  onTurnEnd: ((agentId: string) => void) | undefined,
): void {
  stores.controlStore.clearAgent(agentId)
  stores.agentSessionStore.clearThinkingTokens(agentId)
  if (catchUpPhase === 'live')
    stores.chatStore.sweepOrphanedBufferedSpans(agentId)
  if (catchUpPhase === 'live' && sc.agentSessionId && stores.tabStore.getAgentTab(agentId))
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
  if (value.spanId)
    chatStore.appendCommandStream(agentId, value.spanId, value.method, text)
  else
    chatStore.streamingText.set(agentId, chatStore.streamingText.get(agentId) + text)
}

/**
 * The `streamEnd` arm: close the streaming buffer (command stream or free-form text) and
 * badge the tab when the agent isn't the one on screen.
 */
export function handleStreamEnd(agentId: string, value: AgentStreamEnd, stores: Pick<AgentMessageStores, 'chatStore' | 'tabStore'>): void {
  const { chatStore, tabStore } = stores
  if (value.spanId)
    chatStore.clearCommandStream(agentId, value.spanId)
  else
    chatStore.streamingText.clear(agentId)
  if (tabStore.state.activeTabKey !== tabKey({ type: TabType.AGENT, id: agentId }))
    tabStore.setNotification(TabType.AGENT, agentId, true)
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
  catchUpPhase: 'catchingUp' | 'live',
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
): void {
  const { tabStore, controlStore, agentSessionStore } = stores
  // During catch-up, the INACTIVE statusChange may have already been processed before
  // this replayed controlRequest arrives. Skip adding the request so the user isn't
  // stuck on an unanswerable prompt.
  const agentEntry = tabStore.getAgentTab(cr.agentId)
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
  controlStore.addRequest(cr.agentId, { requestId: cr.requestId, agentId: cr.agentId, payload })
  if (catchUpPhase === 'live') {
    // Light up the tab badge so a user looking at a sibling tab knows the background
    // agent is now waiting on them.
    if (tabStore.state.activeTabKey !== tabKey({ type: TabType.AGENT, id: cr.agentId }))
      tabStore.setNotification(TabType.AGENT, cr.agentId, true)
    // The agent paused mid-turn to wait on the user; it is no longer thinking, and this
    // pause may produce no agent message and no INACTIVE, so drop the per-turn estimate
    // here too -- otherwise the counter lingers frozen until the next turn.
    agentSessionStore.clearThinkingTokens(agentId)
    onTurnEnd?.(agentId)
  }
}

/**
 * The `statusChange` arm: apply a worker status snapshot to the agent tab. Skips a
 * payload-less catch-up sentinel; otherwise drains the pending-outbound queue on a
 * STARTING->ACTIVE/STARTUP_FAILED transition, reconciles the reported option-group
 * catalog into the tab (with per-axis optimistic suppression), consolidates every field
 * into ONE updateTab, stops the aggregate settings spinner when nothing's pending, and
 * runs the INACTIVE turn-end cleanup. The worker-online flag is authoritative only on a
 * full status snapshot. Orchestration over the already-extracted pure helpers
 * (drainPendingOutboundOnStart / resolveSettingsTabFields / buildAgentStatusTabUpdate /
 * handleAgentInactive); `setWorkerOnline` is the hook's signal setter.
 */
export function handleAgentStatusChange(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: 'catchingUp' | 'live',
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
  settingsLoading: WorkspaceConnectionParams['settingsLoading'],
  setWorkerOnline: (online: boolean) => void,
  onTurnEnd: ((agentId: string, numToolUses?: number) => void) | undefined,
): void {
  const { chatStore, tabStore } = stores
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

  // Read the prior status before updateTab overwrites it so a STARTING ->
  // ACTIVE / STARTUP_FAILED transition can drain the per-agent pending-message queue.
  const prev = tabStore.getAgentTab(sc.agentId)
  drainPendingOutboundOnStart(sc, prev, chatStore)

  // Whether THIS agent has any settings change in flight -- gates only the aggregate
  // spinner stop below; the optimistic-value suppression is per-AXIS (pendingAxes).
  const pendingSettings = settingsLoading.isPending(sc.agentId)
  // Prime the global settings-label cache from the reported groups at this data-ingestion
  // boundary so notification renderers can resolve human names when an inline label is
  // missing (resolveSettingsTabFields itself is pure).
  if (sc.optionGroups.length > 0)
    updateSettingsLabelCache(sc.agentProvider, sc.optionGroups)
  const settingsFields = resolveSettingsTabFields(prev, sc.optionGroups, settingsLoading.pendingAxes(sc.agentId))

  // Consolidate every per-status field into one updateTab so the store walks state.tabs once.
  tabStore.updateTab(TabType.AGENT, sc.agentId, buildAgentStatusTabUpdate(sc, hasStatus, settingsFields))
  if (!pendingSettings)
    settingsLoading.stop()
  if (sc.status === AgentStatus.INACTIVE)
    handleAgentInactive(agentId, sc, catchUpPhase, stores, onTurnEnd)
}

/**
 * Forward-fill the window->live tail gap for every agent tab that lags its recorded
 * live tail. Two cases, both run inside a reactive effect (see useWorkspaceConnection) so
 * this re-evaluates whenever any agent's window tail / recorded live tail / deferral flag
 * moves -- replacing the one-shot forward-fill that fired only on CatchUpComplete:
 *  - reader AT the tail (hasNewerMessages false but NOT caught up): catchUpToTail drains
 *    the gap via its addMessage path.
 *  - an EXHAUSTION-FORCED park (hasNewerMessages true because a broadcast storm outran the
 *    bounded forward-fill, NOT a settled scrolled-away wall): resumeDeferredTailFill
 *    resumes the bounded fill so a FOLLOWING reader self-heals as the storm drains. A
 *    plain scrolled-away hasNewerMessages (deferral flag clear) is left to the affordance.
 * Reads ALL invariant terms per agent so the effect subscribes to each. Both fillers are
 * idempotent (no-op while one is already draining the agent), so a re-run on their own
 * per-page writes does no work; a tab with an empty workerId (a non-active-workspace
 * agent) is skipped.
 */
export function reconcileLaggingTails(deps: {
  agentTabs: () => ReadonlyArray<{ id: string, workerId: string }>
  hasNewerMessages: (agentId: string) => boolean
  caughtUpToLiveTail: (agentId: string) => boolean
  isTailFillDeferred: (agentId: string) => boolean
  getLastSeq: (agentId: string) => bigint
  isFetchingNewer: (agentId: string) => boolean
  catchUpToTail: (workerId: string, agentId: string, afterSeq: bigint) => void
  resumeDeferredTailFill: (workerId: string, agentId: string) => void
  jumpToLatest: (workerId: string, agentId: string) => void
}): void {
  for (const tab of deps.agentTabs()) {
    if (!tab.workerId)
      continue
    const atTail = !deps.hasNewerMessages(tab.id)
    const caughtUp = deps.caughtUpToLiveTail(tab.id)
    const deferred = deps.isTailFillDeferred(tab.id)
    if (caughtUp)
      continue
    // The window is EMPTY (getLastSeq 0n) while server content still exists (liveTail > 0,
    // so not caughtUp): e.g. a full phantom reap on reconnect dropped every loaded row
    // (all were tail rows deleted while disconnected) but older history survives. There's
    // no loaded anchor to forward-fill from, so re-seat on the latest page. Guarded on the
    // in-flight flag so the re-seat isn't re-issued each reconcile tick while it resolves
    // (jumpToLatest aborts + restarts its own fetch, which would otherwise loop).
    if (deps.getLastSeq(tab.id) === 0n) {
      if (!deps.isFetchingNewer(tab.id))
        deps.jumpToLatest(tab.workerId, tab.id)
      continue
    }
    if (atTail)
      deps.catchUpToTail(tab.workerId, tab.id, deps.getLastSeq(tab.id))
    else if (deferred)
      deps.resumeDeferredTailFill(tab.workerId, tab.id)
  }
}

export interface WorkspaceConnectionParams {
  chatStore: ReturnType<typeof createChatStore>
  tabStore: ReturnType<typeof createTabStore>
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  registry: WorkspaceStoreRegistryType
  settingsLoading: ReturnType<typeof createLoadingSignal>
  getActiveWorkspaceId: () => string | null
  /** Returns the worker ID for the active workspace. */
  getWorkerId: () => string
  /** Called when an agent turn ends (turn completed or control request received). */
  onTurnEnd?: (agentId: string, numToolUses?: number) => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { chatStore, tabStore, controlStore, agentSessionStore, settingsLoading } = params
  const [workerOnline, setWorkerOnline] = createSignal(true)

  // Single unified event stream abort controller.
  let eventStreamAbort: AbortController | null = null
  // Serialized key of the current subscription set to detect changes.
  let currentTargetsKey = ''

  // Set of agent/terminal IDs that belong to non-active workspaces (in registry
  // snapshots). Events for these receive lightweight handling (status/git updates
  // to the snapshot) rather than full chat processing.
  const nonActiveAgentIds = new Set<string>()
  const nonActiveTerminalIds = new Set<string>()

  // Handle an agent event from the unified stream.
  const handleAgentEvent = (
    agentEvent: AgentEvent,
    catchUpPhases: Map<string, 'catchingUp' | 'live'>,
    // The resume cursor (the client's loaded/recorded tail) this subscribe sent per
    // agent, captured at subscribe time. Used as the CatchUpStart reap ceiling so a
    // live message that raced in AFTER subscribe (seq above it) isn't reaped as a
    // phantom -- see the catchUpStart case.
    resumeTails: Map<string, bigint>,
  ) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    // Non-active workspace agent — only handle status/git changes,
    // skip full chat processing to avoid routing events to the wrong stores.
    //
    // Live controlRequest / controlCancel / agentMessage / streamChunk events
    // for these agents are intentionally dropped here: the user can't see them
    // (no active tab renders this agent), and the WatchEvents catch-up replay
    // run by useWorkspaceConnection.watchEvents on workspace switch reads
    // pending control_requests from the DB, so any still-pending prompt is
    // re-delivered through the full handler at that point. The DB row is the
    // source of truth; live broadcasts are an optimization for the active
    // workspace.
    if (nonActiveAgentIds.has(agentId)) {
      if (inner.case === 'statusChange') {
        const sc = inner.value
        if (sc.status === AgentStatus.UNSPECIFIED && !sc.gitStatus)
          return
        // Find the snapshot that owns this agent by looking for a tab
        // with this id and AGENT type — the per-agent metadata now
        // travels on the tab record, so the lookup is unified.
        const owningWsId = params.registry.findContaining(
          s => s.tabs.some(t => t.type === TabType.AGENT && t.id === agentId),
        )?.workspaceId
        if (owningWsId) {
          params.registry.update(owningWsId, (snap) => {
            let tabs = snap.tabs
            if (sc.status !== AgentStatus.UNSPECIFIED) {
              const i = snap.tabs.findIndex(t => t.type === TabType.AGENT && t.id === agentId)
              const existing = i >= 0 ? snap.tabs[i] : undefined
              if (existing && isAgentTab(existing) && existing.agentStatus !== sc.status) {
                tabs = snap.tabs.slice()
                tabs[i] = { ...existing, agentStatus: sc.status }
              }
            }
            if (sc.gitStatus) {
              const next = toGitTabFields(sc.gitStatus.branch, sc.gitStatus.originUrl, sc.gitStatus.toplevel, sc.gitStatus.isWorktree)
              tabs = spliceTabGitFields(tabs, t => t.type === TabType.AGENT && t.id === agentId, next)
            }
            if (tabs === snap.tabs)
              return snap
            return { ...snap, tabs }
          })
        }
      }
      return
    }

    // Get or initialize catch-up phase for this agent.
    const catchUpPhase = catchUpPhases.get(agentId) ?? 'live'
    const markLiveAgentActive = () => {
      if (catchUpPhase !== 'live')
        return
      setWorkerOnline(true)
      const current = tabStore.getAgentTab(agentId)
      if (current?.agentStatus === AgentStatus.INACTIVE) {
        tabStore.updateTab(TabType.AGENT, agentId, { agentStatus: AgentStatus.ACTIVE })
      }
    }

    switch (inner.case) {
      case 'agentMessage':
        markLiveAgentActive()
        handleAgentMessage(
          agentId,
          inner.value,
          { agentSessionStore, chatStore, tabStore },
          params.onTurnEnd,
          catchUpPhase,
        )
        break
      case 'streamChunk':
        markLiveAgentActive()
        handleStreamChunk(agentId, inner.value, chatStore)
        break
      case 'streamEnd':
        markLiveAgentActive()
        handleStreamEnd(agentId, inner.value, { chatStore, tabStore })
        break
      case 'statusChange':
        handleAgentStatusChange(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, tabStore, controlStore },
          settingsLoading,
          setWorkerOnline,
          params.onTurnEnd,
        )
        break
      case 'controlRequest':
        markLiveAgentActive()
        handleControlRequest(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, tabStore, controlStore },
          params.onTurnEnd,
        )
        break
      case 'controlCancel': {
        const cc = inner.value
        controlStore.removeRequest(cc.agentId, cc.requestId)
        break
      }
      case 'messageError': {
        const me = inner.value
        if (me.error) {
          chatStore.setMessageError(me.messageId, me.error)
        }
        else {
          chatStore.clearMessageError(me.messageId)
        }
        break
      }
      case 'messageDeleted': {
        const md = inner.value
        // Pass the deleted row's seq (so removeMessage can tell whether it was the
        // recorded live tail) and the authoritative post-delete tail (so it can set
        // the high-water to exactly that, even when the deleted row was unloaded
        // beyond the window) -- no deletedSeq-1 guesswork.
        chatStore.removeMessage(md.agentId, md.messageId, md.seq, md.newLatestSeq)
        break
      }
      case 'todosChanged': {
        // Sole driver of the sidebar to-do list. The worker persists
        // every to-do event in agent_todos and ships the post-mutation
        // snapshot here; clients replace wholesale.
        const tc = inner.value
        chatStore.todos.replace(tc.agentId, tc.todos)
        break
      }
      case 'catchUpStart':
        // Pre-trim BEFORE the message replay renders: the worker ships the
        // authoritative live tail up front so a reconnecting client drops phantom rows
        // (a tail it loaded before disconnect that was deleted while away) immediately,
        // rather than flashing them until catchUpComplete reconciles at the end. An
        // unset latest_seq (worker couldn't determine the tail) is skipped by the store.
        //
        // At catch-up START, latest_seq IS the start tail, so a phantom and a live
        // message that raced in are indistinguishable by seq alone -- both sit above
        // latest_seq. The watcher is registered BEFORE the worker reads the tail, so
        // such a live frame CAN land before this one (a tight race). Bound the reap by
        // the resume cursor this subscribe sent (the client's loaded tail): a row above
        // it arrived AFTER subscribe -- a genuine live arrival -- and is exempted, while
        // a phantom (a once-loaded row, so seq <= the resume cursor) is still reaped.
        // catchUpComplete then reconciles again with the authoritative band.
        chatStore.reconcileAuthoritativeTail(agentId, inner.value.latestSeq, resumeTails.get(agentId))
        break
      case 'catchUpComplete':
        catchUpPhases.set(agentId, 'live')
        // Catch-up done: the live-append guard returns to the recorded-live-tail
        // comparison (which correctly splices a post-delete message). Clear BEFORE the
        // reconcile so the probe's contiguous forward-fill pages aren't re-dropped.
        chatStore.setCatchingUp(agentId, false)
        // Reconcile the window to the authoritative live tail the worker reports (the
        // final authority after the replay burst; catchUpStart did an early pass). A
        // reconnecting client never received the AgentMessageDeleted for rows deleted
        // while it was disconnected, so it drops phantom rows beyond latest_seq and
        // clamps its recorded live-tail -- otherwise its "new messages below"
        // affordance can stay stuck past a now-shorter history. An unset latest_seq
        // (worker couldn't determine the tail) is skipped for the reap, but PROBED (see
        // probeIndeterminate below). start_tail_seq (the tail when replay began) bounds
        // the reap so a live message that raced in DURING catch-up -- seq above it --
        // isn't reaped as a phantom. When start_tail_seq is indeterminate (unset, a failed
        // worker readback), fall back to the resume cursor (the loaded tail this subscribe
        // sent) as the ceiling -- the SAME bound catchUpStart uses -- so a live arrival
        // above it is still exempted instead of reaping everything beyond latest_seq and
        // losing the raced-in message.
        //
        // The bounded replay (<= 50 rows) may not have reached the tail, but we do NOT
        // forward-fill here: reconcileAuthoritativeTail raises the recorded live tail to
        // latest_seq, and the continuous tail-reconcile effect (below) forward-fills
        // whenever the loaded tail lags it -- so a windowed-away reader keeps their
        // position + affordance, and a following reader's gap closes without hinging on
        // this frame. The ONE exception folded into the reconcile is an INDETERMINATE
        // (unset) tail: liveTail can't be raised, so probeIndeterminate=true nudges it one
        // past the loaded tail to make the continuous reconcile probe (it would otherwise
        // read the partial replay as caught up). This keeps ALL forward-fill in the
        // continuous reconcile -- there is no one-shot fill in this handler.
        chatStore.reconcileAuthoritativeTail(
          agentId,
          inner.value.latestSeq,
          inner.value.startTailSeq === undefined
            ? resumeTails.get(agentId)
            : inner.value.startTailSeq,
          true,
        )
        // Re-seed the scroll-rail marks so any user sends / deletes that happened while
        // disconnected are reflected (live add/remove already heals the connected case). Tied
        // to this subscription's signal so a resubscribe/teardown cancels it (see loadMessageMarks).
        void chatStore.loadMessageMarks(tabStore.getAgentTab(agentId)?.workerId ?? '', agentId, eventStreamAbort?.signal)
        // Reclaim any command stream orphaned DURING catch-up: a mid-stream
        // delete (or beyond-window reseq) recorded an orphan, but its turn-end
        // divider replayed while the phase was still 'catchingUp', so the
        // turn-end sweep above was skipped. Drain once here on the transition so
        // an orphan can't sit stuck until the next live turn-end (or forever, if
        // none follows). No-op when nothing was orphaned.
        chatStore.sweepOrphanedBufferedSpans(agentId)
        break
    }
  }

  // Handle a terminal event from the unified stream.
  const handleTerminalEvent = (termEvent: TerminalEvent) => {
    const terminalId = termEvent.terminalId

    // Non-active workspace terminal — skip data events (no terminal instance
    // exists), but handle closed + statusChange to keep the snapshot's
    // status / gitBranch / gitOriginUrl fresh for the sidebar badge.
    if (nonActiveTerminalIds.has(terminalId)) {
      if (termEvent.event.case === 'closed') {
        const key = tabKey({ type: TabType.TERMINAL, id: terminalId })
        const owningWsId = params.registry.findContaining(
          s => s.tabs.some(t => tabKey(t) === key && isTerminalTab(t) && t.status !== TerminalStatus.EXITED),
        )?.workspaceId
        if (owningWsId) {
          params.registry.update(owningWsId, snap => ({
            ...snap,
            tabs: snap.tabs.map(t => tabKey(t) === key && isTerminalTab(t) ? { ...t, status: TerminalStatus.EXITED } : t),
          }))
        }
      }
      else if (termEvent.event.case === 'statusChange') {
        const sc = termEvent.event.value
        if (sc.gitBranch || sc.gitOriginUrl || sc.gitToplevel) {
          const key = tabKey({ type: TabType.TERMINAL, id: terminalId })
          const owningWsId = params.registry.findContaining(s => s.tabs.some(t => tabKey(t) === key))?.workspaceId
          if (owningWsId) {
            const next = toGitTabFields(sc.gitBranch, sc.gitOriginUrl, sc.gitToplevel, sc.gitIsWorktree)
            params.registry.update(owningWsId, (snap) => {
              const tabs = spliceTabGitFields(snap.tabs, t => tabKey(t) === key, next)
              return tabs === snap.tabs ? snap : { ...snap, tabs }
            })
          }
        }
      }
      return
    }

    switch (termEvent.event.case) {
      case 'data': {
        const instance = getTerminalInstance(terminalId)
        if (instance) {
          const tab = tabStore.getTerminalTab(terminalId)
          const checkContent = tab && !tab.contentReady
          const onParsed = () => {
            if (checkContent && bufferHasVisibleContent(instance.terminal))
              tabStore.markTerminalContentReady(terminalId)
          }
          const { data, isSnapshot, endOffset } = termEvent.event.value
          const newOffset = applyTerminalData(
            instance,
            data,
            isSnapshot,
            Number(endOffset),
            tab?.lastOffset ?? 0,
            onParsed,
          )
          tabStore.setTerminalLastOffset(terminalId, newOffset)
        }
        break
      }
      case 'closed':
        tabStore.markTerminalExited(terminalId)
        break
      case 'statusChange': {
        const sc = termEvent.event.value
        // Only propagate into the tab store when the server reports a
        // terminal lifecycle transition — STARTING, READY, or
        // STARTUP_FAILED. READY and STARTUP_FAILED both arrive on
        // normal subscribe via WatchEvents's catch-up, so the race of a
        // late subscriber missing the one-shot broadcast is closed.
        const existingTab = tabStore.getTerminalTab(terminalId)
        // Git branch / origin / toplevel are carried on every post-phase-0
        // STARTING broadcast. Update the tab whenever a non-empty value
        // arrives so a reconnect or a late worktree-creation refreshes the
        // badge.
        if (existingTab && (sc.gitBranch || sc.gitOriginUrl || sc.gitToplevel)) {
          const next = toGitTabFields(sc.gitBranch, sc.gitOriginUrl, sc.gitToplevel, sc.gitIsWorktree)
          if (gitTabFieldsDiffer(existingTab, next))
            tabStore.updateTab(TabType.TERMINAL, terminalId, next)
        }
        switch (sc.status) {
          case TerminalStatus.STARTING:
            if (existingTab && existingTab.status !== TerminalStatus.READY && existingTab.status !== TerminalStatus.STARTING) {
              tabStore.updateTab(TabType.TERMINAL, terminalId, {
                status: TerminalStatus.STARTING,
                startupMessage: sc.startupMessage || undefined,
              })
            }
            else if (existingTab?.status === TerminalStatus.STARTING && sc.startupMessage && sc.startupMessage !== existingTab.startupMessage) {
              // Same-status STARTING event with an updated phase label —
              // refresh the overlay text without re-triggering the
              // status-change observers.
              tabStore.updateTab(TabType.TERMINAL, terminalId, { startupMessage: sc.startupMessage })
            }
            break
          case TerminalStatus.READY:
            // Preserve DISCONNECTED / EXITED — a previously-alive terminal
            // whose worker reconnected should not be dragged back to READY.
            if (existingTab?.status === TerminalStatus.STARTING || existingTab?.status === undefined) {
              tabStore.updateTab(TabType.TERMINAL, terminalId, {
                status: TerminalStatus.READY,
                startupError: undefined,
                startupMessage: undefined,
              })
            }
            break
          case TerminalStatus.STARTUP_FAILED:
            tabStore.updateTab(TabType.TERMINAL, terminalId, {
              status: TerminalStatus.STARTUP_FAILED,
              startupError: sc.startupError || undefined,
              startupMessage: undefined,
            })
            break
        }
        break
      }
    }
  }

  // Previous stream handle, kept alive during the gap between abort and
  // new stream registration so the server-side watcher can still deliver
  // terminal data until the new WatchEvents updates its routing.
  let previousHandle: { close: () => void } | null = null

  // Unified event stream via E2EE channel with retry.
  const watchEvents = async (
    agentEntries: WatchAgentEntry[],
    terminalIds: string[],
    signal: AbortSignal,
  ) => {
    // Load initial messages for active workspace agents only. Non-active
    // workspace agents only receive lightweight status/git updates — they
    // don't need full chat history loaded.
    await Promise.all(
      agentEntries
        .filter(entry => !nonActiveAgentIds.has(entry.agentId))
        .map(async (entry) => {
          try {
            const wid = tabStore.getAgentTab(entry.agentId)?.workerId ?? ''
            await chatStore.loadInitialMessages(wid, entry.agentId)
            // Seed the scroll-rail marks alongside history (not awaited: a failure
            // must not block or fail the history load -- the rail just stays hidden). Tied to
            // this subscription's signal so a resubscribe/teardown cancels it.
            void chatStore.loadMessageMarks(wid, entry.agentId, eventStreamAbort?.signal)
          }
          catch (err) {
            showWarnToast('Failed to load chat history', err)
          }
        }),
    )

    if (signal.aborted)
      return

    // Per-agent catch-up phase tracking.
    const catchUpPhases = new Map<string, 'catchingUp' | 'live'>()
    for (const entry of agentEntries) {
      catchUpPhases.set(entry.agentId, 'catchingUp')
    }
    // The resume cursor sent per agent on the current (re)subscribe, captured below so
    // CatchUpStart can exempt live arrivals that post-date it from the phantom reap.
    const resumeTails = new Map<string, bigint>()

    // Per-loop reconnect backoff. Successful events reset the
    // sequence; stream-level errors also reset (the legacy code did
    // `Math.min(backoff, 500)` to retry fast, which the helper's
    // initial-delay floor of 1s approximates closely enough). Only
    // sustained connection-lost errors let the sequence walk up to
    // 30s.
    const backoff = createExponentialBackoff<string>({
      initialMs: 1000,
      maxMs: 30000,
      multiplier: 2,
      jitterFactor: 0,
    })
    const BACKOFF_KEY = 'watch'
    signal.addEventListener('abort', () => backoff.cancelAll(), { once: true })

    while (!signal.aborted) {
      try {
        // Build entries with current afterSeq values. Resume from the highest
        // observed live seq (not just the window tail): while scrolled away from
        // the tail, the window tail lags the live tail, and resuming there would
        // make the worker replay a page of messages the live-append guard drops.
        const agents = agentEntries.map((entry) => {
          const resumeSeq = untrack(() => chatStore.getResumeAfterSeq(entry.agentId))
          // Capture the resume cursor as the CatchUpStart reap ceiling (see catchUpStart).
          resumeTails.set(entry.agentId, resumeSeq)
          return agentWatchEntry(entry.agentId, resumeSeq)
        })

        const workerId = untrack(() => params.getWorkerId())
        if (!workerId)
          return

        // Seed after_offset from the tab's resume cursor; 0 means a
        // cold subscribe (the tab was hydrated without a screen or the
        // cursor hasn't advanced yet).
        const terminals = terminalIds.map(id => ({
          terminalId: id,
          afterOffset: BigInt(untrack(() => tabStore.getTerminalTab(id)?.lastOffset ?? 0)),
        }))

        // Open the E2EE channel stream to the Worker.
        const handle = await watchEventsViaChannel(workerId, {
          agents,
          terminals,
        })

        // Reset catch-up phases only after the replacement stream exists. workerRpc
        // buffers events until onEvent is wired below, so a pre-CatchUpStart live frame
        // cannot sneak through without the guard; a failed open no longer leaves the
        // store permanently in catching-up mode.
        for (const entry of agentEntries) {
          catchUpPhases.set(entry.agentId, 'catchingUp')
          if (!nonActiveAgentIds.has(entry.agentId))
            chatStore.setCatchingUp(entry.agentId, true)
        }

        // Wire the consumer callbacks before closing the previous handle.
        // workerRpc.ts buffers any events that arrive before onEvent is
        // wired; waitForStreamCompletion captures end / error / abort that
        // fire during the synchronous setup window.
        handle.onEvent((response) => {
          backoff.reset(BACKOFF_KEY)
          switch (response.event.case) {
            case 'agentEvent':
              handleAgentEvent(response.event.value, catchUpPhases, resumeTails)
              break
            case 'terminalEvent':
              handleTerminalEvent(response.event.value)
              break
          }
        })

        // Now that callbacks are wired, clean up the previous stream.
        // The server-side sender update ensures no more events arrive on
        // the old request ID once the server processes this WatchEvents.
        previousHandle?.close()
        previousHandle = handle

        await waitForStreamCompletion(handle, signal)
      }
      catch (err) {
        if (signal.aborted)
          return

        const isConnectionLost = err instanceof ChannelError && err.source === 'transport'

        if (isConnectionLost) {
          showWarnToast('Connection to worker lost, reconnecting\u2026', err)
          // Channel disconnected (worker went offline or restarted).
          // Mark worker as offline so terminals show disconnection and
          // thinking indicators are hidden.
          setWorkerOnline(false)
        }
        else {
          // Stream-level error (e.g. NOT_FOUND for entities not yet
          // visible). Retry quickly without alarming the user. Reset
          // the backoff so a benign transient error doesn't inherit a
          // long delay from a prior connection-lost streak.
          log.warn('[watchEvents] stream error, retrying:', err)
          backoff.reset(BACKOFF_KEY)
        }
      }

      if (signal.aborted)
        return
      await new Promise<void>((resolve) => {
        backoff.schedule(BACKOFF_KEY, resolve)
      })
    }
  }

  // Watch all agents and terminals on the current worker via a single
  // unified WatchEvents stream. When the entity set changes (new agent
  // or terminal created), the effect triggers a stream restart.
  // Also includes agents/terminals from non-active workspace snapshots
  // in the registry, so that status updates are received for all workspaces.
  createEffect(() => {
    const workerId = params.getWorkerId()
    const wsId = params.getActiveWorkspaceId()

    // Collect all agent IDs on this worker.
    const agentEntries: WatchAgentEntry[] = []
    const terminalIds: string[] = []
    nonActiveAgentIds.clear()
    nonActiveTerminalIds.clear()

    if (wsId && workerId) {
      // Active workspace agents/terminals: both kinds now live in
      // tabStore. AGENT tabs carry their metadata directly on the tab.
      for (const tab of tabStore.state.tabs) {
        if (tab.workerId !== workerId)
          continue
        if (tab.type === TabType.AGENT) {
          // Seed entry: the per-subscribe build (see agents.map above) recomputes
          // replay/cursor from the live resume cursor, so the placeholder is fresh.
          agentEntries.push(agentWatchEntry(tab.id, BigInt(0)))
        }
        else if (tab.type === TabType.TERMINAL) {
          terminalIds.push(tab.id)
        }
      }

      // Non-active workspace agents/terminals from registry snapshots.
      const activeAgentIds = new Set(agentEntries.map(e => e.agentId))
      const activeTermIds = new Set(terminalIds)

      for (const snap of params.registry.all()) {
        if (snap.workspaceId === wsId)
          continue
        if (!snap.tabsLoaded)
          continue
        for (const tab of snap.tabs) {
          if (tab.workerId !== workerId)
            continue
          if (tab.type === TabType.AGENT && !activeAgentIds.has(tab.id)) {
            agentEntries.push(agentWatchEntry(tab.id, BigInt(0)))
            nonActiveAgentIds.add(tab.id)
          }
          else if (tab.type === TabType.TERMINAL && !activeTermIds.has(tab.id)) {
            terminalIds.push(tab.id)
            nonActiveTerminalIds.add(tab.id)
          }
        }
      }
    }

    // Build a key representing the current subscription set and each target's role.
    // Role matters: the same id moving between active and non-active handling must
    // restart the stream so callbacks stop dropping full chat/control processing.
    const newKey = buildWatchTargetsKey(workerId, agentEntries, terminalIds, nonActiveAgentIds, nonActiveTerminalIds)

    // Skip if the subscription set hasn't changed.
    if (newKey === currentTargetsKey)
      return

    // Tear down old stream.
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    currentTargetsKey = newKey

    // Start new stream if there's anything to watch. If the new set is empty, no
    // replacement stream will arrive to retire the previous handle, so close it now.
    if (!workerId || (agentEntries.length === 0 && terminalIds.length === 0)) {
      previousHandle?.close()
      previousHandle = null
      return
    }

    const abort = new AbortController()
    eventStreamAbort = abort
    watchEvents(agentEntries, terminalIds, abort.signal)
  })

  // When the worker goes offline, mark running terminals as disconnected,
  // clear stale streaming text, and set active agents to inactive so the
  // thinking indicator hides. The real status will arrive when the WatchEvents
  // stream reconnects.
  createEffect(() => {
    if (workerOnline())
      return
    const workerId = params.getWorkerId()
    const isAffected = (t: Tab) =>
      t.type === TabType.TERMINAL && t.workerId === workerId && t.status === TerminalStatus.READY
    batch(() => {
      tabStore.markTerminalsDisconnected(workerId)
      for (const snap of params.registry.all()) {
        if (!snap.tabs.some(isAffected))
          continue
        params.registry.update(snap.workspaceId, s => ({
          ...s,
          tabs: s.tabs.map(t => isAffected(t) ? { ...t, status: TerminalStatus.DISCONNECTED } : t),
        }))
      }
    })
    for (const tab of tabStore.state.tabs) {
      if (tab.type !== TabType.AGENT)
        continue
      chatStore.streamingText.clear(tab.id)
      for (const spanId of Object.keys(chatStore.getAgentCommandStreams(tab.id)))
        chatStore.clearCommandStream(tab.id, spanId)
      if (tab.agentStatus === AgentStatus.ACTIVE) {
        tabStore.updateTab(TabType.AGENT, tab.id, { agentStatus: AgentStatus.INACTIVE })
      }
    }
  })

  // Lazy message loading for agent tabs not on the current worker
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    if (!activeKey)
      return
    const parts = activeKey.split(':')
    if (parts.length !== 2)
      return
    const tabType = Number(parts[0]) as TabType
    if (tabType !== TabType.AGENT)
      return
    const tabId = parts[1]
    if (chatStore.isInitialLoadComplete(tabId))
      return
    // Only load messages for agents in the active workspace's tab store.
    // Non-active workspace agents exist only in registry snapshots and
    // don't have a workerId locally — attempting to load with an empty
    // workerId would cause an "invalid_argument" error.
    const agent = tabStore.getAgentTab(tabId)
    if (!agent || !agent.workerId)
      return
    chatStore.loadInitialMessages(agent.workerId, tabId).catch((err) => {
      showWarnToast('Failed to load chat history', err)
    })
    // Seed the scroll-rail marks for the newly-opened agent (fire-and-forget). Tied to the
    // current subscription's signal so a resubscribe/teardown cancels it (see loadMessageMarks).
    void chatStore.loadMessageMarks(agent.workerId, tabId, eventStreamAbort?.signal)
  })

  // Continuous tail reconcile: whenever a loaded window lags its recorded live tail
  // while the reader is AT the tail (hasMoreNewer false but NOT caught up), forward-fill
  // the gap. This REPLACES the one-shot forward-fill that fired on catchUpComplete when
  // replay_has_more: keying on the windowing invariant rather than a discrete frame
  // makes it (a) fire for ANY cause of the lag -- a bounded catch-up replay OR a live
  // arrival the store dropped beyond the window to keep it contiguous
  // (beyondUnloadedNewerTail) -- and (b) robust to a stream drop between the catch-up
  // status marker and CatchUpComplete (the gap closes on the next reactive change, not
  // only on a successful CatchUpComplete). catchUpToTail is idempotent (no-ops while one
  // is already draining the agent) and its loop exits the moment the window catches up
  // or the reader scrolls away, so a re-run on its own per-page writes does no work.
  createEffect(() => reconcileLaggingTails({
    agentTabs: () => tabStore.state.tabs
      .filter(t => t.type === TabType.AGENT)
      .map(t => ({ id: t.id, workerId: t.workerId ?? '' })),
    hasNewerMessages: id => chatStore.hasNewerMessages(id),
    caughtUpToLiveTail: id => chatStore.caughtUpToLiveTail(id),
    isTailFillDeferred: id => chatStore.isTailFillDeferred(id),
    getLastSeq: id => chatStore.getLastSeq(id),
    isFetchingNewer: id => chatStore.isFetchingNewer(id),
    // Tie all three reconcile-driven forward-fill paths to the CURRENT WatchEvents
    // subscription, so a workspace switch / worker change (which aborts + replaces
    // eventStreamAbort) stops a fetch running against a worker the reader navigated
    // away from instead of leaking it. They are single-flight + idempotent, so a
    // resubscribe that aborts one simply restarts it on the next reconcile tick -- the
    // pre-windowing teardown guarantee, restored. The empty-window re-seat
    // (jumpToLatest) ties via the store's beginHistoryFetch (its fetch + the
    // forwardFillToLiveTail loop it drives both abort with the signal).
    catchUpToTail: (workerId, agentId, afterSeq) => void chatStore.catchUpToTail(workerId, agentId, afterSeq, eventStreamAbort?.signal),
    resumeDeferredTailFill: (workerId, agentId) => void chatStore.resumeDeferredTailFill(workerId, agentId, eventStreamAbort?.signal),
    jumpToLatest: (workerId, agentId) => void chatStore.jumpToLatestMessages(workerId, agentId, eventStreamAbort?.signal),
  }))

  // Abort the stream on page unload. SolidJS's onCleanup does
  // not fire on hard browser refresh, so without this the connection stays
  // open as a zombie until the server times it out.
  const abortStream = () => {
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    previousHandle?.close()
    previousHandle = null
  }

  window.addEventListener('beforeunload', abortStream)

  onCleanup(() => {
    window.removeEventListener('beforeunload', abortStream)
    abortStream()
    currentTargetsKey = ''
  })

  return {
    workerOnline,
  }
}
