import type { AgentChatMessage, AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { classifyAgentMessage } from '~/components/chat/messageClassification'
import { allRegisteredProviders, providerFor } from '~/components/chat/providers/registry'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { getInnerMessage, getInnerMessageType, parseMessageContent } from '~/lib/messageParser'
import { NOTIFICATION_TYPE } from '~/lib/notificationTypes'
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
 * provider's plugin, which classifies its terminal envelope (Claude
 * `type:"result"`, Codex `turn/completed`, ACP `stopReason`, Pi `agent_end`)
 * as `result_divider`.
 */
export function isAgentWorking(msgs: AgentChatMessage[]): boolean {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const msg = msgs[i]
    // Messages with delivery errors were never sent to the agent — skip them.
    if (msg.deliveryError)
      continue

    const parsed = parseMessageContent(msg)
    const category = classifyAgentMessage(msg)
    if (category.kind === 'result_divider')
      return false
    // context_cleared in a notification-thread row means the agent
    // restarted with a fresh context and is now idle — stop scanning.
    if (containsContextCleared(parsed))
      return false
    // Empty notification wrappers are what the consolidator emits when
    // every threaded message has been superseded — no progress signal.
    if (parsed.wrapper && parsed.wrapper.messages.length === 0)
      continue
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
 */
export function shouldShowThinkingIndicator(
  agent: AgentInfo | undefined,
  sessionInfo: AgentSessionInfo | undefined,
  msgs: AgentChatMessage[],
  streamingText: string | undefined,
  pendingControlRequests = 0,
): boolean {
  if (!agent || agent.status !== AgentStatus.ACTIVE)
    return false
  if (pendingControlRequests > 0)
    return false
  if (streamingText)
    return true
  const plugin = agent.agentProvider !== undefined ? providerFor(agent.agentProvider) : undefined
  const override = plugin?.hasActiveTurn?.(agent, sessionInfo)
  if (override !== null && override !== undefined)
    return override
  return isAgentWorking(msgs)
}
