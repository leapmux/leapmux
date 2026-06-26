import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { spanRole } from '~/components/chat/spanRole'
import { getOrCreate } from '~/lib/getOrCreate'
import { parseMessageContent } from '~/lib/messageParser'

/**
 * Window-scoped index linking a tool span's opener (tool_use) and result
 * (tool_result) messages by spanId, so a tool_use bubble can find its result
 * and vice versa. Extracted from the chat store: it owns only the two
 * id->message maps plus a per-message parse cache, with no reactive coupling.
 *
 * Routing is by message ROLE (the per-provider `spanRole` classifier), not
 * arrival order: a tool_result always files into the result map and a tool_use
 * into the opener map, so a result that arrives before its opener (out-of-order
 * live delivery, or a re-broadcast after the opener was trimmed away) can't be
 * misfiled as the opener. Other kinds that happen to share a spanId fall back to
 * first-seen-is-opener.
 */
export interface ChatSpanIndex {
  /**
   * Index one or more messages incrementally (does NOT clear first). Returns
   * true when a spanId slot was about to be reassigned to a DIFFERENT message id
   * -- the incremental update can no longer be trusted to match the window (a
   * re-broadcast opener/result under a new id, with the old instance still
   * loaded). A true return is MANDATORY-reindex, not advisory: the conflicting
   * message was ALREADY filed (the maps are left in a partially-updated state),
   * so the caller MUST `reindex` from the authoritative window to discard it.
   * The sole caller does (`if (inserted && index(...)) reindex(...)`); a future
   * caller that treats the boolean as advisory would leave a stale slot.
   */
  index: (agentId: string, ...messages: AgentChatMessage[]) => boolean
  /** Replace an agent's index with exactly `messages` (clear, then index). */
  reindex: (agentId: string, messages: AgentChatMessage[]) => void
  /** Parsed opener (tool_use) for a spanId, or undefined. Parse is cached. */
  getOpenerParsed: (agentId: string, spanId: string) => ParsedMessageContent | undefined
  /**
   * The opener (tool_use) message id for a spanId, or undefined. Lets a consumer
   * resolve the OPENER's content version from a tool_result's spanId, so an
   * in-place opener body change (which sizes the result's diff) can bust the
   * result's cached classification / height estimate.
   */
  getOpenerId: (agentId: string, spanId: string) => string | undefined
  /** Parsed result (tool_result) for a spanId, or undefined. Parse is cached. */
  getResultParsed: (agentId: string, spanId: string) => ParsedMessageContent | undefined
  /**
   * Drop the memoized parse for a message replaced in place under a stable
   * reference (the store's same-seq update). The local parse cache assumes message
   * immutability, so an in-place content swap must evict here or a paired
   * tool_use/tool_result lookup keeps returning the pre-update parse.
   */
  invalidate: (message: AgentChatMessage) => void
}

export function createSpanIndex(): ChatSpanIndex {
  // The opener (tool_use) message per spanId, and the result (tool_result).
  const openers = new Map<string, Map<string, AgentChatMessage>>()
  const results = new Map<string, Map<string, AgentChatMessage>>()

  // Per-message memoized parse. AgentChatMessage instances are immutable, so a
  // WeakMap is the natural cache: entries get GC'd whenever the store drops a
  // message. The shared cache lets a tool_result bubble reuse the parse the
  // tool_use bubble's own render already paid for.
  const parsedCache = new WeakMap<AgentChatMessage, ParsedMessageContent>()
  function parsedFor(message: AgentChatMessage): ParsedMessageContent {
    return getOrCreate(parsedCache, message, () => parseMessageContent(message))
  }

  function mapFor(store: Map<string, Map<string, AgentChatMessage>>, agentId: string): Map<string, AgentChatMessage> {
    return getOrCreate(store, agentId, () => new Map<string, AgentChatMessage>())
  }

  // True when filing `msg` into `targetStore` would leave the index inconsistent,
  // so the caller must rebuild from the authoritative window. Two cases:
  //  - the TARGET side already holds a DIFFERENT message id for this span: a
  //    same-span re-broadcast under a new id, with the old instance maybe still
  //    loaded;
  //  - this message's OWN id currently sits on the OTHER side: the span flipped
  //    classification side under the same id (e.g. a row first filed as opener via
  //    the first-seen fallback, then re-classified as a result), leaving a stale
  //    entry behind that a same-side-only check would miss.
  // The normal opener+result pairing (two DIFFERENT ids, one per side) is NOT a
  // conflict -- only a different id on the SAME side, or the SAME id on BOTH.
  function conflictsForSpan(
    targetStore: Map<string, Map<string, AgentChatMessage>>,
    otherStore: Map<string, Map<string, AgentChatMessage>>,
    agentId: string,
    msg: AgentChatMessage,
  ): boolean {
    const sameSide = targetStore.get(agentId)?.get(msg.spanId)
    if (sameSide !== undefined && sameSide.id !== msg.id)
      return true
    const otherSide = otherStore.get(agentId)?.get(msg.spanId)
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
      mapFor(target, agentId).set(msg.spanId, msg)
    }
    for (const msg of messages) {
      if (!msg.spanId)
        continue
      // parsedFor caches, so this parse is reused by a later opener/result lookup.
      // Pass the provider so the role classifier can apply the right dialect
      // (Pi marks opener/result by envelope `type`, not Anthropic content blocks).
      const role = spanRole(msg.agentProvider, parsedFor(msg))
      if (role === 'result') {
        // Always the result side, regardless of arrival order.
        fileInto(results, openers, msg)
      }
      else if (role === 'opener') {
        fileInto(openers, results, msg)
      }
      else {
        // Other kinds sharing a spanId: first-seen is the opener. But a SECOND
        // 'other' member can't be ordered by role (neither side classifies it), so
        // filing it opposite the first by arrival order is a guess that's wrong when
        // it arrived out of seq order (a result-before-opener pair both classified
        // 'other'). Flag a conflict so the caller reindexes from the authoritative,
        // seq-ordered window, where the lower-seq member correctly becomes the
        // opener. (Today no provider emits two 'other' members on one span, so this
        // is a defensive backstop, not a hot path.)
        if (!openers.get(agentId)?.has(msg.spanId)) {
          fileInto(openers, results, msg)
        }
        else {
          conflict = true
          fileInto(results, openers, msg)
        }
      }
    }
    return conflict
  }

  function reindex(agentId: string, messages: AgentChatMessage[]) {
    openers.delete(agentId)
    results.delete(agentId)
    // Rebuilding from a cleared slate: any "reassignment" index() reports here is
    // against the authoritative window itself, so the return value is irrelevant.
    if (messages.length > 0)
      index(agentId, ...messages)
  }

  // Shared lookup for the opener/result getters -- like mapFor, it takes the
  // backing store (openers or results) as its first param so both getters route
  // through one body.
  function getParsed(store: Map<string, Map<string, AgentChatMessage>>, agentId: string, spanId: string): ParsedMessageContent | undefined {
    const msg = store.get(agentId)?.get(spanId)
    return msg ? parsedFor(msg) : undefined
  }

  return {
    index,
    reindex,
    getOpenerParsed: (agentId: string, spanId: string) => getParsed(openers, agentId, spanId),
    getOpenerId: (agentId: string, spanId: string) => openers.get(agentId)?.get(spanId)?.id,
    getResultParsed: (agentId: string, spanId: string) => getParsed(results, agentId, spanId),
    invalidate: (message: AgentChatMessage) => parsedCache.delete(message),
  }
}
