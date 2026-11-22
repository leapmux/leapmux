import type { Accessor } from 'solid-js'
import type { ContentKeyInputs } from './chatRowGeometry'
import type { MessageCategory } from './messageClassifier'
import type { ResolvedMessage } from './messageContextResolver'
import type { PreparedMessage } from './rowPreparation'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageRevision, MessageSpanIdentity, ToolSpanRole } from '~/lib/messageSpan'
import type { SettingsLabelDependency } from '~/lib/settingsLabelCache'
import { createMemo } from 'solid-js'
import { messageSpanIdentity, messageSpanKey } from '~/lib/messageSpan'
import { collectSettingsLabelDependencies, settingsLabelCacheRevision, settingsLabelDependencyRevision } from '~/lib/settingsLabelCache'
import { shallowEqual, shallowEqualSets } from '~/lib/shallowEqual'
import { rowRevisionKey } from './chatRevisionKey'
import { buildContentKey, buildHeightKey } from './chatRowGeometry'
import { hasCompactionEnd } from './model/notification'
import { prepareMessage } from './rowPreparation'
import { parseSpanLines } from './spanLinesParse'

// ---------------------------------------------------------------------------
// Cache for prepared entries
//
// Prepare each window message for rendering:
// - Parse the original content.
// - Resolve the supplemental content.
// - Classify the resolved payload.
// Cache each result by message ID, so <For> receives the same object for an unchanged row.
// This prevents full DOM recreation for that row.
// Keep preparation and pruning separate from ChatView, so tests can check them directly.
// Reuse an entry only when every freshness input remains unchanged.
// ---------------------------------------------------------------------------

/**
 * The inputs that determine whether a message with the same ID can reuse its cached entry.
 * `freshnessOf` builds this value when the cache classifies a message. `isEntryFresh` compares all its fields.
 *
 * The revision key always includes the message's own revision.
 * These changes affect that revision:
 * - A changed sequence.
 * - An in-place body replacement.
 * - A later supplement.
 *
 * It includes the request revision only for a result role. A row never depends on a
 * LATER message of its span, so no row includes the result revision: a request row
 * reads only its own message (see ToolSpanContext). An unrelated sibling revision
 * changes no field and causes no rebuild.
 */
export interface EntryFreshness {
  /** The row's exact revision dependencies (see chatRevisionKey.ts). */
  revisionKey: string
  /** Whether the classifier read these messages in a subagent's own transcript. */
  isChildTranscript: boolean
  /** Revisions of the exact display-label groups that this row reads. */
  settingsLabelRevision: string
  /**
   * The span role of the row. It decides whether the list counts the row as the request
   * row of its span (see listedRequestSpanKeys), so a change of it rebuilds the entry.
   */
  role: ToolSpanRole
}

/**
 * A prepared message with its parsed span lines and the freshness value at preparation time.
 * The bubble receives this whole entry as its prepared message.
 * The entry and bubble therefore cannot hold different answers for the same row.
 * Separate original and category props let the bubble resolve a different payload.
 * That could make the transcript classify raw bytes while extraction read merged content.
 */
export type ClassifiedEntry = PreparedMessage & {
  /**
   * The id of the LIST row that this entry draws: the message id for the row of a
   * message, and the id of the pending row for the pending row of a tool call that runs
   * (see chatPendingRows). The height cache, the virtualizer, the readiness registry and
   * the scroll anchor key a row by it. State that belongs to the MESSAGE -- its expand
   * state, its diff view -- keys by `message.id`, so the request row and the pending row
   * of one call share it.
   */
  rowId: string
  /** True for the pending row of a tool call that runs (see chatPendingRows). */
  pending: boolean
  parsedSpanLines: (SpanLine | null)[]
  /** The freshness value at preparation time. See EntryFreshness and isEntryFresh. */
  freshness: EntryFreshness
  /**
   * The `spanLines` string that supplied this entry's parsed lines.
   * The store reuses the proxy for an in-place update, so `cached.message.spanLines` reads the current value.
   * Comparing the proxy with itself cannot detect a changed rail payload.
   * This separate string lets a rebuilt entry reuse the parse only for identical content.
   */
  spanLinesRef: string
  /** The request span whose rail the row draws on its body. */
  openingSpanId?: string | undefined
  /** The exact option groups whose labels this entry read. */
  settingsLabelDependencies: SettingsLabelDependency[]
}

/**
 * The content inputs of one classified entry, read from its freshness value.
 * Both cache keys below use this mapping. ChatView does not copy these fields into its call sites.
 * Keep this mapping consistent with the freshness fields that affect row content.
 */
function contentKeyInputsOf(entry: ClassifiedEntry): ContentKeyInputs {
  return {
    revisionKey: entry.freshness.revisionKey,
    isChildTranscript: entry.freshness.isChildTranscript,
    settingsLabelRevision: entry.freshness.settingsLabelRevision,
  }
}

/**
 * The virtualizer's height key for a classified entry at a given UI version.
 * Expanding a row or changing its diff view increments `uiVersion`.
 * Those changes affect the row's height, so the virtualizer must measure it again.
 */
export function heightKeyForEntry(entry: ClassifiedEntry, uiVersion: number): string {
  return buildHeightKey({
    ...contentKeyInputsOf(entry),
    uiVersion,
  })
}

/**
 * The render key uses the classified entry's content inputs. UI state does not change this key.
 * Expanding a row or changing its diff view changes height without changing cached content.
 * Deriving this key from the height key would discard these unchanged values on each click:
 * - The extracted row model.
 * - The normalized command body.
 * - The highlighted ANSI output.
 * - The rendered Markdown.
 *
 * An edit diff is not among them: the diff worker client caches each diff by its
 * content (see diffWorkerClient), whatever this key is.
 */
export function renderKeyForEntry(entry: ClassifiedEntry): string {
  return `${entry.message.id}|${buildContentKey(contentKeyInputsOf(entry))}`
}

export interface ClassifiedEntryCacheDeps {
  /** The window's messages, in display order (read reactively). */
  messages: () => readonly AgentChatMessage[]
  /** The shared resolver's current request revision for this span. */
  requestRevision?: (identity: MessageSpanIdentity) => MessageRevision | undefined
  /**
   * Read the row's content version through the store's getMessageContentVersion.
   * A same-sequence body replacement increments this version in place.
   * Read it reactively, because that merge changes content without changing the fields that the window memo reads.
   * The version change makes the memo check freshness and rebuild the row.
   */
  contentVersionById?: (id: string) => number
  /**
   * The shared resolver's merged payload for one message, when available.
   * Without this value, preparation resolves the payload itself and can produce a second object for the same bytes.
   * The bubble and toolbar must use the same resolved object. The image tab must use that object also.
   */
  resolvedMessage?: (message: AgentChatMessage) => ResolvedMessage | undefined
  /** The shared resolver's one role decision for this message. */
  role: (message: AgentChatMessage) => ToolSpanRole
  /** Show otherwise-hidden messages (the debug preference). */
  showHiddenMessages: () => boolean
  /**
   * Whether these messages belong to a subagent's own transcript.
   * Read it reactively. A subagent tab appears before `listAgents` supplies its `parentAgentId`, and the child's stream starts immediately.
   * This value starts as false for early rows and changes to true when the parent link arrives.
   * That change must invalidate the cached entry.
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
   * Build the freshness value for `message` in one place.
   * `isEntryFresh` compares this value. `buildEntry` stores it.
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
    // Select a sibling by the resolved span role.
    // Some completed ACP updates use the tool_use category but hold the result role.
    // A result draws its request input. A request draws its own message alone.
    // No row records its own selected side twice.
    const selectedMessage = selected?.message ?? message
    const role = selectedMessage.spanId === ''
      ? 'other'
      : deps.role(selectedMessage)
    const request = role === 'result'
      ? deps.requestRevision?.(messageSpanIdentity(selectedMessage))
      : undefined
    return {
      revisionKey: rowRevisionKey({
        own,
        ...(request !== undefined
          ? { request }
          : {}),
      }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
      settingsLabelRevision: settingsLabelDependencyRevision(labelDependencies),
      role,
    }
  }
  /**
   * Reuse a cached entry only when every freshness field matches the current source.
   * `freshnessOf` supplies those fields.
   */
  const isEntryFresh = (cached: ClassifiedEntry | undefined, freshness: EntryFreshness): cached is ClassifiedEntry =>
    !!cached && shallowEqual(cached.freshness, freshness)
  const buildEntry = (message: AgentChatMessage, selected: ResolvedMessage | undefined, cached?: ClassifiedEntry): ClassifiedEntry => {
    // Use the resolver's parse when available, so each reader uses the same resolved payload.
    // A test or isolated preview can omit the resolver. Preparation then resolves the payload itself.
    const selectedMessage = selected?.message ?? message
    const collected = collectSettingsLabelDependencies(() => prepareMessage(selectedMessage, {
      ...(selected === undefined
        ? {}
        : { original: selected.original, resolved: selected.resolved }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    }))
    const prepared = collected.value
    const freshness = freshnessOf(message, selected, collected.dependencies)
    const openingSpanId = prepared.category.kind === 'tool_use' && freshness.role === 'request'
      ? selectedMessage.spanId
      : undefined
    // Compare `spanLines` with its saved string, because the cached proxy reads the current value after an in-place change.
    // Reuse the parse when the row still draws the same opening span and its stored lines match.
    const parsedSpanLines = cached && cached.spanLinesRef === selectedMessage.spanLines && cached.openingSpanId === openingSpanId
      ? cached.parsedSpanLines
      : parseSpanLines(selectedMessage.spanLines, openingSpanId)
    return {
      ...prepared,
      rowId: message.id,
      pending: false,
      parsedSpanLines,
      freshness,
      spanLinesRef: selectedMessage.spanLines,
      openingSpanId,
      settingsLabelDependencies: collected.dependencies,
    }
  }
  /**
   * Return the cached entry when it is fresh. Otherwise, rebuild and cache it.
   * Both visibility checks and entry construction use this function.
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
   * Remove entries that leave the window on every run.
   * Replacing the same number of IDs leaves the size unchanged, so a size check would retain old entries.
   * `hasVisibleEntries` derives from `visibleEntries`, so either public accessor uses this pruning path.
   * The scan processes only entries in the window cache.
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
    if (showHidden)
      return result
    // Decision 20 keeps this safety net for starts that older builds stored.
    // Current workers publish starts as live state. Keep this exception to the old-row removal rule.
    // Only a later end in the loaded window can hide a start. Mixed rows stay visible.
    const visible: ClassifiedEntry[] = []
    let laterEnd = false
    for (let index = result.length - 1; index >= 0; index--) {
      const entry = result[index]!
      if (laterEnd && entry.compactionStartOnly)
        continue
      if (entry.category.kind === 'notification')
        laterEnd ||= hasCompactionEnd(entry.category.entries)
      visible.push(entry)
    }
    return visible.reverse()
  })
  const hasVisibleEntries = createMemo(() => visibleEntries().length > 0)

  return {
    visibleEntries,
    hasVisibleEntries,
    getEntry: id => entryCache.get(id),
  }
}

/**
 * Whether the transcript draws a message as the request row of its span: the message has
 * a span, its span role is request, and its category is one that the transcript draws as a
 * row. The one rule for a message in the loaded window and for one outside it (see
 * createRequestRowPresence).
 *
 * A hidden request draws no request row, also while the debug preference lists it as its
 * raw payload, so the row of its result then draws the request side itself.
 */
function drawsRequestRow(spanId: string, role: ToolSpanRole, kind: MessageCategory['kind']): boolean {
  return spanId !== '' && role === 'request' && kind !== 'hidden'
}

/**
 * The span keys of the request rows that the list draws, from the entries that it lists
 * (see drawsRequestRow).
 */
export function listedRequestSpanKeys(entries: readonly ClassifiedEntry[]): ReadonlySet<string> {
  const keys = new Set<string>()
  for (const entry of entries) {
    if (drawsRequestRow(entry.message.spanId, entry.freshness.role, entry.category.kind))
      keys.add(messageSpanKey(entry.message))
  }
  return keys
}

export interface RequestRowPresenceDeps {
  /** The entries that the list draws (ClassifiedEntryCache.visibleEntries). */
  entries: Accessor<readonly ClassifiedEntry[]>
  /**
   * The entry of a message of the loaded window, hidden or not, or `undefined` for a
   * message outside it (ClassifiedEntryCache.getEntry).
   */
  windowEntry: (id: string) => ClassifiedEntry | undefined
  /**
   * The request of a span that the resolver holds: a message of the loaded window, or one
   * that a fetch of the span brought (MessageContextResolver.request).
   */
  request: (identity: MessageSpanIdentity) => ResolvedMessage | undefined
  /** The span role of a message (MessageContextResolver.role). */
  role: (message: AgentChatMessage) => ToolSpanRole
  /** Whether the messages belong to a subagent's own transcript (see ClassifiedEntryCacheDeps). */
  isChildTranscript?: () => boolean
}

/**
 * Whether the TRANSCRIPT draws the request of a span as its own row, as a row of the span
 * reads it (`ToolSpanRowPresence.request`): a result row beside its request row draws no
 * header and no request side. Call it within a reactive owner: it creates a memo.
 *
 * The answer must not follow the loaded window. A result row whose request lies in an
 * older page would otherwise draw the request side until that page loads, and then lose
 * it: the reader scrolls up to the result row, the older page arrives, and the row
 * shrinks by its header and its request side in view. On a phone a command summary wraps
 * to several lines, and the content moved by 170px. So the answer holds for the request
 * that the resolver holds, in the window or not:
 *
 * - A span whose request row the list draws: yes.
 * - A span whose request is in the loaded window, and the list draws no request row for
 *   it (a hidden request): no.
 * - A span whose request lies outside the window, and the resolver fetched it: the rule of
 *   the list for that message (see drawsRequestRow), so the row draws the same thing
 *   before and after the page of the request loads.
 * - A span whose request the browser does not hold: no. A row that draws from this answer
 *   waits for the fetch of its span before it reports a height (see
 *   MessageContextResolver.loadRequest), so it keeps its height when the answer becomes
 *   known.
 */
export function createRequestRowPresence(deps: RequestRowPresenceDeps): (identity: MessageSpanIdentity) => boolean {
  const listed = createMemo(() => listedRequestSpanKeys(deps.entries()), new Set<string>(), { equals: shallowEqualSets })
  // The category of a request outside the window depends only on its own payload and on
  // the kind of transcript, so it is kept for each resolved payload and each kind. The role
  // is read every time: it depends on the other messages of the span.
  const outsideKinds = new WeakMap<ResolvedMessage, Map<boolean, MessageCategory['kind']>>()
  const outsideKind = (request: ResolvedMessage): MessageCategory['kind'] => {
    const isChildTranscript = deps.isChildTranscript?.() ?? false
    let kinds = outsideKinds.get(request)
    if (kinds === undefined) {
      kinds = new Map()
      outsideKinds.set(request, kinds)
    }
    let kind = kinds.get(isChildTranscript)
    if (kind === undefined) {
      kind = prepareMessage(request.message, { original: request.original, resolved: request.resolved, isChildTranscript }).category.kind
      kinds.set(isChildTranscript, kind)
    }
    return kind
  }
  return (identity) => {
    if (listed().has(messageSpanKey(identity)))
      return true
    const request = deps.request(identity)
    if (request === undefined)
      return false
    // The list holds the request and draws no request row for it: `listed` holds every
    // entry of the window that draws one.
    if (deps.windowEntry(request.message.id) !== undefined)
      return false
    return drawsRequestRow(request.message.spanId, deps.role(request.message), outsideKind(request))
  }
}
