import type { Accessor } from 'solid-js'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createMemo } from 'solid-js'
import { shallowEqual } from '~/lib/shallowEqual'
import { buildEstimateKey } from './chatHeightEstimator'
import { classifyParsedMessage } from './messageClassification'
import { parseSpanLines } from './spanLinesParse'

// ---------------------------------------------------------------------------
// Classified-entry cache
//
// Classifies each window message for rendering and caches the result by message
// id so <For> receives stable object references for unchanged rows (no full DOM
// recreation). A self-contained unit -- extracted from ChatView so its freshness
// rule (reuse only when seq AND command-stream presence are unchanged) and its
// incremental prune are testable in isolation, mirroring the scroll hook's
// extracted units (createStickyBottom, etc.).
// ---------------------------------------------------------------------------

/**
 * The dimensions that decide whether a cached entry is still reusable for a message
 * under a STABLE id: the seq plus the four derived signals that can move while the
 * seq+id stay put. Built once per (re)classify (freshnessOf) and compared
 * structurally by isEntryFresh, so adding a freshness dimension is a single edit
 * here -- the builder and the comparison can't drift out of a parallel hand-synced
 * field list, the exact hazard this cache's surrounding comments repeatedly warn of.
 */
export interface EntryFreshness {
  /**
   * The message seq at classify time. A new seq is a different message instance under
   * the same id (a reseq / notification consolidation), so it always rebuilds.
   */
  seq: bigint
  /**
   * The message's content version at classify time. A same-seq in-place body
   * replacement (the store's updateExistingMessage same-seq path) keeps the id,
   * seq, AND store-proxy reference, so neither seq nor `cached.msg` identity moves
   * -- only this counter does. Folding it into the freshness check rebuilds the row
   * on such an update instead of rendering the pre-update classification.
   */
  contentVersion: number
  /**
   * Whether the row's span had renderable command-stream content at classify time.
   * A Codex reasoning row with no persisted summary/content classifies as visible
   * (assistant_thinking) ONLY while its span has renderable stream content, so the
   * cache must re-classify when that presence flips — otherwise the row freezes on
   * its first classification (hidden) and the streamed thinking never appears.
   */
  hasCommandStream: boolean
  /**
   * Whether the row's span had a paired tool_use (opener) parse available at
   * classify time. A tool_result's analytical height reads its sibling opener
   * (Claude's edit input, Pi's start args) to size the diff exactly; if the opener
   * arrives LATER (older-page prepend / reseq / re-broadcast) while the result is
   * off-screen, the cache must re-build (and bust the height estimate) so the row
   * isn't frozen at its no-sibling size. (A tool_use row's own span resolves to
   * itself, so this stays stably true for openers — no spurious rebuilds.)
   */
  hasToolUseSibling: boolean
  /**
   * The paired tool_use OPENER's content version at classify time (0 for non-result
   * rows or when no opener is indexed). A tool_result reads its opener's body to size
   * the diff; the opener is a DIFFERENT message, so an in-place same-seq replacement
   * of the opener bumps the OPENER's content version while the result's own seq, id,
   * and `contentVersion` stay put. Folding it into the freshness check rebuilds (and
   * re-estimates) the result row on such an opener change instead of leaving it frozen
   * at the pre-change diff size. Also folded into the height estimate key.
   */
  toolUseSiblingContentVersion: number
}

/**
 * A message classified for rendering, plus the parsed span lines and the freshness
 * signature at classify time.
 */
export type ClassifiedEntry = ReturnType<typeof classifyParsedMessage> & {
  msg: AgentChatMessage
  parsedSpanLines: (SpanLine | null)[]
  /** The freshness signature this entry was built at (see EntryFreshness / isEntryFresh). */
  freshness: EntryFreshness
  /**
   * The `spanLines` string this entry's `parsedSpanLines` was parsed from. The
   * store reuses the proxy on an in-place update, so `cached.msg.spanLines` reads
   * the CURRENT (possibly new) value -- comparing the proxy to itself can't tell
   * whether the rail payload actually changed. Snapshotting the string lets a
   * rebuild reuse the parse only when the payload is byte-identical.
   */
  spanLinesRef: string
}

/**
 * The analytical-height estimate-cache key for a classified entry at a given UI
 * version. Reads the four height-affecting freshness signals (the opener content
 * version, content version, sibling presence, command-stream presence) off the
 * entry's OWN freshness signature, so a new freshness dimension that affects height
 * is wired in one place here -- not hand-copied into the ChatView call site, the
 * same drift hazard EntryFreshness itself guards against. Live streaming TEXT is
 * deliberately excluded (it grows per-delta and lives outside msg.content); see
 * ChatView's virtualItems and EstimateKeyInputs for the rationale.
 */
export function estimateKeyForEntry(entry: ClassifiedEntry, uiVersion: number): string {
  return buildEstimateKey({
    seq: entry.msg.seq,
    hasToolUseSibling: entry.freshness.hasToolUseSibling,
    toolUseContentVersion: entry.freshness.toolUseSiblingContentVersion,
    uiVersion,
    contentVersion: entry.freshness.contentVersion,
    hasCommandStream: entry.freshness.hasCommandStream,
  })
}

export interface ClassifiedEntryCacheDeps {
  /** The window's messages, in display order (read reactively). */
  messages: () => readonly AgentChatMessage[]
  /**
   * Whether a span's command stream has renderable content to show, read
   * REACTIVELY. Backed by the store's renderable-span set, this presence bit flips
   * only when the stream first has content OR is cleared (not per delta), so the
   * freshness check can track it for every row without re-classifying the window on
   * every chunk. A row re-classifies the moment its span first has renderable stream
   * content AND the moment it's cleared. NOT "actively streaming": it stays true
   * after the producer goes quiet, until the stream ends.
   */
  hasRenderableStreamBySpanId?: (spanId: string) => boolean
  /**
   * Whether a span has a paired tool_use (opener) parse right now, read
   * REACTIVELY (the store's span index). Lets a tool_result row re-classify and
   * re-estimate the moment its opener is indexed, instead of staying frozen at its
   * no-sibling height when the opener arrives after it.
   */
  hasToolUseSiblingBySpanId?: (spanId: string) => boolean
  /**
   * The paired tool_use opener's content version for a span, read REACTIVELY (the
   * store's getToolUseContentVersionBySpanId). A tool_result sizes its diff from the
   * opener, so an in-place opener body change -- which bumps the opener's version,
   * not the result's -- must wake this memo to re-classify and re-estimate the
   * result row. Only consulted for tool_result rows (see toolUseSiblingContentVersionOf).
   */
  toolUseSiblingContentVersionBySpanId?: (spanId: string) => number
  /**
   * The row's content version (the store's getMessageContentVersion), bumped on a
   * same-seq in-place body replacement. MUST read REACTIVELY: that merge changes
   * only content (a field this memo doesn't read -- it reads seq/id/spanId), so it
   * would NOT wake the memo on its own. Subscribing to the version here is what makes
   * the bump wake the memo so it re-checks freshness and rebuilds the row.
   */
  contentVersionById?: (id: string) => number
  /** Scrolled away from the live tail: trailing optimistic locals are hidden. */
  hasNewerMessages: () => boolean
  /** Show otherwise-hidden messages (the debug preference). */
  showHiddenMessages: () => boolean
}

export interface ClassifiedEntryCache {
  /** Visible classified entries for the current window (reactive). */
  visibleEntries: Accessor<ClassifiedEntry[]>
  /** Whether ANY message would render — cheaper than materializing visibleEntries(). */
  hasVisibleEntries: Accessor<boolean>
  /** The cached entry for a message id (for the height-estimate-miss logger). */
  getEntry: (id: string) => ClassifiedEntry | undefined
}

export function createClassifiedEntryCache(deps: ClassifiedEntryCacheDeps): ClassifiedEntryCache {
  const entryCache = new Map<string, ClassifiedEntry>()
  /**
   * Renderable command-stream presence for a row's span, read REACTIVELY. The
   * store's renderable-span bit flips only when the stream first has content or is
   * cleared, so the freshness check below can track it for every row each memo run
   * without re-classifying on every delta -- and the memo wakes the moment a span's
   * presence flips in EITHER direction (hidden->visible when it first has content,
   * the reverse when it's cleared).
   */
  const hasRenderableStream = (msg: AgentChatMessage): boolean =>
    !!msg.spanId && (deps.hasRenderableStreamBySpanId?.(msg.spanId) ?? false)
  const hasToolUseSibling = (msg: AgentChatMessage): boolean =>
    !!msg.spanId && (deps.hasToolUseSiblingBySpanId?.(msg.spanId) ?? false)
  const contentVersionOf = (msg: AgentChatMessage): number =>
    deps.contentVersionById?.(msg.id) ?? 0
  // The opener's content version, but ONLY for tool_result rows (the only kind that
  // sizes from a sibling opener). Scoping it to tool_result avoids an opener row
  // redundantly tracking its OWN version twice (once as contentVersion, once here).
  const toolUseSiblingContentVersionOf = (msg: AgentChatMessage, kind: string): number =>
    kind === 'tool_result' && !!msg.spanId
      ? (deps.toolUseSiblingContentVersionBySpanId?.(msg.spanId) ?? 0)
      : 0
  /**
   * Build the freshness signature for `msg` classified as `kind`. The SINGLE place
   * the freshness dimensions are enumerated: isEntryFresh compares against this and
   * buildEntry stores it, so neither can drift from a hand-synced field list. `kind`
   * is the row's classification (only tool_result rows track an opener's version);
   * isEntryFresh passes the CACHED entry's kind so the comparison reads the same slot
   * the entry was built with.
   */
  const freshnessOf = (msg: AgentChatMessage, kind: string): EntryFreshness => ({
    seq: msg.seq,
    contentVersion: contentVersionOf(msg),
    hasCommandStream: hasRenderableStream(msg),
    hasToolUseSibling: hasToolUseSibling(msg),
    toolUseSiblingContentVersion: toolUseSiblingContentVersionOf(msg, kind),
  })
  /**
   * A cached entry is reusable only if its freshness signature still matches the
   * message's: the seq, the in-place content version, the command-stream presence,
   * the paired tool_use availability, AND (for a tool_result) the opener's content
   * version are all unchanged. seq alone is not enough -- a same-seq in-place body
   * replacement keeps the seq (and the proxy reference), so the content version is
   * what reveals it; and an opener edit moves only the OPENER's version, so a result
   * row needs that folded in too. Compared STRUCTURALLY against a freshly-built
   * signature so the dimension list lives only in freshnessOf.
   */
  const isEntryFresh = (cached: ClassifiedEntry | undefined, msg: AgentChatMessage): cached is ClassifiedEntry =>
    !!cached && shallowEqual(cached.freshness, freshnessOf(msg, cached.category.kind))
  const buildEntry = (msg: AgentChatMessage, cached?: ClassifiedEntry): ClassifiedEntry => {
    const hasCommandStream = hasRenderableStream(msg)
    const classified = classifyParsedMessage(msg, { hasCommandStream })
    // Reuse the cached parse when the `spanLines` payload is byte-identical to the
    // one it was parsed from -- compared against the snapshot, not `cached.msg`
    // (the shared proxy reads the CURRENT value, so it can't detect an in-place
    // rail change). A string compare, so a window-replace new instance with an
    // identical payload still reuses while an in-place change re-parses.
    const parsedSpanLines = cached && cached.spanLinesRef === msg.spanLines
      ? cached.parsedSpanLines
      : parseSpanLines(msg.spanLines)
    return {
      msg,
      ...classified,
      parsedSpanLines,
      freshness: freshnessOf(msg, classified.category.kind),
      spanLinesRef: msg.spanLines,
    }
  }
  /**
   * The classified entry for a message: reused when still fresh (same seq AND
   * command-stream presence), otherwise freshly built and cached. The single
   * home for the cache-fill dance both the emptiness check (hasVisibleMessage)
   * and the full materialization (visibleEntries) share, so populating the cache
   * for the visibleEntries memo can't drift from the freshness rule.
   */
  const resolveEntry = (msg: AgentChatMessage): ClassifiedEntry => {
    const cached = entryCache.get(msg.id)
    if (isEntryFresh(cached, msg))
      return cached
    const entry = buildEntry(msg, cached)
    entryCache.set(msg.id, entry)
    return entry
  }
  const hasVisibleMessage = (msg: AgentChatMessage): boolean =>
    resolveEntry(msg).category.kind !== 'hidden'
  /**
   * Walk the whole window once, computing the `hideTailLocals` flag in ONE place
   * and collecting the present-id set so the cache can be pruned to the window.
   * Both accessors share this so neither the tail-local rule nor the prune can
   * drift between them. The visit always sees every row (it never breaks early),
   * so `present` is complete regardless of how the visitor short-circuits its own
   * work; `visit` receives whether the row is a hidden trailing local.
   *
   * Trailing optimistic locals (seq 0n): while scrolled away from the live tail
   * (hasNewerMessages), the in-memory bottom isn't the real bottom and the
   * streaming/thinking UI is hidden, so a pending bubble stranded after old
   * history would be wrong. The newest-end trim keeps the local in the store, so
   * nothing is lost -- it reappears at the tail on jump-to-latest.
   *
   * The prune (pruneToWindow) drops cached entries for departed ids EVERY run.
   */
  /**
   * Drop cached entries for ids no longer in the window, EVERY run (no size
   * guard): a window that swaps out and in the SAME number of ids leaves size
   * unchanged, so a `size >` shortcut would never fire and would leak the departed
   * entries -- the exact "reading ONLY hasVisibleEntries keeps the cache bounded"
   * contract walkWindow must honor. The cache is window-sized (<= a few hundred),
   * so the unconditional sweep is trivial.
   */
  const pruneToWindow = (present: Set<string>) => {
    for (const id of entryCache.keys()) {
      if (!present.has(id))
        entryCache.delete(id)
    }
  }
  const walkWindow = (visit: (msg: AgentChatMessage, isHiddenTailLocal: boolean) => void) => {
    const hideTailLocals = deps.hasNewerMessages()
    const present = new Set<string>()
    for (const msg of deps.messages()) {
      present.add(msg.id)
      visit(msg, hideTailLocals && msg.seq === 0n)
    }
    pruneToWindow(present)
  }
  const visibleEntries = createMemo(() => {
    const showHidden = deps.showHiddenMessages()
    const result: ClassifiedEntry[] = []
    walkWindow((msg, isHiddenTailLocal) => {
      // Reuse the cached entry when the message hasn't changed (same seq = same
      // content) AND its command-stream presence is unchanged; otherwise rebuild.
      const entry = resolveEntry(msg)
      if (isHiddenTailLocal)
        return
      if (showHidden || entry.category.kind !== 'hidden')
        result.push(entry)
    })
    return result
  })
  // Cheaper than materializing visibleEntries(): it skips building the result
  // array and short-circuits the EXPENSIVE classification at the first visible
  // row (`!visible &&`). It still walks the full window via walkWindow (which
  // collects the present-id set and prunes), so reading ONLY this accessor keeps
  // the cache bounded instead of leaking departed-id entries. Note this bounds the
  // cache but does NOT refresh it: rows past the first visible one skip resolveEntry,
  // so their cached entries are not rebuilt on a hasVisibleEntries-only read -- a
  // consumer that needs fresh entries must also read visibleEntries() (ChatView
  // always does). The hideTailLocals rule comes from walkWindow, so it can't
  // disagree with visibleEntries() -- otherwise hasVisibleEntries() could say
  // "something renders" while visibleEntries() yields [] and a consumer gating on
  // the former would show a non-empty container with zero rows.
  const hasVisibleEntries = createMemo(() => {
    const showHidden = deps.showHiddenMessages()
    let visible = false
    walkWindow((msg, isHiddenTailLocal) => {
      // `showHidden ||` short-circuits hasVisibleMessage (no classify/cache-fill);
      // `!visible &&` stops classifying once any visible row is found.
      if (!visible && !isHiddenTailLocal && (showHidden || hasVisibleMessage(msg)))
        visible = true
    })
    return visible
  })

  return { visibleEntries, hasVisibleEntries, getEntry: id => entryCache.get(id) }
}
