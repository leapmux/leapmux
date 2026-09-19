import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { resolvedSpanRole } from '~/components/chat/providers/registry'
import { getOrCreate } from '~/lib/getOrCreate'
import { parseMessageContent } from '~/lib/messageParser'
import { messageSpanKey } from '~/lib/messageSpan'

/**
 * Window-scoped index linking a tool span's request (tool_use) and result
 * (tool_result) messages by spanId, so a tool_use bubble can find its result
 * and vice versa. Extracted from the chat store: it owns only the two
 * span-to-message maps. The message parser owns the parse cache.
 *
 * Routing is by message ROLE (the per-provider `spanRole` classifier), not
 * arrival order: a tool_result always files into the result map and a tool_use
 * into the request map, so a result that arrives before its request cannot be
 * misfiled as the request. This can occur during out-of-order live delivery or
 * after the request leaves the window. Other kinds that share a spanId use the
 * first message as the request.
 */
export interface ChatSpanIndex {
  /**
   * Index one or more messages incrementally (does NOT clear first). Returns
   * true when a spanId slot was about to be reassigned to a DIFFERENT message id
   * -- the incremental update can no longer be trusted to match the window (a
   * re-broadcast request/result under a new id, with the old instance still
   * loaded). A true return is MANDATORY-reindex, not advisory: the conflicting
   * message was ALREADY filed (the maps are left in a partially-updated state),
   * so the caller MUST `reindex` from the authoritative window to discard it.
   * Each caller must inspect this result and rebuild on a conflict.
   */
  index: (agentId: string, ...messages: AgentChatMessage[]) => boolean
  /** Replace an agent's index with exactly `messages` (clear, then index). */
  reindex: (agentId: string, messages: AgentChatMessage[]) => void
  /** The request message for a spanId, or undefined. */
  getRequestMessage: (agentId: string, identity: MessageSpanIdentity) => AgentChatMessage | undefined
  /** The result message for a spanId, or undefined. */
  getResultMessage: (agentId: string, identity: MessageSpanIdentity) => AgentChatMessage | undefined

}

export function createSpanIndex(): ChatSpanIndex {
  // The request (tool_use) message per spanId, and the result (tool_result).
  const requests = new Map<string, Map<string, AgentChatMessage>>()
  const results = new Map<string, Map<string, AgentChatMessage>>()

  function mapFor(store: Map<string, Map<string, AgentChatMessage>>, agentId: string): Map<string, AgentChatMessage> {
    return getOrCreate(store, agentId, () => new Map<string, AgentChatMessage>())
  }

  // True when filing `msg` into `targetStore` would leave the index inconsistent,
  // so the caller must rebuild from the authoritative window. Two cases:
  //  - the TARGET side already holds a DIFFERENT message id for this span: a
  //    same-span re-broadcast under a new id, with the old instance maybe still
  //    loaded;
  //  - this message's OWN id currently sits on the OTHER side. The span changed
  //    classification under the same id. This can occur when the fallback first
  //    files a request and the provider later classifies it as a result.
  // The normal request and result pairing (two DIFFERENT ids, one per side) is NOT a
  // conflict -- only a different id on the SAME side, or the SAME id on BOTH.
  function conflictsForSpan(
    targetStore: Map<string, Map<string, AgentChatMessage>>,
    otherStore: Map<string, Map<string, AgentChatMessage>>,
    agentId: string,
    msg: AgentChatMessage,
  ): boolean {
    const sameSide = targetStore.get(agentId)?.get(messageSpanKey(msg))
    if (sameSide !== undefined && sameSide.id !== msg.id)
      return true
    const otherSide = otherStore.get(agentId)?.get(messageSpanKey(msg))
    return otherSide !== undefined && otherSide.id === msg.id
  }

  function index(agentId: string, ...messages: AgentChatMessage[]): boolean {
    let conflict = false
    // File `msg` into the `target` map (recording any conflict against `other`):
    // the conflict-check-then-set two-step every role branch below repeats.
    const fileInto = (
      target: Map<string, Map<string, AgentChatMessage>>,
      other: Map<string, Map<string, AgentChatMessage>>,
      msg: AgentChatMessage,
    ) => {
      conflict ||= conflictsForSpan(target, other, agentId, msg)
      mapFor(target, agentId).set(messageSpanKey(msg), msg)
    }
    for (const msg of messages) {
      if (!msg.spanId)
        continue
      // The shared message parser caches this parse for later renderer lookups.
      // Each provider identifies its request and result roles from the protocol.
      // Unknown roles use sequence order. Known results must never depend on arrival order.
      const role = resolvedSpanRole(parseMessageContent(msg), msg.agentProvider)
      if (role === 'result') {
        // Always the result side, regardless of arrival order.
        fileInto(results, requests, msg)
      }
      else if (role === 'request') {
        fileInto(requests, results, msg)
      }
      else {
        // A refreshed message keeps its side when its protocol still omits the role.
        if (requests.get(agentId)?.get(messageSpanKey(msg))?.id === msg.id) {
          fileInto(requests, results, msg)
          continue
        }
        if (results.get(agentId)?.get(messageSpanKey(msg))?.id === msg.id) {
          fileInto(results, requests, msg)
          continue
        }
        // For other kinds, the first message is the request. A second `other`
        // member has no role that orders it. Arrival order gives a wrong answer
        // when the result arrives first. Report a conflict so the caller reindexes
        // the authoritative window in sequence order. Some shapes omit status,
        // so their protocol cannot identify the role here.
        if (!requests.get(agentId)?.has(messageSpanKey(msg))) {
          fileInto(requests, results, msg)
        }
        else {
          conflict = true
          fileInto(results, requests, msg)
        }
      }
    }
    return conflict
  }

  function reindex(agentId: string, messages: AgentChatMessage[]) {
    requests.delete(agentId)
    results.delete(agentId)
    // Rebuilding from a cleared slate: any "reassignment" index() reports here is
    // against the authoritative window itself, so the return value is irrelevant.
    if (messages.length > 0)
      index(agentId, ...messages)
  }

  return {
    index,
    reindex,
    getRequestMessage: (agentId: string, identity: MessageSpanIdentity) => requests.get(agentId)?.get(messageSpanKey(identity)),
    getResultMessage: (agentId: string, identity: MessageSpanIdentity) => results.get(agentId)?.get(messageSpanKey(identity)),
  }
}
