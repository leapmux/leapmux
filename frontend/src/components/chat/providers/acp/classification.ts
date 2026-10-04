import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationContext, ClassificationInput } from '../registry'
import { isFinishedToolCallStatus, toolCallStatus } from '~/components/chat/model/toolCallStatus'
import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { messageCompletionFromProto } from '../../assembledMessage'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from '../../messageUtils'
import { classifyNotifications } from '../../notificationClassification'
import { unwrapACPResult } from './resultWrapper'
import { ACP_SESSION_UPDATE } from './updateVocabulary'

/**
 * True when the wrapper holds a notification thread of this family.
 *
 * This family adds no type to the base set, which is what the `undefined` states. Every
 * notification these daemons write is LeapMux's own envelope, and
 * `BASE_NOTIFICATION_TYPES` accepts each one -- `agent_error` included, because the
 * worker writes that type for every provider.
 *
 * The `system` test is a forward-compatibility guard, not live coverage. See
 * {@link isHiddenACPNotification} for the standing property that keeps it.
 */
export function isACPNotifThread(wrapper: { messages: unknown[] } | null): boolean {
  return isNotificationThreadWrapper(wrapper, undefined, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

/**
 * Per-message hidden rules for a `system` frame of this family, applied by both the
 * standalone classifier and the consolidated-thread filter. A frame hidden on its own
 * stays hidden once the worker threads it into a `notification_thread` wrapper, which
 * is the standalone/thread parity Claude and Codex keep as well. The rules hide the
 * `init` and `task_notification` lifecycle frames and a final (non-compacting)
 * status. The shared notification renderer draws none of those, so a thread of only
 * such frames would surface as a `notification` that holds no block, and the row would
 * fall back to the raw-frame card.
 *
 * NO DAEMON OF THIS FAMILY SENDS A `system` FRAME. Each answers pure JSON-RPC, and
 * no `sessionUpdate` vocabulary of theirs holds that word. The worker stores two
 * shapes of theirs byte for byte -- a JSON-RPC envelope, tagged `jsonrpc`/`method`/
 * `id`, and a session update, tagged `sessionUpdate` -- and neither shape carries a
 * top-level `type`. The registration hook says the same thing from the other side: the
 * plugins of this family supply no `notificationEntry`, because every
 * notification in those transcripts is LeapMux's own envelope.
 *
 * No transcript of this family holds such a frame today, and the guard stays because
 * the worker admits one by construction. The default branch of `handleACPOutput`
 * (backend/internal/worker/agent/providers/acp/base.go) persists one stdout line byte for byte
 * and never reads a top-level `type`. A line that carries neither `method` nor `id`
 * reaches that branch, so a `{type:"system",...}` line on a daemon's stdout becomes an
 * AGENT row that this classifier must then read. The shape is not hypothetical:
 * Cursor's own bundle builds `{type:"system",subtype:"init"}`, behind a print-mode
 * flag the worker does not pass. The cost of the guard is these few lines. The cost of
 * its absence is a raw-frame card in the transcript.
 */
function isHiddenACPNotification(m: unknown): boolean {
  if (!isObject(m) || m.type !== 'system')
    return false
  const subtype = pickString(m, 'subtype')
  if (subtype === 'init' || subtype === 'task_notification')
    return true
  return isFinalCompactingStatus(m)
}

/**
 * True when `parent` is a JSON-RPC response envelope (a `result`/`error`
 * payload with an `id` and no `method`). Shared by Codex and ACP-based
 * provider classifiers, which all hide these from the chat view.
 */
export function isJsonRpcResponseObject(parent: Record<string, unknown>): boolean {
  if ('method' in parent)
    return false
  return ('result' in parent || 'error' in parent) && ('id' in parent)
}

export interface ACPClassifyConfig {
  /**
   * Provider-specific classification of a `session/update` whose
   * `sessionUpdate` is `tool_call_update`. Returns a `tool_use` category when
   * the provider recognizes its own wire shape in the update (e.g. Goose's
   * subagent tool-request _meta), or `undefined` to let the shared classifier
   * handle it. Kept provider-neutral here; each provider supplies its own from
   * its plugin registration.
   */
  classifyToolCallUpdate?: (parent: Record<string, unknown>) => MessageCategory | undefined
  /**
   * The stop reason that a provider's own frame states for the end of a turn that
   * the agent started by itself, or undefined for any other frame. The worker stores
   * that frame as the turn-end row, as it stores a prompt response.
   */
  agentTurnEnd?: (parent: Record<string, unknown>) => string | undefined
}

export function classifyACPMessage(config: ACPClassifyConfig = {}): (input: ClassificationInput, context?: ClassificationContext) => MessageCategory {
  const hiddenSessionUpdates = new Set<string>([
    ACP_UPDATE.CurrentMode,
    ACP_SESSION_UPDATE.USAGE_UPDATE,
    ACP_SESSION_UPDATE.AVAILABLE_COMMANDS_UPDATE,
    ACP_SESSION_UPDATE.USER_MESSAGE_CHUNK,
    // The backend consumes config_option_update centrally for every ACP provider (it
    // never persists it), so hide it for all of them -- including historical rows that
    // predate the central handling and would otherwise render as an unknown message.
    ACP_SESSION_UPDATE.CONFIG_OPTION_UPDATE,
    // The runtime's own session title and modified time, which LeapMux never shows:
    // it gives its tabs their own names. One update arrives per turn, so a build that
    // persisted it wrote a raw-JSON row into every transcript -- and those rows are
    // still in the database, which is why hiding it here is not redundant with the
    // worker's drop.
    ACP_SESSION_UPDATE.SESSION_INFO_UPDATE,
  ])
  return (input: ClassificationInput, _context?: ClassificationContext): MessageCategory => {
    const parent = input.parentObject
    const wrapper = input.wrapper

    if (wrapper) {
      if (isACPNotifThread(wrapper)) {
        // Drop the per-message hidden shapes (the same ones the standalone
        // classifier hides) so a thread of only-hidden entries collapses to
        // `hidden` rather than surfacing an empty notification or raw JSON.
        const msgs = wrapper.messages.filter(m => !isHiddenACPNotification(m))
        if (msgs.length === 0)
          return { kind: 'hidden' }
        return classifyNotifications(msgs, input.agentProvider)
      }
      if (wrapper.messages.length === 0)
        return { kind: 'hidden' }
    }

    if (!parent)
      return { kind: 'unknown' }

    // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
    // classifyMessage, before any plugin?.transcript.classify runs, since it is a LeapMux-neutral shape covering
    // every ACP-based provider -- OpenCode/Kilo/Goose/Reasonix/Cursor -- at one site.)

    // Two reads of one field, and the second is not redundant. `sessionUpdate` is the
    // TOKEN every branch below compares against, so a non-string value is no token at
    // all. The LAST branch asks a different question: a row that carries a truthy
    // `sessionUpdate` is a session update of this family, malformed or not, and never
    // LeapMux's own `{content}` envelope. One read for both questions answers that
    // branch on the token, so a malformed update draws its `content` as the reader's
    // own message and drops the rest of the frame.
    const sessionUpdateValue = parent.sessionUpdate
    const sessionUpdate = pickString(parent, 'sessionUpdate')
    const type = pickString(parent, 'type')

    // agent_message_chunk and agent_thought_chunk have no case here. The worker
    // assembles a run of those chunks into ONE row that carries the shared
    // assembled-message envelope, so classifyMessage answers assistant_text or
    // assistant_thinking before this plugin runs.

    if (sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL)
      return { kind: 'tool_use' }

    if (sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE) {
      // A provider may recognize its own tool_call_update wire shape (Goose's
      // subagent tool-request _meta) before the shared status-based path runs.
      // The provider-specific shape lives in the provider plugin, not here.
      if (config.classifyToolCallUpdate) {
        const providerCategory = config.classifyToolCallUpdate(parent)
        if (providerCategory)
          return providerCategory
      }
      if (isFinishedToolCallStatus(toolCallStatus(pickString(parent, 'status'))) || messageCompletionFromProto(input.completion))
        return { kind: 'tool_use' }
      return { kind: 'hidden' }
    }

    if (sessionUpdate === ACP_SESSION_UPDATE.PLAN)
      return { kind: 'tool_use' }

    // The provider's own end of a turn that the agent started by itself comes BEFORE
    // the hidden updates. A provider can state that end on an update that the family
    // hides otherwise -- Kiro states it on a `session_info_update` -- and the worker
    // stores exactly that update as the turn-end row.
    if (config.agentTurnEnd?.(parent) !== undefined)
      return { kind: 'result_divider' }

    if (hiddenSessionUpdates.has(sessionUpdate))
      return { kind: 'hidden' }

    // Read stopReason through the shared unwrap, because a server may wrap the
    // turn fields in a native result envelope and the worker persists the answer
    // byte for byte. Require a *string* stopReason so the gate matches
    // acpResultDivider's pickString read (mirroring the Codex turn.status gate):
    // a non-string stopReason is a malformed turn-end, not a divider this
    // provider can label.
    if (typeof unwrapACPResult(parent)?.stopReason === 'string')
      return { kind: 'result_divider' }

    // The forward-compatibility guard. No daemon of this family sends a `system`
    // frame, and the worker's verbatim persist admits one anyway -- see
    // {@link isHiddenACPNotification} for the whole standing property.
    if (type === 'system') {
      if (isHiddenACPNotification(parent))
        return { kind: 'hidden' }
      return classifyNotifications([parent], input.agentProvider)
    }

    if (isPlainNotificationType(type))
      return classifyNotifications([parent], input.agentProvider)

    if (!sessionUpdateValue && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    if (isJsonRpcResponseObject(parent))
      return { kind: 'hidden' }

    return { kind: 'unknown' }
  }
}
