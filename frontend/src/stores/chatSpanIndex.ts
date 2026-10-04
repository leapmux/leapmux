import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { resolvedSpanRole } from '~/components/chat/providers/registry'
import { getOrCreate } from '~/lib/getOrCreate'
import { parseMessageContent } from '~/lib/messageParser'
import { messageSpanKey } from '~/lib/messageSpan'

/**
 * Pair the request and result messages in one loaded window by their span identity.
 * This index owns the two message maps. The message parser owns the parse cache.
 *
 * The provider's `spanRole` identifies each known side, regardless of arrival order.
 * A result can arrive before its request during live delivery or after the request leaves the window.
 * `none` supplies neither side. Unknown roles use the first message as the request.
 */
export interface ChatSpanIndex {
  /**
   * Index messages without first clearing the maps.
   * Return true when an update replaces a different ID or changes an existing message's side.
   * Removing a side after a message becomes `none` reports a conflict also.
   * A second unknown-role member requires a rebuild in sequence order and reports a conflict also.
   * The update changes the maps before it reports the conflict.
   * Each caller must inspect the result and rebuild from its authoritative window on a conflict.
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
  // Each agent keeps one request and one result for each span identity.
  const requests = new Map<string, Map<string, AgentChatMessage>>()
  const results = new Map<string, Map<string, AgentChatMessage>>()

  function mapFor(store: Map<string, Map<string, AgentChatMessage>>, agentId: string): Map<string, AgentChatMessage> {
    return getOrCreate(store, agentId, () => new Map<string, AgentChatMessage>())
  }

  // Report a conflict when the update would disagree with the authoritative window:
  // - The target side already holds a different message ID for this span.
  // - This message's ID occupies the other side, because its classification changed.
  // A valid pair has different IDs on different sides. That pair does not conflict.
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
    // Check the two sides before storing the message on its selected side.
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
      if (role === 'none') {
        const key = messageSpanKey(msg)
        // A same-ID role change must remove the old side and rebuild the window index.
        for (const store of [requests, results]) {
          const sides = store.get(agentId)
          if (sides?.get(key)?.id === msg.id) {
            sides.delete(key)
            conflict = true
          }
        }
        continue
      }
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
        // An unknown role uses the first message as the request.
        // Arrival order gives a wrong answer when the result arrives first.
        // Report a conflict for the second member, so the caller rebuilds its window in sequence order.
        // Some protocols omit status and cannot identify the side here.
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
    // These messages are the authoritative window, so a reported conflict requires no further rebuild.
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
