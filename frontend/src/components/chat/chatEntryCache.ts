import type { Accessor } from 'solid-js'
import type { ContentKeyInputs } from './chatRowGeometry'
import type { ResolvedMessage } from './messageContextResolver'
import type { PreparedMessage } from './rowPreparation'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageRevision, MessageSpanIdentity } from '~/lib/messageSpan'
import type { SettingsLabelDependency } from '~/lib/settingsLabelCache'
import { createMemo } from 'solid-js'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { collectSettingsLabelDependencies, settingsLabelCacheRevision, settingsLabelDependencyRevision } from '~/lib/settingsLabelCache'
import { shallowEqual } from '~/lib/shallowEqual'
import { rowRevisionKey } from './chatRevisionKey'
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
 * under a STABLE id: the row's exact revision dependencies as one key, plus the
 * child-transcript flag.
 *
 * Built once per (re)classify (freshnessOf) and compared structurally by
 * isEntryFresh, so adding a freshness dimension is a single edit in the builder
 * -- the exact hazard this cache's surrounding comments repeatedly warn of. The
 * revision key carries the message's own revision ALWAYS (a reseq, an in-place
 * body replacement, a late supplement), the REQUEST revision only on a result
 * row, and the RESULT revision only on a tool-use row; an unrelated sibling's
 * revision appears in no member, so a change to it rebuilds nothing.
 */
export interface EntryFreshness {
  /** The row's exact revision dependencies (see chatRevisionKey.ts). */
  revisionKey: string
  /** Whether these messages were a SUBAGENT's own transcript at classify time. */
  isChildTranscript: boolean
  /** Revisions of the exact display-label groups that this row reads. */
  settingsLabelRevision: string
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
  /** The exact option groups whose labels this entry read. */
  settingsLabelDependencies: SettingsLabelDependency[]
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
    revisionKey: entry.freshness.revisionKey,
    isChildTranscript: entry.freshness.isChildTranscript,
    settingsLabelRevision: entry.freshness.settingsLabelRevision,
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
 * the other threw away the row's extracted model, its normalized command body, its
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
  requestRevision?: (identity: MessageSpanIdentity) => MessageRevision | undefined
  /** The shared resolver's current result revision for this span. */
  resultRevision?: (identity: MessageSpanIdentity) => MessageRevision | undefined
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
  resolvedMessage?: (message: AgentChatMessage) => ResolvedMessage | undefined
  /** The shared resolver's one role decision for this message. */
  role: (message: AgentChatMessage) => 'request' | 'result' | 'other'
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
  /** Whether any message renders, derived from the same visible entry list. */
  hasVisibleEntries: Accessor<boolean>
  /** The cached entry for a message id (used by scroll/debug logging). */
  getEntry: (id: string) => ClassifiedEntry | undefined
}

export function createClassifiedEntryCache(deps: ClassifiedEntryCacheDeps): ClassifiedEntryCache {
  const entryCache = new Map<string, ClassifiedEntry>()
  /**
   * Build the freshness signature for `message`. This is the SINGLE place that
   * lists the freshness dimensions. `isEntryFresh` compares this value, and
   * `buildEntry` stores it, so the two paths cannot drift.
   */
  const freshnessOf = (
    message: AgentChatMessage,
    selected: ResolvedMessage | undefined,
    labelDependencies: readonly SettingsLabelDependency[],
  ): EntryFreshness => {
    // Subscribe the window memo to catalog changes. The dependency key below
    // decides whether this row actually needs a rebuild.
    settingsLabelCacheRevision()
    const own: MessageRevision = selected?.revision ?? {
      id: message.id,
      seq: message.seq,
      contentVersion: deps.contentVersionById?.(message.id) ?? 0,
      supplementalRevision: message.supplementalRevision,
    }
    // Select a sibling by the resolved SPAN ROLE, not by the message category.
    // Some completed ACP updates use the tool_use category but hold the result
    // role. A result draws its request input. A request can draw hidden result
    // data. No row records its own selected side a second time.
    const selectedMessage = selected?.message ?? message
    const role = selectedMessage.spanId === ''
      ? 'other'
      : deps.role(selectedMessage)
    const selectedIdentity = messageSpanIdentity(selectedMessage)
    const request = role === 'result'
      ? deps.requestRevision?.(selectedIdentity)
      : undefined
    const result = role === 'request'
      ? deps.resultRevision?.(selectedIdentity)
      : undefined
    return {
      revisionKey: rowRevisionKey({
        own,
        ...(request !== undefined ? { request } : {}),
        ...(result !== undefined ? { result } : {}),
      }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
      settingsLabelRevision: settingsLabelDependencyRevision(labelDependencies),
    }
  }
  /**
   * Reuse a cached entry only when its revision key and child-transcript flag
   * match the current source. `freshnessOf` owns both values.
   */
  const isEntryFresh = (cached: ClassifiedEntry | undefined, freshness: EntryFreshness): cached is ClassifiedEntry =>
    !!cached && shallowEqual(cached.freshness, freshness)
  const buildEntry = (message: AgentChatMessage, selected: ResolvedMessage | undefined, cached?: ClassifiedEntry): ClassifiedEntry => {
    // The shared resolver's own parse when it holds one, so the bubble, the toolbar
    // and the image tab read the row from ONE resolved payload. The resolver is
    // absent outside ChatView (a test, an isolated preview), and preparation then
    // resolves the payload itself.
    const selectedMessage = selected?.message ?? message
    const collected = collectSettingsLabelDependencies(() => prepareMessage(selectedMessage, {
      ...(selected === undefined ? {} : { original: selected.original, resolved: selected.resolved }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    }))
    const prepared = collected.value
    // Reuse the cached parse when the `spanLines` payload is byte-identical to the
    // one it was parsed from -- compared against the snapshot, not `cached.message`
    // (the shared proxy reads the CURRENT value, so it can't detect an in-place
    // rail change). A string compare, so a window-replace new instance with an
    // identical payload still reuses while an in-place change re-parses.
    const parsedSpanLines = cached && cached.spanLinesRef === selectedMessage.spanLines
      ? cached.parsedSpanLines
      : parseSpanLines(selectedMessage.spanLines)
    return {
      ...prepared,
      parsedSpanLines,
      freshness: freshnessOf(message, selected, collected.dependencies),
      spanLinesRef: selectedMessage.spanLines,
      settingsLabelDependencies: collected.dependencies,
    }
  }
  /**
   * Return the cached classified entry when it is fresh. Otherwise, rebuild and
   * cache it. Both visibility checks and materialization use this function.
   */
  const resolveEntry = (message: AgentChatMessage): ClassifiedEntry => {
    const cached = entryCache.get(message.id)
    const selected = deps.resolvedMessage?.(message)
    const freshness = freshnessOf(message, selected, cached?.settingsLabelDependencies ?? [])
    if (isEntryFresh(cached, freshness))
      return cached
    const entry = buildEntry(message, selected, cached)
    entryCache.set(message.id, entry)
    return entry
  }
  /**
   * Drop cached entries for ids no longer in the window, EVERY run (no size
   * guard): a window that swaps out and in the SAME number of ids leaves size
   * unchanged, so a `size >` shortcut would never fire and would leak the departed
   * entries. `hasVisibleEntries` derives from `visibleEntries`, so either public
   * accessor runs this same pruning path. The cache is window-sized (<= a few
   * hundred), so the unconditional sweep is trivial.
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
  const hasVisibleEntries = createMemo(() => visibleEntries().length > 0)

  return { visibleEntries, hasVisibleEntries, getEntry: id => entryCache.get(id) }
}
