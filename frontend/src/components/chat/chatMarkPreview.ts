import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createStore, produce, reconcile } from 'solid-js/store'
import { parseMessageContent } from '~/lib/messageParser'
import { truncatePreview } from '~/lib/textTruncate'
import { defaultMarkPreview } from './markPreviewShared'
import { classifyAgentMessage } from './messageClassification'
import { controlResponsePreviewText, parsePersistedControlResponse, resolveControlResponseDisplay } from './persistedControlResponse'
import { pluginFor } from './providers/registry'

// ---------------------------------------------------------------------------
// Scroll-rail mark preview -- extraction + cache
//
// Resolves the short plaintext snippet the rail shows when the user hovers a jump dot, and
// caches it. Two concerns, one job ("give me the preview for this mark"):
//   1. messageMarkPreviewText -- pure extraction, routed through the message's own Provider
//      plugin (`previewText`) since only the renderer layer knows each provider's raw shapes,
//      with the shared, provider-neutral `defaultMarkPreview` (markPreviewShared.ts, a leaf
//      to avoid a registry import cycle) as the fallback.
//   2. the reactive `seq -> preview text` cache + fetch-through below.
//
// A marked message is usually OUTSIDE the loaded window (the rail spans the whole
// conversation), so the preview is resolved on demand: from the loaded window when possible
// (no fetch), else via a single-message fetch (GetAgentMessage). The cache is module-global
// (keyed by `${agentId}:${seq}`) so it survives rail remounts (tile split/merge, tab switch)
// and is shared across every rail instance for the same agent. Store DATA ops (loaded-message
// lookup, single-message fetch) are injected as deps so this component-layer module never
// imports the DI'd store.
// ---------------------------------------------------------------------------

/**
 * Resolve the hover-preview text for a marked message, routed through the message's own
 * provider plugin (`Provider.previewText`) so provider-specific shapes are read by the
 * provider that owns them, with the shared neutral {@link defaultMarkPreview} as the
 * fallback for providers with no bespoke marked shape. `parseMessageContent` and
 * `classifyAgentMessage` are both WeakMap-cached on the message reference, so this is
 * cheap to call repeatedly on hover. Returns null when the content carries no previewable
 * text (the rail then shows a mark-type label instead).
 */
export function messageMarkPreviewText(message: AgentChatMessage): string | null {
  // Defensive: a provider plugin's previewText (or the parse/classify it drives) throwing
  // on a malformed message must degrade to "no preview" (null), never propagate. On the
  // synchronous loaded-window path (warmMarkPreview) an uncaught throw errors the caller's
  // warmPreview effect on every hover; on the async single-fetch path it lands in the
  // .catch that is reserved for TRANSIENT RPC failures, which would re-fetch the same dot
  // forever instead of caching '' once. Catch it here so both paths cache a label.
  try {
    const parsed = parseMessageContent(message)
    const category = classifyAgentMessage(message)
    const plugin = pluginFor(message.agentProvider)
    // A persisted control-response row resolves its preview through the SAME per-provider
    // derivation the transcript row uses (plugin.controlResponseDisplay), so the dot reads
    // identically to the row it jumps to. Degrades to the neutral behavior/generic fallback.
    if (category.kind === 'control_response') {
      const cr = parsePersistedControlResponse(parsed.parentObject)
      if (cr) {
        const display = resolveControlResponseDisplay(cr, plugin?.controlResponseDisplay)
        return truncatePreview(controlResponsePreviewText(display))
      }
    }
    // Route to the plugin's previewText when it defines one, treating its result as
    // authoritative -- including a deliberate null (no preview). Only a provider WITHOUT
    // a previewText falls back to the shared neutral extractor. (A `?? defaultMarkPreview`
    // would both re-run the default a second time -- claude's previewText already falls
    // back to it internally -- and rob a plugin of the ability to suppress a preview.)
    const previewText = plugin?.previewText
    return previewText ? previewText(category, parsed) : defaultMarkPreview(category, parsed)
  }
  catch (err) {
    console.warn('mark preview extraction failed', { id: message.id, err })
    return null
  }
}

// '' is a real cache entry meaning "resolved, but no previewable text" -- distinct
// from `undefined` ("not resolved yet"), so a resolved-empty preview shows the rail's
// mark-type label without re-fetching on every hover.
const MAX_PREVIEW_CACHE_ENTRIES_PER_AGENT = 500
const [cache, setCache] = createStore<Record<string, string>>({})
// Key -> a token identifying the CURRENT in-flight fetch, both to dedupe concurrent hovers
// on the same dot AND to fence a stale resolution: forget (agent close) drops the key, and
// a re-warm after a close/reopen issues a NEW token, so an older fetch that resolves late
// finds its token gone/superseded and does not write to the cache.
const inflight = new Map<string, number>()
let nextFetchToken = 0

function cacheKey(agentId: string, seq: bigint): string {
  return `${agentId}:${seq}`
}

function cachePrefix(agentId: string): string {
  return `${agentId}:`
}

function setCachedMarkPreview(agentId: string, seq: bigint, preview: string): void {
  const k = cacheKey(agentId, seq)
  const prefix = cachePrefix(agentId)
  setCache(produce((c) => {
    if (!(k in c)) {
      // Evict this agent's oldest previews down to the cap before inserting. ONE Object.keys
      // pass (insertion-ordered, so the agent-prefixed slice is oldest-first) serves both the
      // count and the eviction, rather than scanning the whole global cache twice per insert.
      const agentKeys = Object.keys(c).filter(existing => existing.startsWith(prefix))
      let excess = agentKeys.length - MAX_PREVIEW_CACHE_ENTRIES_PER_AGENT + 1
      for (const existing of agentKeys) {
        if (excess <= 0)
          break
        delete c[existing]
        excess--
      }
    }
    c[k] = preview
  }))
}

export interface MarkPreviewDeps {
  /** The loaded message at this seq, or undefined when it's outside the window. */
  getLoadedMessageBySeq: (agentId: string, seq: bigint) => AgentChatMessage | undefined
  /**
   * Fetch a single message by seq. Resolves `undefined` ONLY for a definitive absence
   * (no row at that seq -- deleted/reseq'd since the mark was recorded); REJECTS on a
   * transient RPC failure. The two must stay distinguishable so warmMarkPreview caches
   * '' for a real absence but retries a transient failure instead of poisoning the dot.
   */
  fetchMessageBySeq: (workerId: string, agentId: string, seq: bigint) => Promise<AgentChatMessage | undefined>
}

/**
 * Reactive read of a resolved preview: `undefined` until resolved, `''` when resolved
 * with no previewable text, otherwise the snippet. Tracks the cache so the tooltip
 * re-renders when a pending fetch lands.
 */
export function getCachedMarkPreview(agentId: string, seq: bigint): string | undefined {
  return cache[cacheKey(agentId, seq)]
}

/**
 * Ensure the preview for (agentId, seq) is resolved into the cache. Idempotent and
 * deduped: a cached key or an in-flight fetch is a no-op. Resolves synchronously from
 * the loaded window when the message is present (no fetch); otherwise fetches the single
 * message and caches its extracted preview. A DEFINITIVE miss -- the fetch resolves with
 * no row (deleted/reseq'd since the mark was recorded) -- caches `''` so the rail falls
 * back to a label without re-fetching on every hover. A TRANSIENT fetch FAILURE (the RPC
 * rejects) is deliberately left UNRESOLVED (no cache entry) so a later hover retries,
 * rather than poisoning the dot with a permanent empty preview for the rest of the session.
 */
export function warmMarkPreview(workerId: string, agentId: string, seq: bigint, deps: MarkPreviewDeps): void {
  if (seq <= 0n)
    return
  const k = cacheKey(agentId, seq)
  if (k in cache || inflight.has(k))
    return

  const local = deps.getLoadedMessageBySeq(agentId, seq)
  if (local) {
    setCachedMarkPreview(agentId, seq, messageMarkPreviewText(local) ?? '')
    return
  }

  const token = ++nextFetchToken
  inflight.set(k, token)
  // Only the CURRENT fetch for this key may write/clear -- a resolution whose token was
  // dropped by forget or superseded by a re-warm is a no-op, so it can neither re-leak an
  // entry for a closed agent nor clobber a fresher fetch's result.
  const isCurrent = () => inflight.get(k) === token
  deps.fetchMessageBySeq(workerId, agentId, seq)
    .then((msg) => {
      // A resolved-undefined message is a DEFINITIVE absence (no row at this seq) --
      // cache '' so the rail shows a label without re-fetching. A REJECTION lands in
      // .catch below and is NOT cached, so a transient failure can be retried.
      if (isCurrent())
        setCachedMarkPreview(agentId, seq, msg ? (messageMarkPreviewText(msg) ?? '') : '')
    })
    .catch(() => {
      // Transient fetch failure: leave the key UNRESOLVED (the finally drops the
      // in-flight token, so the next hover re-fetches) rather than caching '' --
      // caching here would permanently label the dot until the agent is reopened.
    })
    .finally(() => {
      if (isCurrent())
        inflight.delete(k)
    })
}

/**
 * Drop every cached preview + in-flight key for an agent. Called from the chat
 * store's forgetAgent so the module-global cache doesn't accumulate one entry per
 * (agentId, seq) ever hovered for the life of the tab -- and so a stale `''` (a
 * fetch that transiently found no row) never survives a close/reopen of the same
 * agentId to suppress the now-available preview. Dropping the in-flight token also
 * fences any pending fetch for this agent, so a late resolve can't re-leak an entry.
 */
export function forgetMarkPreview(agentId: string): void {
  const prefix = cachePrefix(agentId)
  setCache(produce((c) => {
    for (const k of Object.keys(c)) {
      if (k.startsWith(prefix))
        delete c[k]
    }
  }))
  for (const k of inflight.keys()) {
    if (k.startsWith(prefix))
      inflight.delete(k)
  }
}

/** Test-only: reset the module-global cache + in-flight set between cases. */
export function __resetMarkPreviewCacheForTest(): void {
  setCache(reconcile({}))
  inflight.clear()
}
