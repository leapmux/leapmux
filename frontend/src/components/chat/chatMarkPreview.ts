import type { MessageContextResolver } from './messageContextResolver'
import type { ChatRowExtraction } from './rowExtraction'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageRevision } from '~/lib/messageSpan'
import { createSignal } from 'solid-js'
import { truncatePreview } from '~/lib/textTruncate'
import { appendCompletionMarker } from './assembledMessage'
import { rowRevisionKey } from './chatRevisionKey'
import { controlResponsePreviewText } from './persistedControlResponse'
import { quotableTextForRow } from './results/rowText'
import { toolCallMeta } from './results/tools/meta'
import { extractedRow } from './rowExtraction'
import { prepareChatRow } from './rowPreparation'

// ---------------------------------------------------------------------------
// Scroll-rail mark preview -- extraction + cache
//
// Resolves the short plaintext snippet the rail shows when the user hovers a jump dot, and
// caches it. Two concerns, one job ("give me the preview for this mark"):
//   1. messageMarkPreviewText -- pure extraction, routed through the row model: a tool row
//      answers from `toolCallMeta(row).previewText()` and the prose rows from
//      `quotableTextForRow`, so the provider that owns the raw shape reads it once and the
//      rail and the Copy button cannot state two different texts for one row.
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
 * Resolve the hover-preview text for a marked message, read from the row model its own
 * provider produced -- `toolCallMeta(row).previewText()` for a tool row, `quotableTextForRow`
 * for the prose rows -- so a provider-specific shape is read by the provider that owns it.
 * Returns null when the content carries no previewable text (the rail then shows a
 * mark-type label instead).
 *
 * Through the SHARED preparation, which is what fixed three separate previews at once:
 * the payload is resolved before it is classified, so a body LeapMux recovered into
 * supplemental content reaches the dot -- a retained Codex command's output, a
 * retained Pi call's partial result, and the arguments a scheduled ZCode call omitted
 * all previewed as nothing before. `parseMessageContent` is WeakMap-cached on the
 * message reference, so this stays cheap to call repeatedly on hover.
 *
 * TaskUpdate rows read their immutable persisted snapshots through the message.
 */
export function messageMarkPreviewText(message: AgentChatMessage): string | null {
  // Defensive: the parse, the supplemental merge and the classification all run outside
  // the extraction's own guard, and a malformed message that makes any of them throw
  // must degrade to "no preview" (null) rather than propagate. On the synchronous
  // loaded-window path (warmMarkPreview) an uncaught throw breaks the caller's
  // warmPreview effect on every hover; on the async single-fetch path it lands in the
  // .catch that is reserved for TRANSIENT RPC failures, which would re-fetch the same dot
  // forever instead of caching '' once. Catch it here so both paths cache a label.
  try {
    // No `sides`, so the preparation states this message as its span's only side (see
    // `soleSide`). That is not an omission: a marked message is usually outside the
    // loaded window, so its sibling is not loaded, and resolving one would put a span
    // fetch behind every hover.
    const { extraction } = prepareChatRow(message)
    const preview = rowPreviewText(extraction)
    if (preview === null)
      return null
    return truncatePreview(appendCompletionMarker(preview, extraction.completion))
  }
  catch (err) {
    console.warn('mark preview extraction failed', { id: message.id, err })
    return null
  }
}

/**
 * The rail snippet for one row.
 *
 * A row nobody could read previews as NOTHING, and the dot then shows its mark-type
 * label. The frame itself is not a preview: the transcript draws it in a collapsed
 * card because it is the only content that row has, but a snippet of JSON under a
 * jump dot tells the reader nothing about where the dot leads.
 */
function rowPreviewText(extraction: ChatRowExtraction): string | null {
  const row = extractedRow(extraction)
  // A tool call answers from its toolbar derivation, so the rail snippet and the
  // Copy button can never state two different texts for one row. The prose rows
  // quote their own words, and a turn-end rule states its label.
  if (row?.kind === 'tool')
    return toolCallMeta(row).previewText()
  // A saved control answer previews the words its own row shows. Layer 1 ran the
  // provider's derivation, so the dot and the row it jumps to cannot disagree -- this
  // module used to run that derivation a second time.
  if (row?.kind === 'control-response')
    return controlResponsePreviewText(row.display)
  const quotable = quotableTextForRow(row)
  if (quotable !== null)
    return quotable
  if (row?.kind === 'divider')
    return row.divider.label.trim() || null
  return null
}

// '' is a real cache entry meaning "resolved, but no previewable text" -- distinct
// from `undefined` ("not resolved yet"), so a resolved-empty preview shows the rail's
// mark-type label without re-fetching on every hover.
const MAX_PREVIEW_CACHE_ENTRIES_PER_AGENT = 500

/**
 * One agent's previews, in arrival order.
 *
 * A bucket PER AGENT, so insertion, oldest-entry eviction, agent removal and lookup
 * each walk one agent's list and no other's: the flat global object this replaces
 * scanned every key the tab ever hovered -- every agent's, under every other agent's
 * prefix -- to insert one entry, and one long session made that scan the cost of a
 * hover.
 *
 * `entries` is a plain Map in insertion order, which IS the eviction order. The
 * signal is what makes a write reach the rail: reactive readers track it, every
 * mutation bumps it, and one signal per bucket is the granularity the rail needs --
 * the dots of one agent re-read their previews when any of that agent's previews
 * land, and no other agent's rail re-renders.
 */
interface MarkPreviewBucket {
  entries: Map<string, { revisionKey: string, text: string }>
  readVersion: () => number
  bump: (value?: number) => number
}

const buckets = new Map<string, MarkPreviewBucket>()

function bucketFor(agentId: string): MarkPreviewBucket {
  const existing = buckets.get(agentId)
  if (existing)
    return existing
  const [readVersion, bump] = createSignal(0)
  const bucket: MarkPreviewBucket = { entries: new Map(), readVersion, bump }
  buckets.set(agentId, bucket)
  return bucket
}

/** The bucket's entry for one seq, without tracking the version signal. */
function peekBucketEntry(agentId: string, seq: bigint): { revisionKey: string, text: string } | undefined {
  return buckets.get(agentId)?.entries.get(seq.toString())
}

function previewRevisionKey(revision: MessageRevision | undefined): string {
  return revision === undefined ? `missing:${String(revision)}` : rowRevisionKey({ own: revision })
}

function setCachedMarkPreview(agentId: string, seq: bigint, revision: MessageRevision | undefined, preview: string): void {
  const key = seq.toString()
  const bucket = bucketFor(agentId)
  if (!bucket.entries.has(key)) {
    // Evict this agent's OLDEST previews down to the cap before inserting. The
    // map iterates in insertion order, so the first keys are the oldest, and the
    // walk touches this bucket alone.
    let excess = bucket.entries.size - MAX_PREVIEW_CACHE_ENTRIES_PER_AGENT + 1
    for (const oldest of bucket.entries.keys()) {
      if (excess <= 0)
        break
      bucket.entries.delete(oldest)
      excess--
    }
  }
  bucket.entries.set(key, { revisionKey: previewRevisionKey(revision), text: preview })
  bucket.bump()
}

/**
 * Reactive read of a resolved preview: `undefined` until resolved, `''` when resolved
 * with no previewable text, otherwise the snippet. Tracks the agent's bucket, so the
 * tooltip re-renders when a pending fetch lands.
 */
export function getCachedMarkPreview(agentId: string, seq: bigint, revision?: MessageRevision): string | undefined {
  // Allocate the bucket before the first read. The open preview must subscribe to
  // its signal while the cache is cold, or the first warm cannot update the card.
  const bucket = bucketFor(agentId)
  bucket.readVersion()
  const entry = bucket.entries.get(seq.toString())
  if (entry === undefined)
    return undefined
  return revision === undefined || entry.revisionKey === previewRevisionKey(revision) ? entry.text : undefined
}

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
export async function warmMarkPreview(agentId: string, seq: bigint, messages: Pick<MessageContextResolver, 'peek' | 'message'>): Promise<void> {
  if (seq <= 0n)
    return
  const k = cacheKey(agentId, seq)
  const local = messages.peek(seq)
  const cached = peekBucketEntry(agentId, seq)
  if (cached !== undefined && (local === undefined || cached.revisionKey === previewRevisionKey(local.revision)))
    return
  if (local) {
    inflight.delete(k)
    setCachedMarkPreview(agentId, seq, local.revision, messageMarkPreviewText(local.message) ?? '')
    return
  }
  if (inflight.has(k))
    return

  const token = ++nextFetchToken
  inflight.set(k, token)
  // Only the CURRENT fetch for this key may write/clear -- a resolution whose token was
  // dropped by forget or superseded by a re-warm is a no-op, so it can neither re-leak an
  // entry for a closed agent nor clobber a fresher fetch's result.
  const isCurrent = () => inflight.get(k) === token
  await messages.message(seq)
    .then((msg) => {
      // A resolved-undefined message is a DEFINITIVE absence (no row at this seq) --
      // cache '' so the rail shows a label without re-fetching. A REJECTION lands in
      // .catch below and is NOT cached, so a transient failure can be retried.
      if (isCurrent())
        setCachedMarkPreview(agentId, seq, msg?.revision, msg ? (messageMarkPreviewText(msg.message) ?? '') : '')
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
 * store's forgetAgent so the per-agent buckets don't accumulate one entry per
 * (agent, seq) ever hovered for the life of the tab -- and so a stale `''` (a
 * fetch that transiently found no row) never survives a close/reopen of the same
 * agentId to suppress the now-available preview. Dropping the in-flight token also
 * fences any pending fetch for this agent, so a late resolve can't re-leak an entry.
 */
export function forgetMarkPreview(agentId: string): void {
  // Readers fetch the bucket from the map on every read, so dropping it is the whole
  // removal: no scan of other agents' entries, and a reopen allocates a fresh bucket
  // through `bucketFor`. A fetch that resolves after this lands writes nothing -- the
  // in-flight fence below dropped its token.
  buckets.delete(agentId)
  const prefix = cachePrefix(agentId)
  for (const k of inflight.keys()) {
    if (k.startsWith(prefix))
      inflight.delete(k)
  }
}

/** Test-only: reset the per-agent buckets + in-flight set between cases. */
export function __resetMarkPreviewCacheForTest(): void {
  buckets.clear()
  inflight.clear()
}
