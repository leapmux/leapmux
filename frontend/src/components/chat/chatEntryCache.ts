import type { Accessor } from 'solid-js'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SpanMessageRevision } from '~/stores/chatTypes'
import { createMemo } from 'solid-js'
import { shallowEqual } from '~/lib/shallowEqual'
import { buildHeightKey } from './chatRowGeometry'
import { classifyParsedMessage } from './messageClassification'
import { parseSpanLines } from './spanLinesParse'

// ---------------------------------------------------------------------------
// Classified-entry cache
//
// Classifies each window message for rendering and caches the result by message
// id so <For> receives stable object references for unchanged rows (no full DOM
// recreation). A self-contained unit -- extracted from ChatView so its freshness
// rule (reuse only when all freshness inputs are unchanged) and its
// incremental prune are testable in isolation, mirroring the scroll hook's
// extracted units (createStickyBottom, etc.).
// ---------------------------------------------------------------------------

/**
 * The dimensions that decide whether a cached entry is still reusable for a message
 * under a STABLE id: the seq plus derived signals that can move while the
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
   * Whether the row's span had a paired tool_use (opener) parse available at
   * classify time. A tool_result's renderer reads its sibling opener (Claude's edit
   * input, Pi's start args); if the opener arrives LATER (older-page prepend /
   * reseq / re-broadcast) while the result is off-screen, the cache must re-build
   * (and bust the measured-height cache key) so the row isn't frozen at its
   * no-sibling shape. (A tool_use row's own span resolves to itself, so this stays
   * stably true for openers — no spurious rebuilds.)
   */
  hasToolUseSibling: boolean
  /**
   * The paired tool_use OPENER's content version at classify time (0 for non-result
   * rows or when no opener is indexed). Retained separately for tests/debugging and
   * for the height debug view. The revision key also identifies replacement messages.
   */
  toolUseSiblingContentVersion: number
  /**
   * Stable token for the paired tool_use opener identity/seq/content version. A
   * present-to-different-present sibling replacement can keep contentVersion at 0,
   * so the version alone is not enough to bust cached result rows.
   */
  toolUseSiblingRevisionKey: string
  /**
   * Whether the row's span had a paired tool_result parse available at classify
   * time. Some tool_use rows (Claude Task*) render from hidden result data, so
   * late result arrival must rebuild the opener row instead of freezing it at its
   * no-result shape.
   */
  hasToolResultSibling: boolean
  /**
   * The paired tool_result's content version at classify time (0 for non-opener
   * rows or when no result is indexed). Retained separately for tests/debugging and
   * the height debug view. The revision key also identifies replacement messages.
   */
  toolResultSiblingContentVersion: number
  /** Stable token for the paired tool_result identity/seq/content version. */
  toolResultSiblingRevisionKey: string
  /**
   * Whether these messages were a SUBAGENT's own transcript at classify time. The
   * flag decides whether a forwarded `parent_tool_use_id` row is the prompt SENT
   * to a subagent (the parent's view) or an ordinary message INSIDE it, so a row
   * classified under the wrong value renders as the collapsed "Prompt" card
   * forever.
   *
   * It is not constant for the life of a view: a subagent tab is placed before
   * `listAgents` hydrates its `parentAgentId`, and the child's own messages are
   * subscribed immediately, so a row can be classified in that window. Tracking it
   * here rebuilds those rows when the link lands.
   */
  isChildTranscript: boolean
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
 * The measured-height cache key for a classified entry at a given UI
 * version. Reads height-affecting freshness signals (sibling presence/content
 * versions and content version) off the entry's own
 * freshness signature, so a new freshness dimension that affects height is wired
 * in one place here -- not hand-copied into the ChatView call site, the same
 * drift hazard EntryFreshness itself guards against.
 */
export function heightKeyForEntry(entry: ClassifiedEntry, uiVersion: number): string {
  return buildHeightKey({
    seq: entry.msg.seq,
    hasToolUseSibling: entry.freshness.hasToolUseSibling,
    toolUseContentVersion: entry.freshness.toolUseSiblingContentVersion,
    toolUseRevisionKey: entry.freshness.toolUseSiblingRevisionKey,
    hasToolResultSibling: entry.freshness.hasToolResultSibling,
    toolResultContentVersion: entry.freshness.toolResultSiblingContentVersion,
    toolResultRevisionKey: entry.freshness.toolResultSiblingRevisionKey,
    uiVersion,
    contentVersion: entry.freshness.contentVersion,
    isChildTranscript: entry.freshness.isChildTranscript,
  })
}

export interface ClassifiedEntryCacheDeps {
  /** The window's messages, in display order (read reactively). */
  messages: () => readonly AgentChatMessage[]
  /** The shared resolver's current request revision for this span. */
  requestRevision?: (spanId: string) => SpanMessageRevision | undefined
  /** The shared resolver's current result revision for this span. */
  resultRevision?: (spanId: string) => SpanMessageRevision | undefined
  /**
   * The row's content version (the store's getMessageContentVersion), bumped on a
   * same-seq in-place body replacement. MUST read REACTIVELY: that merge changes
   * only content (a field this memo doesn't read -- it reads seq/id/spanId), so it
   * would NOT wake the memo on its own. Subscribing to the version here is what makes
   * the bump wake the memo so it re-checks freshness and rebuilds the row.
   */
  contentVersionById?: (id: string) => number
  /** Show otherwise-hidden messages (the debug preference). */
  showHiddenMessages: () => boolean
  /**
   * Whether these messages are a SUBAGENT's own transcript. Read REACTIVELY: a
   * subagent tab is placed before `listAgents` hydrates its `parentAgentId`, and
   * the child's own message stream starts immediately, so this reads false for
   * the rows that arrive in that window and flips true when the link lands. It IS
   * a freshness dimension for exactly that reason.
   */
  isChildTranscript?: () => boolean
}

export interface ClassifiedEntryCache {
  /** Visible classified entries for the current window (reactive). */
  visibleEntries: Accessor<ClassifiedEntry[]>
  /** Whether ANY message would render — cheaper than materializing visibleEntries(). */
  hasVisibleEntries: Accessor<boolean>
  /** The cached entry for a message id (used by scroll/debug logging). */
  getEntry: (id: string) => ClassifiedEntry | undefined
}

export function createClassifiedEntryCache(deps: ClassifiedEntryCacheDeps): ClassifiedEntryCache {
  const entryCache = new Map<string, ClassifiedEntry>()
  const revisionKeyOf = (revision: SpanMessageRevision | undefined): string =>
    revision === undefined ? '' : `${revision.id.length}:${revision.id}|${revision.seq}|${revision.contentVersion}|${revision.supplementalRevision}`

  const freshnessOf = (msg: AgentChatMessage, kind: string): EntryFreshness => {
    const request = msg.spanId ? deps.requestRevision?.(msg.spanId) : undefined
    const result = msg.spanId && kind.startsWith('tool_use') ? deps.resultRevision?.(msg.spanId) : undefined
    return {
      seq: msg.seq,
      contentVersion: deps.contentVersionById?.(msg.id) ?? 0,
      hasToolUseSibling: request !== undefined,
      toolUseSiblingContentVersion: request?.contentVersion ?? 0,
      toolUseSiblingRevisionKey: revisionKeyOf(request),
      hasToolResultSibling: result !== undefined,
      toolResultSiblingContentVersion: result?.contentVersion ?? 0,
      toolResultSiblingRevisionKey: revisionKeyOf(result),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    }
  }
  /**
   * A cached entry is reusable only if its freshness signature still matches the
   * message's seq, in-place content version,
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
    const classified = classifyParsedMessage(msg, {
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    })
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
   * freshness inputs, otherwise freshly built and cached. The single
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
  const walkWindow = (visit: (msg: AgentChatMessage) => void) => {
    const present = new Set<string>()
    for (const msg of deps.messages()) {
      present.add(msg.id)
      visit(msg)
    }
    pruneToWindow(present)
  }
  const visibleEntries = createMemo(() => {
    const showHidden = deps.showHiddenMessages()
    const result: ClassifiedEntry[] = []
    walkWindow((msg) => {
      // Reuse the cached entry when all freshness inputs match.
      const entry = resolveEntry(msg)
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
  // always does).
  const hasVisibleEntries = createMemo(() => {
    const showHidden = deps.showHiddenMessages()
    let visible = false
    walkWindow((msg) => {
      // `showHidden ||` short-circuits hasVisibleMessage (no classify/cache-fill);
      // `!visible &&` stops classifying once any visible row is found.
      if (!visible && (showHidden || hasVisibleMessage(msg)))
        visible = true
    })
    return visible
  })

  return { visibleEntries, hasVisibleEntries, getEntry: id => entryCache.get(id) }
}
