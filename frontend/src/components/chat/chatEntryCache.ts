import type { Accessor } from 'solid-js'
import type { ContentKeyInputs } from './chatRowGeometry'
import type { PreparedMessage } from './rowPreparation'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import type { SpanMessageRevision } from '~/stores/chatTypes'
import { createMemo } from 'solid-js'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { shallowEqual } from '~/lib/shallowEqual'
import { buildContentKey, buildHeightKey } from './chatRowGeometry'
import { prepareMessage } from './rowPreparation'
import { parseSpanLines } from './spanLinesParse'

// ---------------------------------------------------------------------------
// Prepared-entry cache
//
// Prepares each window message for rendering -- parse, resolve the supplemental
// content, classify the resolved payload -- and caches the result by message id so
// <For> receives stable object references for unchanged rows (no full DOM
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
   * seq, AND store-proxy reference, so neither seq nor `cached.message` identity moves
   * -- only this counter does. Folding it into the freshness check rebuilds the row
   * on such an update instead of rendering the pre-update classification.
   */
  contentVersion: number
  /**
   * The message's supplemental revision at classify time.
   *
   * The preparation MERGES the supplemental content into the payload before it
   * classifies, so a supplement that arrives late can change the category as well as
   * the body -- a ZCode `result` frame whose recovered output lands afterwards is the
   * case that named this rule. The store bumps `contentVersion` for such an arrival
   * today, so this dimension is currently redundant with the one above; it is stated
   * anyway, because the cache must not depend on the store keeping that discipline
   * for a field the cache itself reads.
   */
  supplementalRevision: bigint
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
 * A message PREPARED for rendering, plus the parsed span lines and the freshness
 * signature at prepare time.
 *
 * It EXTENDS `PreparedMessage` rather than holding one, so the bubble takes the whole
 * entry as its prepared message and the two cannot hold different answers for one
 * row. The bubble used to receive the original parse and the category as separate
 * props and resolve the payload itself, which is how the transcript came to classify
 * the raw bytes and extract the merged ones.
 */
export type ClassifiedEntry = PreparedMessage & {
  parsedSpanLines: (SpanLine | null)[]
  /** The freshness signature this entry was built at (see EntryFreshness / isEntryFresh). */
  freshness: EntryFreshness
  /**
   * The `spanLines` string this entry's `parsedSpanLines` was parsed from. The
   * store reuses the proxy on an in-place update, so `cached.message.spanLines` reads
   * the CURRENT (possibly new) value -- comparing the proxy to itself can't tell
   * whether the rail payload actually changed. Snapshotting the string lets a
   * rebuild reuse the parse only when the payload is byte-identical.
   */
  spanLinesRef: string
}

/**
 * The content signals of one classified entry, read off its own freshness
 * signature.
 *
 * ONE reader of `EntryFreshness` for both keys below, so a new freshness dimension
 * is wired in one place here -- not hand-copied into the ChatView call sites, the
 * same drift hazard `EntryFreshness` itself guards against.
 */
function contentKeyInputsOf(entry: ClassifiedEntry): ContentKeyInputs {
  return {
    seq: entry.message.seq,
    hasToolUseSibling: entry.freshness.hasToolUseSibling,
    toolUseContentVersion: entry.freshness.toolUseSiblingContentVersion,
    toolUseRevisionKey: entry.freshness.toolUseSiblingRevisionKey,
    hasToolResultSibling: entry.freshness.hasToolResultSibling,
    toolResultContentVersion: entry.freshness.toolResultSiblingContentVersion,
    toolResultRevisionKey: entry.freshness.toolResultSiblingRevisionKey,
    contentVersion: entry.freshness.contentVersion,
    supplementalRevision: entry.freshness.supplementalRevision,
    isChildTranscript: entry.freshness.isChildTranscript,
  }
}

/**
 * The measured-height cache key for a classified entry at a given UI version.
 *
 * The VIRTUALIZER's key. A per-row expand or diff-view toggle bumps `uiVersion`,
 * which changes the row's height and so must re-measure it.
 */
export function heightKeyForEntry(entry: ClassifiedEntry, uiVersion: number): string {
  return buildHeightKey({ ...contentKeyInputsOf(entry), uiVersion })
}

/**
 * The render-cache key for a classified entry: its content signals, and NOT its UI
 * state.
 *
 * The row CACHE's key, and it is deliberately a different key from the height one.
 * The two answer different questions: an expand or a diff-view toggle changes what
 * the row MEASURES and changes nothing that the cache holds. Deriving one key from
 * the other threw away the row's extracted IR, its normalized command body, its
 * Myers diff and its rendered markdown on every click of the expand control, and the
 * row rebuilt all four to draw the same content at a new height.
 */
export function renderKeyForEntry(entry: ClassifiedEntry): string {
  return `${entry.message.id}|${buildContentKey(contentKeyInputsOf(entry))}`
}

export interface ClassifiedEntryCacheDeps {
  /** The window's messages, in display order (read reactively). */
  messages: () => readonly AgentChatMessage[]
  /** The shared resolver's current request revision for this span. */
  requestRevision?: (identity: MessageSpanIdentity) => SpanMessageRevision | undefined
  /** The shared resolver's current result revision for this span. */
  resultRevision?: (identity: MessageSpanIdentity) => SpanMessageRevision | undefined
  /**
   * The row's content version (the store's getMessageContentVersion), bumped on a
   * same-seq in-place body replacement. MUST read REACTIVELY: that merge changes
   * only content (a field this memo doesn't read -- it reads seq/id/spanId), so it
   * would NOT wake the memo on its own. Subscribing to the version here is what makes
   * the bump wake the memo so it re-checks freshness and rebuilds the row.
   */
  contentVersionById?: (id: string) => number
  /**
   * The shared resolver's merged payload for one message, when it holds one.
   *
   * Preparation resolves the payload itself without this, which is correct but
   * produces a SECOND object for the same bytes -- and the bubble, the toolbar and
   * the image tab would then each read the row from a different one. Reading the
   * resolver's copy here is what keeps them on one.
   */
  resolvedParsed?: (message: AgentChatMessage) => ParsedMessageContent | undefined
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

  /**
   * Build the freshness signature for `message` classified as `kind`. The SINGLE place
   * the freshness dimensions are enumerated: isEntryFresh compares against this and
   * buildEntry stores it, so neither can drift from a hand-synced field list. `kind`
   * is the row's classification (only a tool_result row tracks an opener's
   * revision); isEntryFresh passes the CACHED entry's kind, so the comparison reads
   * the same slots the entry was built with.
   */
  const freshnessOf = (message: AgentChatMessage, kind: string): EntryFreshness => {
    const request = message.spanId ? deps.requestRevision?.(messageSpanIdentity(message)) : undefined
    // The opener's REVISION, for a tool_result row alone -- the only kind that
    // sizes itself from a sibling opener. A tool_use row's own span resolves to
    // ITSELF, so an ungated read records that row's own content version a second
    // time, in a slot whose name states that it holds a sibling's. The presence
    // flag below stays ungated: an opener that resolves to itself holds that flag
    // stable at true, which is what keeps an opener row from rebuilding.
    const opener = kind === 'tool_result' ? request : undefined
    const result = message.spanId && kind.startsWith('tool_use') ? deps.resultRevision?.(messageSpanIdentity(message)) : undefined
    return {
      seq: message.seq,
      contentVersion: deps.contentVersionById?.(message.id) ?? 0,
      supplementalRevision: message.supplementalRevision,
      hasToolUseSibling: request !== undefined,
      toolUseSiblingContentVersion: opener?.contentVersion ?? 0,
      toolUseSiblingRevisionKey: revisionKeyOf(opener),
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
  const isEntryFresh = (cached: ClassifiedEntry | undefined, message: AgentChatMessage): cached is ClassifiedEntry =>
    !!cached && shallowEqual(cached.freshness, freshnessOf(message, cached.category.kind))
  const buildEntry = (message: AgentChatMessage, cached?: ClassifiedEntry): ClassifiedEntry => {
    // The shared resolver's own parse when it holds one, so the bubble, the toolbar
    // and the image tab read the row from ONE resolved payload. The resolver is
    // absent outside ChatView (a test, an isolated preview), and preparation then
    // resolves the payload itself.
    const resolved = deps.resolvedParsed?.(message)
    const prepared = prepareMessage(message, {
      ...(resolved === undefined ? {} : { resolved }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    })
    // Reuse the cached parse when the `spanLines` payload is byte-identical to the
    // one it was parsed from -- compared against the snapshot, not `cached.message`
    // (the shared proxy reads the CURRENT value, so it can't detect an in-place
    // rail change). A string compare, so a window-replace new instance with an
    // identical payload still reuses while an in-place change re-parses.
    const parsedSpanLines = cached && cached.spanLinesRef === message.spanLines
      ? cached.parsedSpanLines
      : parseSpanLines(message.spanLines)
    return {
      ...prepared,
      parsedSpanLines,
      freshness: freshnessOf(message, prepared.category.kind),
      spanLinesRef: message.spanLines,
    }
  }
  /**
   * The classified entry for a message: reused when still fresh (same seq AND
   * freshness inputs, otherwise freshly built and cached. The single
   * home for the cache-fill dance both the emptiness check (hasVisibleMessage)
   * and the full materialization (visibleEntries) share, so populating the cache
   * for the visibleEntries memo can't drift from the freshness rule.
   */
  const resolveEntry = (message: AgentChatMessage): ClassifiedEntry => {
    const cached = entryCache.get(message.id)
    if (isEntryFresh(cached, message))
      return cached
    const entry = buildEntry(message, cached)
    entryCache.set(message.id, entry)
    return entry
  }
  const hasVisibleMessage = (message: AgentChatMessage): boolean =>
    resolveEntry(message).category.kind !== 'hidden'
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
  const walkWindow = (visit: (message: AgentChatMessage) => void) => {
    const present = new Set<string>()
    for (const message of deps.messages()) {
      present.add(message.id)
      visit(message)
    }
    pruneToWindow(present)
  }
  const visibleEntries = createMemo(() => {
    const showHidden = deps.showHiddenMessages()
    const result: ClassifiedEntry[] = []
    walkWindow((message) => {
      // Reuse the cached entry when all freshness inputs match.
      const entry = resolveEntry(message)
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
    walkWindow((message) => {
      // `showHidden ||` short-circuits hasVisibleMessage (no classify/cache-fill);
      // `!visible &&` stops classifying once any visible row is found.
      if (!visible && (showHidden || hasVisibleMessage(message)))
        visible = true
    })
    return visible
  })

  return { visibleEntries, hasVisibleEntries, getEntry: id => entryCache.get(id) }
}
