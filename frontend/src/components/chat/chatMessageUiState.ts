import type { Setter } from 'solid-js'
import type { MessageUiKey } from './messageUiKeys'
import { createSignal } from 'solid-js'
import { capMapInsertionOrder } from '~/lib/mapLru'

// ---------------------------------------------------------------------------
// Per-message UI state
//
// Lifted, id-keyed UI state for chat rows (a per-message diff-view override and a
// per-message boolean flag map) so a toggle survives <For> re-renders as messages
// are added. Extracted from ChatView as its own unit, mirroring the other chat
// concerns the windowing work pulled out (the classified-entry cache, the scroll
// hook, the virtualizer).
//
// The state deliberately OUTLIVES the windowed message list: a row trimmed out of
// the in-memory window keeps its expand / diff-view choice when it scrolls back in
// and re-mounts. It is therefore NOT pruned by message presence -- with the list
// windowed, a shrink can be a routine trim rather than a deletion, so pruning on
// it would silently reset the user's choices. Memory is bounded instead by an
// insertion-order cap, far above any plausible number of rows a user toggles, so a
// long session can't grow the maps without limit. A genuinely-deleted row's stale
// entry is harmless (its row never renders) and ages out under the cap.
//
// The cap PROTECTS the currently-rendered rows (the virtualizer's mounted set,
// passed in as `protectedIds`): an on-screen row toggled across a long session
// must never be the eviction target, or its choice would silently revert WHILE
// VISIBLE. This mirrors the virtualizer's height cache, which protects the same
// set for the same reason; only off-screen rows are ever evicted.
// ---------------------------------------------------------------------------

/**
 * Upper bound on retained per-message UI-state entries. Far above any plausible
 * number of distinct rows a user toggles in one session, so it never evicts in
 * practice -- it exists purely to keep the maps from growing without limit.
 */
const MAX_UI_STATE_ENTRIES = 1024

/**
 * Cap the per-message UI-state maps at MAX_UI_STATE_ENTRIES, dropping the
 * insertion-order-oldest entries -- but NEVER a `protect`ed (currently-rendered)
 * id, so a visible row's choice can't revert under the cap. The single home for the
 * "UI state is bounded by a cap, not by message presence" rule; delegates to the
 * shared insertion-order LRU primitive so this and the virtualizer's height cache
 * don't repeat the eviction loop. Mutates and returns the same Map.
 */
export function capInsertionOrder<V>(map: Map<string, V>, protect?: ReadonlySet<string>): Map<string, V> {
  return capMapInsertionOrder(map, MAX_UI_STATE_ENTRIES, { protect })
}

/**
 * Insert/update `key` at the MRU (insertion-order-newest) end, then cap. A plain
 * `Map.set` on an EXISTING key keeps its original position, so an actively
 * re-toggled off-screen row would drift toward the eviction front and be dropped
 * before never-touched newer rows. Deleting first re-inserts it as newest, making
 * the cap a true recency-aware LRU. Mutates and returns the same Map.
 */
function touchAndCap<V>(map: Map<string, V>, key: string, value: V, protect?: ReadonlySet<string>): Map<string, V> {
  map.delete(key)
  map.set(key, value)
  return capInsertionOrder(map, protect)
}

export interface MessageUiState {
  /** The user's per-message diff-view override, or undefined to use the global pref. */
  getLocalDiffView: (messageId: string) => 'unified' | 'split' | undefined
  setLocalDiffView: (messageId: string, view: 'unified' | 'split') => void
  /** A per-message boolean UI flag (expand/collapse toggles), or undefined for the renderer default. */
  getMessageUiBool: (messageId: string, key: MessageUiKey) => boolean | undefined
  setMessageUiBool: (messageId: string, key: MessageUiKey, value: boolean) => void
  /**
   * A per-message version that bumps on every real UI-state change (a diff-view or
   * boolean-flag toggle that actually changed a value). Read REACTIVELY: ChatView
   * folds it into the virtualizer's per-row height key so a toggle on an
   * OFF-SCREEN (premeasured) row invalidates its cached DOM height instead of
   * leaving the offset map stale until the row re-mounts. Starts at 0 for an
   * untouched row.
   */
  getUiVersion: (messageId: string) => number
}

export interface CreateMessageUiStateOptions {
  /**
   * The currently-rendered row ids the cap must never evict, read at eviction time
   * (so it tracks the live mounted set). Wired to the virtualizer's `mountedIds` in
   * ChatView. Omitted -> no protection (the prior behavior), for standalone tests.
   */
  protectedIds?: () => ReadonlySet<string>
}

/**
 * Create the per-message UI-state store. Call within a component's reactive owner
 * (it allocates signals); the returned accessors are stable for the component's
 * lifetime.
 */
export function createMessageUiState(opts: CreateMessageUiStateOptions = {}): MessageUiState {
  const [diffViewOverrides, setDiffViewOverrides] = createSignal<Map<string, 'unified' | 'split'>>(new Map())
  const [messageUiState, setMessageUiState] = createSignal<Map<string, Map<string, boolean>>>(new Map())
  // Per-id change counter (see getUiVersion). Capped + protected like the state maps
  // so it can't grow without bound; a touched row's version aging out under the cap
  // is harmless (it only affects an OFF-SCREEN measured height, which re-measures on
  // mount/premeasure).
  const [uiVersions, setUiVersions] = createSignal<Map<string, number>>(new Map())

  // Resolved at each cap (eviction time), so it reflects the rows mounted right now
  // rather than a snapshot from store-creation time.
  const protect = () => opts.protectedIds?.()

  const bumpUiVersion = (messageId: string) => {
    setUiVersions((prev) => {
      const next = new Map(prev)
      return touchAndCap(next, messageId, (next.get(messageId) ?? 0) + 1, protect())
    })
  }
  const getUiVersion = (messageId: string): number => uiVersions().get(messageId) ?? 0

  /**
   * The optimistic-set ceremony both setters share: skip the clone (and the
   * consumer notification) when `isUnchanged`, otherwise clone, write the
   * `buildValue` result at the MRU end + cap, and bump the UI version exactly once.
   * Centralizing it keeps the two setters from drifting on the no-op-skip OR the
   * version bump -- both load-bearing reactive correctness (a missed skip notifies
   * consumers on a no-op; a missed bump leaves an off-screen row's cached estimate
   * stale). Closes over `protect` and `bumpUiVersion`.
   */
  const setIfChanged = <V>(
    setSignal: Setter<Map<string, V>>,
    messageId: string,
    isUnchanged: (prev: Map<string, V>) => boolean,
    buildValue: (prev: Map<string, V>) => V,
  ) => {
    let changed = false
    setSignal((prev) => {
      if (isUnchanged(prev))
        return prev
      changed = true
      const next = new Map(prev)
      return touchAndCap(next, messageId, buildValue(prev), protect())
    })
    if (changed)
      bumpUiVersion(messageId)
  }

  const getLocalDiffView = (messageId: string) => diffViewOverrides().get(messageId)
  const setLocalDiffView = (messageId: string, view: 'unified' | 'split') =>
    setIfChanged(
      setDiffViewOverrides,
      messageId,
      prev => prev.get(messageId) === view,
      () => view,
    )

  const getMessageUiBool = (messageId: string, key: MessageUiKey): boolean | undefined =>
    messageUiState().get(messageId)?.get(key)
  const setMessageUiBool = (messageId: string, key: MessageUiKey, value: boolean) =>
    setIfChanged(
      setMessageUiState,
      messageId,
      prev => prev.get(messageId)?.get(key) === value,
      (prev) => {
        const current = new Map(prev.get(messageId) ?? [])
        current.set(key, value)
        return current
      },
    )

  return { getLocalDiffView, setLocalDiffView, getMessageUiBool, setMessageUiBool, getUiVersion }
}
