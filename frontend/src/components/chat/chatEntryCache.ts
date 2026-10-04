import type { Accessor } from 'solid-js'
import type { ContentKeyInputs } from './chatRowGeometry'
import type { ResolvedMessage } from './messageContextResolver'
import type { PreparedMessage } from './rowPreparation'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageRevision, MessageSpanIdentity, ToolSpanRole } from '~/lib/messageSpan'
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
 * It includes the request revision only for a result role and the result revision only for a request role.
 * An unrelated sibling revision changes no field and causes no rebuild.
 */
export interface EntryFreshness {
  /** The row's exact revision dependencies (see chatRevisionKey.ts). */
  revisionKey: string
  /** Whether the classifier read these messages in a subagent's own transcript. */
  isChildTranscript: boolean
  /** Revisions of the exact display-label groups that this row reads. */
  settingsLabelRevision: string
}

/**
 * A prepared message with its parsed span lines and the freshness value at preparation time.
 * The bubble receives this whole entry as its prepared message.
 * The entry and bubble therefore cannot hold different answers for the same row.
 * Separate original and category props let the bubble resolve a different payload.
 * That could make the transcript classify raw bytes while extraction read merged content.
 */
export type ClassifiedEntry = PreparedMessage & {
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
  return buildHeightKey({ ...contentKeyInputsOf(entry), uiVersion })
}

/**
 * The render key uses the classified entry's content inputs. UI state does not change this key.
 * Expanding a row or changing its diff view changes height without changing cached content.
 * Deriving this key from the height key would discard these unchanged values on each click:
 * - The extracted row model.
 * - The normalized command body.
 * - The Myers diff.
 * - The rendered Markdown.
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
    // A result draws its request input. A request can draw hidden result data.
    // No row records its own selected side twice.
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
      ...(selected === undefined ? {} : { original: selected.original, resolved: selected.resolved }),
      isChildTranscript: deps.isChildTranscript?.() ?? false,
    }))
    const prepared = collected.value
    // Compare `spanLines` with its saved string, because the cached proxy reads the current value after an in-place change.
    // A new window object with identical content reuses the parse. Changed content requires a new parse.
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
    return result
  })
  const hasVisibleEntries = createMemo(() => visibleEntries().length > 0)

  return { visibleEntries, hasVisibleEntries, getEntry: id => entryCache.get(id) }
}
