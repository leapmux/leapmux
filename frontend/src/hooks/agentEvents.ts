/** Each agent-event handler receives explicit stores and runs independently of the connection hook. */
import type { AgentActivityState, AgentChatMessage, AgentControlCancelRequest, AgentControlRequest, AgentStatusChange, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { AgentSettledEventDetail } from '~/lib/agentSettledEvent'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { RateLimitInfo, RateLimitUpdate } from '~/models/agentSession'
import type { AgentActivityStore } from '~/stores/agentActivity.store'
import type { createAgentSessionStore, LiveGenerationProgress, SessionMetadataDelivery } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { GoalProgress } from '~/stores/chatGoal'
import type { ToolProgressRetry, ToolProgressUpdate } from '~/stores/chatToolProgress'
import type { ControlPayloadFault, createControlStore } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { AgentTab } from '~/stores/tab.types'
import type { LiveWrite, TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { classifyAgentMessage } from '~/components/chat/messageClassifier'
import { compactionContextTokens } from '~/components/chat/notificationEntries'
import { providerFor, resolvedSpanRole, resolveMessageForRendering } from '~/components/chat/providers/registry'
import { mergeStableOptionGroupRefs, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { GOAL_PROGRESS_FIELD, RATE_LIMIT_FIELD, RATE_LIMIT_UPDATE_FIELD, RATE_LIMIT_UPDATE_MODE, RUNNING_TOOL_FIELD, RUNNING_TOOL_RETRY_FIELD, SESSION_INFO_KEY } from '~/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentStatus, ControlResponseState, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { isTabOnScreen } from '~/hooks/watchPlan'
import { AGENT_SETTLED_EVENT } from '~/lib/agentSettledEvent'
import { assignDefined, isObject, pickBoolean, pickCounter, pickNumber, pickString } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { extractContextUsage, extractPlanFilePath, extractPlanUpdated, extractResultMetadata, extractSettingsChanges, getInnerMessage, normalizeContextUsage, parseMessageContent } from '~/lib/messageParser'
import { messageSpanIdentity } from '~/lib/messageSpan'
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
 * Translate the neutral snake_case rate-limit payload into camelCase RateLimitInfo values.
 * The agent session store and rate-limit helpers consume those values.
 * Claude and Codex both emit this neutral wire shape.
 */
function wireRateLimitsToCamel(value: unknown): Record<string, RateLimitInfo> | undefined {
  if (!isObject(value))
    return undefined
  const out: Record<string, RateLimitInfo> = {}
  for (const [key, tier] of Object.entries(value)) {
    if (!isObject(tier))
      continue
    // Each typed picker validates one field. assignDefined omits absent or invalid fields.
    // RateLimitInfo has eight fields, and shallowEqual compares the key count before the values.
    // The serializer omits absent keys from a stored tier.
    // Writing those keys as undefined here would make each broadcast compare unequal to that stored tier.
    // An explicit undefined fallback keeps the destination field's RateLimitInfo type.
    // The type checker rejects a picker whose return type differs from that field.
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
 * Translate the neutral session-info payload into camelCase updates for AgentSessionInfo.
 * Each field has its own validation and conversion.
 * An omitted or invalid field stays absent from the result.
 * If no field remains, the caller skips the store write.
 * The exported pure function permits direct tests without a live connection.
 */
export function wireSessionInfoToUpdates(
  info: Record<string, unknown> | undefined,
): Record<string, unknown> {
  const updates: Record<string, unknown> = {}
  if (!info)
    return updates
  // Read each indexed value once before checking its type.
  // The local value lets TypeScript retain the type that the guard establishes.
  // A repeated read through these generated table properties does not retain that narrower type.
  // wireRateLimitsToCamel instead uses a typed picker for each field.
  // These two fields use local validation because updates accepts unknown values.
  // That target record does not check each field's semantic type.
  const totalCostUsd = info[SESSION_INFO_KEY.TotalCostUsd]
  if (typeof totalCostUsd === 'number')
    updates.totalCostUsd = totalCostUsd
  const contextUsage = normalizeContextUsage(info[SESSION_INFO_KEY.ContextUsage])
  if (contextUsage)
    updates.contextUsage = contextUsage
  return updates
}

/** Decode one explicit rate-limit merge or replacement from session info. */
export function wireRateLimitUpdateFromSessionInfo(
  info: Record<string, unknown> | undefined,
): RateLimitUpdate | undefined {
  if (!info)
    return undefined
  const update = info[SESSION_INFO_KEY.RateLimits]
  if (!isObject(update))
    return undefined
  const values = wireRateLimitsToCamel(update[RATE_LIMIT_UPDATE_FIELD.Values])
  const mode = update[RATE_LIMIT_UPDATE_FIELD.Mode]
  if (values === undefined || (mode !== RATE_LIMIT_UPDATE_MODE.Merge && mode !== RATE_LIMIT_UPDATE_MODE.Replace))
    return undefined
  return { mode, values }
}

function wireGenerationProgress(info: Record<string, unknown> | undefined): LiveGenerationProgress | null {
  const revision = info?.[SESSION_INFO_KEY.GenerationProgressRevision]
  if (typeof revision !== 'number' || !Number.isSafeInteger(revision) || revision <= 0)
    return null
  const progress: LiveGenerationProgress = { revision }
  const thinkingTokens = info?.[SESSION_INFO_KEY.ThinkingTokens]
  if (typeof thinkingTokens === 'number' && thinkingTokens > 0)
    progress.thinkingTokens = thinkingTokens
  const outputBytes = info?.[SESSION_INFO_KEY.OutputBytes]
  if (typeof outputBytes === 'number' && outputBytes > 0) {
    progress.output = {
      bytes: outputBytes,
      minimum: info?.[SESSION_INFO_KEY.OutputBytesMinimum] === true,
    }
  }
  return progress
}

/**
 * Translate a running_tool broadcast into the chat store's accumulating span state.
 * The exported pure function permits direct wire-boundary tests.
 * wireSessionInfoToUpdates handles scalar AgentSessionInfo fields separately.
 *
 * The span ID and native session ID together identify the tool row.
 * Other fields can be absent because heartbeat and retry events report different facts.
 * The retry field has three outcomes:
 * - An absent or unreadable value retains the current retry.
 * - An object supplies a retry update.
 * - An explicit null clears the retry.
 *
 * Retain only the badge's fields. The tool row already supplies the tool name and subagent type.
 */
export function wireRunningToolToUpdate(value: unknown): ToolProgressUpdate | undefined {
  if (!isObject(value))
    return undefined
  const spanId = pickString(value, RUNNING_TOOL_FIELD.SpanId)
  if (spanId === '')
    return undefined

  // An absent session becomes an empty string, as it does in messageSpanKey.
  // The progress update and its row therefore use the same key.
  const update: ToolProgressUpdate = { spanId, agentSessionId: pickString(value, RUNNING_TOOL_FIELD.AgentSessionId) }
  // Use a finite, non-negative elapsed time.
  // A non-finite value produces an invalid duration label, and a negative value is not a duration.
  const elapsed = pickCounter(value, RUNNING_TOOL_FIELD.ElapsedSeconds)
  if (elapsed !== undefined)
    update.elapsedSeconds = elapsed
  if (RUNNING_TOOL_FIELD.Retry in value) {
    const retry = wireRunningToolRetry(value[RUNNING_TOOL_FIELD.Retry])
    // Omit an unreadable retry so the store retains its current value.
    // Only an explicit null clears the retry.
    if (retry !== undefined)
      update.retry = retry
  }
  // An empty output tail is a real value for a command that produced no output.
  // Check the field's presence and type instead of its truthiness.
  if (RUNNING_TOOL_FIELD.OutputTail in value && typeof value[RUNNING_TOOL_FIELD.OutputTail] === 'string') {
    update.outputTail = value[RUNNING_TOOL_FIELD.OutputTail] as string
    update.outputTruncated = value[RUNNING_TOOL_FIELD.OutputTruncated] === true
  }
  return update
}

/**
 * Read the supplied goal_progress counters, or return undefined when none is valid.
 * Read each counter independently because providers can supply different subsets.
 * An absent counter stays absent. An invented zero would report a value that the provider did not supply.
 * Require finite, non-negative numbers so the card shows no invalid count or duration.
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
 * Read the retry field of a running_tool update.
 * An explicit null clears the badge. An object updates it.
 * An unreadable value returns undefined and retains the last known retry.
 * A partial or changed wire shape must not erase a live badge or display an invented attempt count.
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
 * Supply explicit stores to the module-level message handlers.
 * Each handler can then run independently without a closure over the connection hook.
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
 * Consume an ephemeral agent_session_info message that the worker does not persist.
 * Translate the neutral snake_case payload into the frontend stores' camelCase fields at this boundary.
 * Return true after this handler consumes the message, including when its replay receipt refuses effects.
 * The caller then skips the persisted-message path.
 */
export function handleAgentSessionInfo(
  agentId: string,
  parsed: ParsedMessageContent,
  stores: Pick<AgentMessageStores, 'agentSessionStore' | 'chatStore'>,
  delivery: SessionMetadataDelivery = { phase: 'live' },
  transcriptOnly = false,
): boolean {
  if (!(parsed.topLevel !== null && !parsed.wrapper && parsed.topLevel.type === NOTIFICATION_TYPE.AgentSessionInfo))
    return false
  const { agentSessionStore, chatStore } = stores
  if (transcriptOnly || (delivery.phase === 'replay' && !agentSessionStore.acceptsReplay(agentId, delivery.replayId ?? 0n)))
    return true
  const info = parsed.topLevel.info as Record<string, unknown> | undefined
  const updates = wireSessionInfoToUpdates(info)
  const rateLimits = wireRateLimitUpdateFromSessionInfo(info)
  const generationProgress = wireGenerationProgress(info)
  if (generationProgress)
    agentSessionStore.applyProgress(agentId, generationProgress)
  // running_tool accumulates per-span state in the chat store.
  // Keep it outside the scalar AgentSessionInfo translation.
  const runningTool = wireRunningToolToUpdate(info?.[SESSION_INFO_KEY.RunningTool])
  if (runningTool)
    chatStore.applyToolProgress(agentId, runningTool)
  // Keep goal_progress beside the goal in the chat store.
  // Its ephemeral updates can occur after each tool call without a goal transition.
  // AgentGoalChanged carries the goal and supported actions separately.
  const goalProgress = wireGoalProgressToUpdate(info?.[SESSION_INFO_KEY.GoalProgress])
  if (goalProgress) {
    const accepted: GoalProgress = {}
    for (const field of Object.keys(goalProgress) as (keyof GoalProgress)[]) {
      if (agentSessionStore.claimGoalProgressWrite(agentId, field, delivery))
        assignDefined(accepted, field, goalProgress[field])
    }
    if (Object.keys(accepted).length > 0)
      chatStore.goal.setProgress(agentId, accepted)
  }
  if (rateLimits)
    updates.rateLimits = rateLimits.values
  // Skip the scalar store write when no translated field remains.
  // This avoids a reactive update for unsupported or unrelated session-info keys.
  if (Object.keys(updates).length > 0) {
    agentSessionStore.updateInfo(agentId, updates, {
      delivery,
      ...(rateLimits ? { rateLimits: { mode: rateLimits.mode } } : {}),
    })
  }
  return true
}

/** The explicit delivery phase controls live effects. Replay must preserve a manually edited tab title. */
export type CatchUpPhase = 'catchingUp' | 'live'

function messageMetadataDelivery(msg: AgentChatMessage, phase: CatchUpPhase, replayId: bigint): SessionMetadataDelivery {
  return { phase: phase === 'live' ? 'live' : 'replay', replayId, seq: msg.seq }
}

/**
 * Read notification metadata independently of the message's source.
 * Neutral notifications match their explicit type, and provider hooks refuse frames that they do not recognize.
 * The compaction scan asks the row's provider for its boundary and returns undefined when none exists.
 * Usage extraction additionally requires an AGENT-source row.
 */
export function applyNotificationMetadata(agentId: string, msg: AgentChatMessage, parsed: ParsedMessageContent, stores: AgentMessageStores, catchUpPhase: CatchUpPhase, replayId = 0n): void {
  if (msg.transcriptOnly || parsed.topLevel === null)
    return
  const { agentSessionStore, chatStore, metadata } = stores
  const delivery = messageMetadataDelivery(msg, catchUpPhase, replayId)
  const plugin = providerFor(msg.agentProvider)
  const innerMsg = getInnerMessage(parsed)
  const innerType = innerMsg?.type as string | undefined

  if (innerType === NOTIFICATION_TYPE.ContextCleared) {
    agentSessionStore.clearContextUsage(agentId, delivery)
    // A live clear removes indicators whose rows no longer exist.
    // Replay restores scalar history through receipts and retains canonical to-dos.
    if (catchUpPhase === 'live') {
      chatStore.todos.clear(agentId)
      clearPerTurnLiveState(agentId, stores)
    }
  }

  // Let the provider hook identify its rate-limit frame.
  // This shared handler reads no provider-specific wire token.
  const rls = plugin?.session?.rateLimitsFromMessage?.(parsed)
  if (rls) {
    agentSessionStore.updateInfo(agentId, { rateLimits: rls.values }, {
      delivery,
      rateLimits: { mode: rls.mode },
    })
  }

  // Extract context usage and cumulative cost once for an AGENT-source row.
  // The neutral extractor validates cost and context usage and excludes child usage.
  // The provider hook reads its own native usage shape.
  // The source guard prevents a USER or LEAPMUX row from changing usage through similar fields.
  if (msg.source === MessageSource.AGENT) {
    const resolved = resolveMessageForRendering(parsed, msg.agentProvider)
    const usage = extractContextUsage(resolved, p => plugin?.session?.contextUsageFromMessage?.(p) ?? null)
    if (usage)
      agentSessionStore.updateInfo(agentId, usage, { delivery })
  }

  // Use a completed compaction boundary's post-compaction count to replace stale context usage.
  // The boundary can supply the count directly or through the provider's saved-token calculation.
  // Reset the input and cache fields because the boundary supplies no breakdown.
  // Retain the known context window so the usage percentage keeps its denominator.
  // The row's provider identifies the boundary. An unrelated message supplies no count.
  const postTokens = compactionContextTokens(parsed, msg.agentProvider)
  if (postTokens !== undefined) {
    const existing = agentSessionStore.getInfo(agentId).contextUsage
    agentSessionStore.updateInfo(agentId, {
      contextUsage: compactionContextUsage(postTokens, existing),
    }, { delivery })
  }

  if (catchUpPhase === 'live' && innerType === NOTIFICATION_TYPE.SettingsChanged) {
    const sc = extractSettingsChanges(parsed)
    if (sc)
      emitSettingsChanged(sc)
  }

  // A notification wrapper can contain plan_execution or plan_updated beside other types.
  // Scan every wrapper. For an unwrapped message, scan only the matching inner type.
  if (parsed.wrapper !== null || innerType === NOTIFICATION_TYPE.PlanExecution) {
    const planFile = extractPlanFilePath(parsed)
    if (planFile)
      agentSessionStore.updateInfo(agentId, { planFilePath: planFile }, { delivery })
  }
  if (parsed.wrapper !== null || innerType === NOTIFICATION_TYPE.PlanUpdated) {
    const planUpdate = extractPlanUpdated(parsed)
    if (planUpdate) {
      if (planUpdate.planFilePath)
        agentSessionStore.updateInfo(agentId, { planFilePath: planUpdate.planFilePath }, { delivery })
      // A live plan can update the tab title. Replay must preserve a manual title.
      // The plan file path restores independently through the scalar receipt.
      if (catchUpPhase === 'live' && planUpdate.updateAgentTitle && planUpdate.planTitle)
        metadata.patch(agentId, { title: planUpdate.planTitle })
    }
  }
}

/**
 * Handle a row that the provider plugin classifies as result_divider.
 * Clear live per-turn indicators only for live delivery.
 * Restore scalar result metadata through the exact delivery receipt during live delivery and replay.
 * The worker's activity transition controls the alert and badge through handleAgentSettled.
 * A separate divider alert would notify twice or report completion while a subagent still runs.
 */
export function handleResultDivider(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  stores: AgentMessageStores,
  catchUpPhase: CatchUpPhase,
  replayId = 0n,
): void {
  if (msg.transcriptOnly)
    return
  const { agentSessionStore, view } = stores
  const delivery = messageMetadataDelivery(msg, catchUpPhase, replayId)
  // A current live divider clears every indicator, regardless of its source.
  // This includes a tool whose result never arrived after interruption or process failure.
  // Historical dividers retain the replacement turn's indicators.
  if (catchUpPhase === 'live')
    clearPerTurnLiveState(agentId, stores)
  // Use the confirmed catalog model to resolve the context-window hint.
  // An optimistic model value can still identify the default sentinel or a model whose relaunch did not occur.
  // That value could select the wrong context window for the completed turn.
  const modelId = optionGroup(view.getAgentTab(agentId)?.optionGroups, OPTION_ID_MODEL)?.currentValue
  // handleAgentSettled owns the activity alert.
  // A turn divider does not establish that every subagent stopped.
  const meta = extractResultMetadata(resolveMessageForRendering(parsed, msg.agentProvider), modelId)
  if (!meta)
    return
  if (meta.contextUsage) {
    agentSessionStore.updateInfo(agentId, { contextUsage: meta.contextUsage }, { delivery })
  }
  else if (meta.contextWindow !== undefined) {
    const existingUsage = agentSessionStore.getInfo(agentId).contextUsage
    if (existingUsage) {
      agentSessionStore.updateInfo(agentId, {
        contextUsage: { ...existingUsage, contextWindow: meta.contextWindow },
      }, { delivery })
    }
  }
  if (meta.totalCostUsd !== undefined) {
    agentSessionStore.updateInfo(agentId, { totalCostUsd: meta.totalCostUsd }, { delivery })
  }
}

/**
 * Clear a tool's live progress when its result row arrives.
 * For example, Claude Code sends periodic heartbeats without a separate progress-clear event when the tool stops.
 * Use the provider plugin's spanRole hook to identify the result row.
 * This keeps provider parsing at the browser's extraction boundary.
 *
 * Clear progress even when the loaded window excludes or removed the result row.
 * These events remove any remaining progress:
 * - Turn completion.
 * - INACTIVE status.
 * - A context clear.
 */
export function dropFinishedToolProgress(
  agentId: string,
  msg: AgentChatMessage,
  parsed: ParsedMessageContent,
  chatStore: AgentMessageStores['chatStore'],
): void {
  if (!msg.spanId)
    return
  // Use both the native session ID and span ID, as the progress writer and reader do.
  if (resolvedSpanRole(parsed, msg.agentProvider) === 'result')
    chatStore.dropToolProgress(agentId, messageSpanIdentity(msg))
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
 * Process an agentMessage frame through these steps:
 * - Consume ephemeral session info.
 * - Apply notification metadata.
 * - Append the stored message and trim background history.
 * - Clear completed tool progress for live delivery.
 * - Apply result-divider metadata and live cleanup.
 * The caller marks live activity before this handler, except for transcript-only delivery.
 * retain-transcript preserves the received row and its bytes without effects on current state.
 * Ephemeral session info still creates no transcript row.
 */
export function handleAgentMessage(
  agentId: string,
  msg: AgentChatMessage,
  stores: AgentMessageStores,
  catchUpPhase: CatchUpPhase,
  replayId = 0n,
  effectMode: 'apply-current-state' | 'retain-transcript' = 'apply-current-state',
): void {
  const { chatStore, view, selection } = stores
  const applyCurrentState = effectMode === 'apply-current-state' && !msg.transcriptOnly

  // Parse the received bytes once and share the result across the message handlers.
  // A parse failure yields an empty parsed value, so handlers that require a JSON object do nothing.
  const parsed = parseMessageContent(msg)

  // Consume ephemeral agent_session_info before the persisted-message path.
  // That payload creates no transcript row.
  if (handleAgentSessionInfo(agentId, parsed, stores, messageMetadataDelivery(msg, catchUpPhase, replayId), !applyCurrentState))
    return

  // Apply notification metadata independently of transcript storage and display.
  if (applyCurrentState)
    applyNotificationMetadata(agentId, msg, parsed, stores, catchUpPhase, replayId)

  chatStore.addMessage(agentId, msg)
  // A tool's result row means that the tool stopped. Clear its live progress and badge.
  if (applyCurrentState && catchUpPhase === 'live')
    dropFinishedToolProgress(agentId, msg, parsed, chatStore)
  // Trim history only when this agent is not its tile's selected tab.
  // Compare the selected key with this agent's exact key.
  // A truthiness check would treat every tile with any selected tab as visible and disable the history limit.
  if (
    selection.activeKeyForTile(view.getAgentTab(agentId)?.tileId ?? '')
    !== tabKey({ type: TabType.AGENT, id: agentId })
    && chatStore.getMessages(agentId).length > MAX_BACKGROUND_CHAT_MESSAGES
  ) {
    chatStore.trimOldestEnd(agentId, MAX_BACKGROUND_CHAT_MESSAGES)
  }
  if (!applyCurrentState)
    return

  // Classify the row before its result-divider decision.
  const category = classifyAgentMessage(msg)

  // Restore scalar result metadata and clear live indicators through handleResultDivider.
  // The provider plugin supplies result_divider classification.
  // The worker's activity transition controls the alert and badge separately.
  if (category.kind === 'result_divider')
    handleResultDivider(agentId, msg, parsed, stores, catchUpPhase, replayId)
}

/**
 * Retain the optimistic value for each pending settings axis. Use the server value for every other axis.
 * A pending axis absent from prevValues is a local clear and must remain absent.
 * useAgentOperations deletes that key before it marks the axis pending.
 * Return the serverValues object unchanged when no axis is pending or no previous values exist.
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
 * Reconcile a reported settings catalog into the tab's per-axis fields:
 * - An empty catalog supplies no update.
 * - Reuse each unchanged group's reference through mergeStableOptionGroupRefs.
 * - Retain optimistic values for pending axes through applyPendingAxisSuppression.
 *
 * tabMetadata.patch suppresses a complete object write when its value is unchanged.
 * The group helper also preserves unchanged elements inside an array that contains a changed group.
 * That separate responsibility prevents unchanged settings rows from losing their identity.
 * The caller updates the label cache at the ingestion boundary. This helper has no side effect.
 */
export function resolveSettingsTabFields(
  prev: AgentTab | undefined,
  optionGroups: AvailableOptionGroup[],
  pendingAxes: ReadonlySet<string>,
): Partial<AgentTab> {
  if (optionGroups.length === 0)
    return {}
  const fields = deriveOptionGroupTabFields(optionGroups)
  // Reuse each unchanged group's previous reference after decoding fresh protobuf objects.
  // A changed effort group must preserve an unchanged model group's identity.
  if (fields.optionGroups && prev?.optionGroups)
    fields.optionGroups = mergeStableOptionGroupRefs(fields.optionGroups, prev.optionGroups)
  // Apply the confirmed catalog and retain pending optimistic values separately.
  if (fields.optionValues)
    fields.optionValues = applyPendingAxisSuppression(fields.optionValues, prev?.optionValues, pendingAxes)
  return fields
}

/**
 * Build one tab update for a statusChange event.
 * Include status and native session ID only when the event supplies a non-UNSPECIFIED status.
 * This prevents a git-only update from replacing valid state with protobuf defaults.
 * Apply startup transitions and the previously reconciled settings fields. Include the reported repository identity also.
 *
 * The caller writes this complete update once.
 * tabMetadata.patch suppresses unchanged object values at the shared write boundary.
 * This pure helper requires no previous tab or per-producer equality checks.
 */
export function buildAgentStatusTabUpdate(
  sc: AgentStatusChange,
  hasStatus: boolean,
  settingsFields: Partial<AgentTab>,
): Partial<AgentTab> {
  return {
    ...(hasStatus ? { agentStatus: sc.status, agentSessionId: sc.agentSessionId } : {}),
    ...(hasStatus ? { supportsSteering: sc.supportsSteering, supportsPreemption: sc.supportsPreemption } : {}),
    // Set the server's startup error on STARTUP_FAILED and clear it on ACTIVE.
    // Other status values retain the existing error.
    ...(sc.status === AgentStatus.STARTUP_FAILED ? { startupError: sc.startupError } : {}),
    ...(sc.status === AgentStatus.ACTIVE ? { startupError: '' } : {}),
    // Show the startup phase while the status is STARTING.
    // Clear it on another explicit status. Retain it when the event supplies no status.
    ...(sc.status === AgentStatus.STARTING
      ? { startupMessage: sc.startupMessage }
      : hasStatus ? { startupMessage: '' } : {}),
    // Include the confirmed catalog and the current values after pending-axis suppression.
    ...settingsFields,
    // The tab stores repository identity. repoGitStore stores the complete git state.
    ...(sc.gitStatus?.toplevel ? { gitToplevel: sc.gitStatus.toplevel } : {}),
  }
}

/** Clear live turn state and unanswered prompts. Keep delivered responses until recording finishes. */
export function handleAgentInactive(
  agentId: string,
  sc: AgentStatusChange,
  catchUpPhase: CatchUpPhase,
  // Reuse the message stores and add the control store that this handler requires.
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
): void {
  stores.controlStore.clearProviderRequests(agentId)
  clearPerTurnLiveState(agentId, stores)
  // A process exit changes the worker's activity state. handleAgentSettled owns the resulting alert.
  // An INACTIVE status does not establish a turn boundary.
  // onTurnEndRefresh owns the git and directory-tree refresh after a turn ends.
}

/** Cancel the provider prompt without discarding a delivered response that still needs recording. */
export function handleControlCancellation(request: AgentControlCancelRequest, controlStore: ReturnType<typeof createControlStore>): void {
  controlStore.cancelRequest(request)
}

/** Apply a request or response-state snapshot. Replay retains recoverable responses without a new notification. */
export function handleControlRequest(
  agentId: string,
  cr: AgentControlRequest,
  catchUpPhase: CatchUpPhase,
  stores: AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> },
): void {
  const { view, metadata, selection, getActiveWorkspaceId, controlStore } = stores
  if (cr.responseState === ControlResponseState.COMPLETED || cr.responseState === ControlResponseState.CANCELED) {
    controlStore.cancelRequest(cr)
    return
  }
  // A saved response can still need local recording after its provider stops.
  const agentEntry = view.getAgentTab(cr.agentId)
  const hasSavedResponse = cr.responseState === ControlResponseState.DELIVERED
    || cr.responseState === ControlResponseState.PENDING || cr.responseState === ControlResponseState.UNCERTAIN
  if (catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE && !hasSavedResponse)
    return
  // Retain an unreadable control request because the provider still waits for its answer.
  // The view must show these items:
  // - The request.
  // - Its failure reason.
  // - The Stop control.
  // Use an empty decoded payload and preserve the original bytes instead of inventing a provider shape.
  let payload: Record<string, unknown> = {}
  let payloadFault: ControlPayloadFault | undefined
  try {
    const parsed = JSON.parse(TEXT_DECODER.decode(cr.payload)) as unknown
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      log.warn('Keeping a control request whose payload is not an object', { agentId: cr.agentId, requestId: cr.requestId })
      payloadFault = 'not-an-object'
    }
    else {
      payload = parsed as Record<string, unknown>
    }
  }
  catch (err) {
    log.warn('Keeping a control request whose payload is malformed', { agentId: cr.agentId, requestId: cr.requestId, err })
    payloadFault = 'malformed'
  }
  controlStore.addRequest(cr.agentId, { requestId: cr.requestId, agentId: cr.agentId, agentSessionId: cr.agentSessionId, payload, ...(payloadFault ? { payloadFault } : {}), originalPayload: cr.payload, agentProvider: cr.agentProvider, claimToken: cr.claimToken, sourceSeq: cr.sourceSeq, responseState: cr.responseState })
  if (catchUpPhase === 'live') {
    // Set the hidden tab's badge when its agent needs an answer.
    // Use the same tile visibility rule as FULL watch mode.
    if (!isAgentTabOnScreen(cr.agentId, view, selection, getActiveWorkspaceId))
      metadata.patch(cr.agentId, { hasNotification: true })
    // A control request can change WORKING to WAITING_FOR_USER.
    // handleAgentSettled plays that alert once.
    // onTurnEndRefresh refreshes git and the directory tree after the turn ends.
    // Let the activity handler send the alert. Let the turn-end handler request the refresh.
  }
}

/**
 * Require an existing agent tab in the active workspace and its tile's selected key.
 * A mounted tab can remain hidden behind another tab in the same tile.
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
 * Apply the Worker's activity state and notify on its settled transition.
 * The store detects the transition before the sound callback runs.
 * Initial and repeated idle reports create no notification.
 * A live transition still notifies during replay.
 * CatchUpStart supplies the replay baseline through AgentActivityStore.seedPublished.
 * Report the browser receipt after the synchronous sound callback returns.
 */
export function handleActivityChanged(
  agentId: string,
  // The proto message supplies an optional numToolUses field.
  value: { state: AgentActivityState, numToolUses?: number | undefined },
  stores: Pick<AgentMessageStores, 'metadata' | 'selection' | 'getActiveWorkspaceId' | 'view'> & {
    agentActivityStore: AgentActivityStore
    onAgentSettled?: (agentId: string, numToolUses?: number) => void
  },
): void {
  if (!stores.agentActivityStore.apply(agentId, value.state))
    return
  handleAgentSettled(agentId, value.numToolUses, stores)
  const detail: AgentSettledEventDetail = {
    agentId,
    state: value.state,
    ...(value.numToolUses === undefined ? {} : { numToolUses: value.numToolUses }),
  }
  window.dispatchEvent(new CustomEvent(AGENT_SETTLED_EVENT, { detail }))
}

/**
 * Set the hidden tab's badge and invoke the activity alert after the worker reports that the agent settled.
 * A turn can end while a subagent still runs, so a turn-end event alone must not trigger this alert.
 * A prompt or process exit can produce a transition out of WORKING.
 *
 * numToolUses identifies the completed turn's tool count when the provider supplies it.
 * An absent count permits an alert.
 * A prompt or process exit can omit the count. A provider can also omit it.
 * An explicit zero lets the sound callback suppress an alert for a turn with no tool use.
 *
 * The caller reaches this handler only after a live activity transition, including while replay occurs.
 * CatchUpStart supplies the replay baseline through AgentActivityStore.seedPublished without an alert.
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
 * Apply the worker's status snapshot to the agent tab:
 * - Reconcile the confirmed settings catalog and pending optimistic values.
 * - Apply one consolidated metadata update.
 * - Stop the settings spinner when this agent has no pending change.
 * - Clear live turn state after INACTIVE status.
 *
 * Only an explicit status makes workerOnline authoritative.
 * Ignore an event that supplies none of these fields:
 * - An explicit status.
 * - Git data.
 * - Settings groups.
 * The worker owns the durable input queue and dispatches its queued input when the provider becomes ready.
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
  // Only an explicit status makes workerOnline authoritative.
  // A sparse event can contain protobuf's default false without a connectivity report.
  if (hasStatus)
    setWorkerOnline(sc.workerOnline)

  // Skip an event that supplies none of these fields:
  // - An explicit status.
  // - Git data.
  // - Settings groups.
  // Continuous reconcileLaggingTails handles catch-up independently.
  // An empty status event must not allocate an update or notify reactive readers.
  const hasPayload = hasStatus || sc.gitStatus !== undefined || sc.optionGroups.length > 0
  if (!hasPayload)
    return

  // Stop the aggregate settings spinner only when this agent has no pending change.
  // The separate pendingAxes set protects optimistic values for each axis.
  const pendingSettings = settingsLoading.isPending(sc.agentId)
  applyAgentStatusTabUpdate(sc, stores, settingsLoading, streamWorkerId)
  if (!pendingSettings)
    settingsLoading.stop()
  if (sc.status === AgentStatus.INACTIVE)
    handleAgentInactive(agentId, sc, catchUpPhase, stores)
}

/**
 * Apply the status event's settings and repository data.
 * Apply its tab fields in the same metadata update.
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
  const migrateHint = prev
    ? migrateErrorHintFromForResolvedRepo(workerId, prev, sc.gitStatus)
    : undefined
  upsertRepoGitFromProtoStatus(repoGitStore, workerId, sc.gitStatus, migrateHint !== undefined
    ? { migrateErrorHintFrom: migrateHint }
    : {})
  const settingsFields = resolveSettingsTabFields(prev, sc.optionGroups, settingsLoading.pendingAxes(sc.agentId))
  // Write the consolidated tab update once.
  // Record live status and catalog writes in their respective epochs.
  // A pending ListAgents response then cannot replace the newer live fields.
  // A git-only event changes neither epoch.
  // See TabMetadataStore.liveStatusEpoch and TabMetadataStore.liveCatalogEpoch.
  const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
  const update = buildAgentStatusTabUpdate(sc, hasStatus, settingsFields)
  const writes: LiveWrite[] = []
  if (hasStatus)
    writes.push('status')
  if (sc.optionGroups.length > 0)
    writes.push('catalog')
  metadata.patchLive(sc.agentId, update, writes)
}
