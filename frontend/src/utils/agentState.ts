import type { AgentChatMessage, AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { TabWorkState } from '~/stores/chatBackgroundTasks'
import { classifyAgentMessage } from '~/components/chat/messageClassification'
import { allRegisteredProviders, pluginFor } from '~/components/chat/providers/registry'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { getInnerMessage, getInnerMessageType, parseMessageContent } from '~/lib/messageParser'
// Side-effect import: ensures every provider has registered itself before
// `allRegisteredProviders()` is consulted to aggregate non-progress
// types/methods. Without this, `agentState.ts` may be evaluated before
// `providers/index.ts` and miss provider contributions.
import '~/components/chat/providers'

/**
 * Inner-message `type` values that don't represent agent progress for the
 * working-state heuristic. The base set covers worker-injected platform
 * notifications and provider-agnostic agent lifecycle events. Providers
 * extend this set via `Provider.nonProgressTypes` (e.g. Pi compaction /
 * auto-retry / extension events).
 *
 * `context_cleared` is intentionally absent: when it appears inside a
 * notification-thread wrapper it is a turn boundary handled by
 * `containsContextCleared`; when it appears as a USER/AGENT plain payload
 * it must not be skipped (the payload carries user/agent content).
 */
const BASE_NON_PROGRESS_TYPES: ReadonlySet<string> = new Set<string>([
  NOTIFICATION_TYPE.SettingsChanged,
  NOTIFICATION_TYPE.Interrupted,
  NOTIFICATION_TYPE.PlanExecution,
  NOTIFICATION_TYPE.PlanUpdated,
  NOTIFICATION_TYPE.AgentError,
  NOTIFICATION_TYPE.AgentSessionInfo,
  NOTIFICATION_TYPE.Compacting,
  NOTIFICATION_TYPE.RateLimit,
  NOTIFICATION_TYPE.RateLimitEvent,
])

let cachedNonProgressTypes: Set<string> | null = null
let cachedNonProgressMethods: Set<string> | null = null

/**
 * Aggregate the base set with each provider's `nonProgressTypes`
 * contribution. Cached on first call; safe because provider registration
 * happens at module-load time and the registry is append-only.
 */
function nonProgressTypes(): Set<string> {
  if (cachedNonProgressTypes)
    return cachedNonProgressTypes
  const aggregated = new Set<string>(BASE_NON_PROGRESS_TYPES)
  for (const plugin of allRegisteredProviders()) {
    if (!plugin.nonProgressTypes)
      continue
    for (const t of plugin.nonProgressTypes)
      aggregated.add(t)
  }
  cachedNonProgressTypes = aggregated
  return aggregated
}

/**
 * Aggregate `nonProgressMethods` across every registered provider.
 * The base set is empty (no provider-agnostic JSON-RPC methods exist);
 * Codex contributes its hidden-lifecycle methods plus the metadata-only
 * notifications (mcp startup, rate limits, thread compaction).
 */
function nonProgressMethods(): Set<string> {
  if (cachedNonProgressMethods)
    return cachedNonProgressMethods
  const aggregated = new Set<string>()
  for (const plugin of allRegisteredProviders()) {
    if (!plugin.nonProgressMethods)
      continue
    for (const m of plugin.nonProgressMethods)
      aggregated.add(m)
  }
  cachedNonProgressMethods = aggregated
  return aggregated
}

function isNonProgressInner(inner: Record<string, unknown> | null | undefined): boolean {
  if (!inner)
    return false
  const type = inner.type
  if (typeof type === 'string' && nonProgressTypes().has(type))
    return true
  const method = inner.method
  if (typeof method === 'string' && nonProgressMethods().has(method))
    return true
  // Claude system messages with subtype=status (covers compacting/idle) or
  // subtype=api_retry are notification-threadable lifecycle markers — see
  // backend isNotificationThreadable.
  if (type === 'system') {
    const subtype = inner.subtype
    if (subtype === 'status' || subtype === 'api_retry')
      return true
  }
  return false
}

/**
 * True if `parsed` carries a `context_cleared` event at top level or anywhere
 * in its wrapper. Only notification-thread rows (the wrapper format) are
 * platform-emitted turn boundaries; USER/AGENT plain payloads that happen to
 * surface a top-level `type: "context_cleared"` (e.g. literal user text, a Pi
 * `default`-handler echo of an unknown event) carry user/agent content and
 * must not be interpreted as turn boundaries. The wrapper presence is the
 * right gate because the backend only ever produces the wrapper format from
 * PersistNotification — never for plain user/agent messages.
 */
function containsContextCleared(parsed: ParsedMessageContent): boolean {
  if (parsed.wrapper === null)
    return false
  if (getInnerMessageType(parsed) === NOTIFICATION_TYPE.ContextCleared)
    return true
  for (const m of parsed.wrapper.messages) {
    if (isObject(m) && m.type === NOTIFICATION_TYPE.ContextCleared)
      return true
  }
  return false
}

/**
 * Whether the agent is still working — the last meaningful (non-notification)
 * message is not a turn-end divider. Turn-end detection is delegated to each
 * provider's plugin, which classifies its closing envelope (Claude
 * `type:"result"`, Codex `turn/completed`, ACP `stopReason`, Pi `agent_end`)
 * as `result_divider`.
 *
 * A divider does not always end the turn. The plugin's `resultDivider` model
 * reports `turnContinues` for a run the provider restarts on its own -- Pi
 * draws one for an attempt it is about to auto-retry -- and this reports the
 * agent still working for the length of that backoff.
 */
export function isAgentWorking(msgs: AgentChatMessage[]): boolean {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const msg = msgs[i]
    const parsed = parseMessageContent(msg)
    const category = classifyAgentMessage(msg)
    // An `unsupported_provider` message is one we cannot interpret at all (no
    // registered plugin) -- it carries no signal that the agent is working, and
    // treating it as progress would pin the thinking indicator on a
    // misconfigured / version-skewed agent forever, so it stops the scan as
    // "not working".
    if (category.kind === 'unsupported_provider')
      return false
    // A turn-end divider means the agent finished -- unless the provider says
    // it restarts the run itself. Pi draws a divider for a failed attempt it is
    // about to auto-retry, and the indicator must stay up for the whole
    // backoff, where nothing streams and no other row arrives.
    if (category.kind === 'result_divider') {
      const model = pluginFor(msg.agentProvider)?.resultDivider?.(parsed.parentObject)
      return model?.turnContinues === true
    }
    // A hidden row draws nothing, so it is no evidence either way. Keep
    // scanning back for the last row that does draw. Stopping here reported
    // "working" forever on a transcript whose last row the UI folds away --
    // a Pi `agent_settled` persisted by a build before the worker dropped it.
    if (category.kind === 'hidden')
      continue
    // The subagent-end divider closes one subagent RUN: the worker writes one
    // each time that subagent's registry row reaches a final status.
    // `subagent_ended` is not a non-progress type, so without this guard the
    // scan stops AT this message and reports a finished subagent as still
    // working.
    //
    // A transcript can hold several, because Claude restarts a finished subagent
    // when the parent messages it. This scan runs BACKWARDS, so it meets a
    // restarted run's messages before the divider that closed the previous run
    // and reports the subagent working again -- which is why the revive needs no
    // change here.
    if (getInnerMessageType(parsed) === NOTIFICATION_TYPE.SubagentEnded)
      return false
    // context_cleared in a notification-thread row means the agent
    // restarted with a fresh context and is now idle — stop scanning.
    if (containsContextCleared(parsed))
      return false
    // (An empty notification wrapper -- what the consolidator emits once every
    // threaded message is superseded -- needs no branch of its own: every
    // provider plugin classifies it `hidden`, so the skip above already took
    // it. The tests below still pin that a transcript of them reports idle.)
    //
    // Platform notifications, agent metadata, and provider lifecycle
    // events never indicate active work — keep scanning back.
    if (isNonProgressInner(getInnerMessage(parsed)))
      continue
    return true
  }
  return false // no messages or all notifications
}

/**
 * Whether the chat-level thinking indicator should be shown for an agent.
 * A provider's `hasActiveTurn` (e.g. Codex's explicit turn ID)
 * takes precedence over the message-history heuristic when defined, so
 * idle-but-running tabs don't show as thinking on creation and post-
 * reconnect rehydration is driven by the authoritative session-info.
 *
 * `work` is what the background-task registry can say about THIS tab. `active`
 * keeps the indicator up even when the parent turn has ended -- a running
 * subagent/shell means the agent is still working. `finished` HIDES it and
 * stops there: for a child tab the registry row is the authoritative record of
 * that subagent's life, so it outranks the MESSAGE-HISTORY heuristic, which
 * reports "working" whenever the transcript's last message is not the closing
 * divider (an interrupt notice, a tool result, a divider write that failed). It
 * does NOT outrank live evidence of a turn in flight -- streaming text, or the
 * provider's own turn check -- because a steerable subagent accepts a new
 * message after its row is final and the row never reopens.
 * `unknown` means the registry has no row to answer with, and the heuristic
 * decides. Todo counts deliberately do NOT appear here (a nonzero todo count
 * alone must not show the indicator).
 */
export function shouldShowThinkingIndicator(
  agent: AgentInfo | undefined,
  sessionInfo: AgentSessionInfo | undefined,
  msgs: AgentChatMessage[],
  streamingText: string | undefined,
  pendingControlRequests = 0,
  work: TabWorkState = 'unknown',
): boolean {
  if (!agent || agent.status !== AgentStatus.ACTIVE)
    return false
  if (pendingControlRequests > 0)
    return false
  if (work === 'active')
    return true
  if (streamingText)
    return true
  const plugin = pluginFor(agent.agentProvider)
  const override = plugin?.hasActiveTurn?.(agent, sessionInfo)
  if (override !== null && override !== undefined)
    return override
  // A finished registry row outranks the MESSAGE HISTORY, but not live evidence
  // of a turn in flight, which is why this sits BELOW `streamingText` and the
  // provider's own turn check rather than above them.
  //
  // The worker reopens the row for both providers now -- Claude when a parent
  // messages a finished subagent, Codex when a later collab call re-registers a
  // child that runs again -- so `work` returns to 'active' and the check at the
  // top of this function answers first. But not INSTANTLY: the revive is a
  // worker-side write that reaches the client asynchronously, so a restarted run
  // can be streaming while `work` still reads 'finished'. The live checks above
  // cover that window; putting this branch ahead of them left that run with no
  // thinking indicator and, because the same predicate controls Interrupt, no
  // way to cancel it.
  //
  // So this branch is the answer for a subagent that is genuinely over.
  if (work === 'finished')
    return false
  return isAgentWorking(msgs)
}
