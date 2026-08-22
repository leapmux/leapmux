import type { FileDiffBase, FileOpenSource, FileViewMode } from './tab.types'
import type { AgentProvider, AgentStatus, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import type { HLC, UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import { createEffect, createMemo } from 'solid-js'
import { createStore, produce, unwrap } from 'solid-js/store'
import { KEY_TAB_MRU, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { hlcIsZero } from '~/lib/crdt/hlc'
import { sameKeys } from '~/lib/sameKeys'
import { shallowEqual, shallowEqualArrays } from '~/lib/shallowEqual'

/**
 * Everything about a tab that the CRDT does NOT carry.
 *
 * `TabRecord` owns identity and placement — `tab_type`, `tab_id`, `tile_id`,
 * `position`, `worker_id`, `display_mode`, `file_view_mode`, `file_diff_base`,
 * `tombstone_at`. Those are read from the projection at join time and are
 * deliberately absent here; storing them a second time is what
 * `reconcileFromProjection` used to exist to repair.
 *
 * What is left splits in two, and both are keyed by tab id — which is globally
 * unique across the user's whole account per the `TabRecord` protocol invariant,
 * so one flat map holds every workspace at once:
 *
 *   - **worker-sourced**: fetched by `listAgents` / `listTerminals` and kept
 *     fresh by the per-worker `WatchEvents` stream. The hub never sees most of
 *     it and the CRDT has no field for any of it.
 *   - **client-local**: MRU, notification dot, and the file-open provenance that
 *     only this browser knows.
 *
 * The four sections below are what `tabView.assemble` reads, expressed as types
 * rather than as comment headers. `TabMetadata` is their intersection, so
 * `patch` still accepts any of them and no call site changes. What the split
 * buys is that the partition this doc has always described is now something the
 * compiler and the reader share: a field added to `AgentMeta` and forgotten in
 * `assemble`'s AGENT arm is a type error, where before both were prose.
 *
 * Deliberately NOT a discriminated union keyed on tab type. Doing that would
 * mean storing the kind here, and `tab_type` is owned by the projection --
 * storing it a second time is precisely what `reconcileFromProjection` existed
 * to repair. A kind PARAMETER on `patch` was the alternative; it would make a
 * mis-targeted write a compile error at ~72% of call sites, but the discriminant
 * would be a caller-supplied literal with nothing tying it to the tab id, and
 * `patchMatching` cannot take one at all. The worst outcome of a mis-targeted
 * write today is a field no `assemble` branch reads back -- dead, not corrupting.
 */

/** Fields every tab kind carries. */
export interface SharedMeta {
  title?: string
  hasNotification?: boolean
  /**
   * Set once the worker has actually answered for this tab.
   *
   * Written ONLY by the paths that HOLD a worker response for this exact tab:
   * `useTabHydrators` (the `listAgents` / `listTerminals` reply) and the local
   * open paths, which already carry the `OpenAgent` / `OpenTerminal` response
   * that answers the same question. Deliberately NOT derived from any payload
   * field: sniffing one ("is `cols` set?", "is `agentStatus` set?") makes the
   * predicate forgeable by anything else that writes that field -- a local
   * terminal resize patches `cols` within a frame of mount, which retires the
   * tab from the candidate set before the worker has ever replied and
   * permanently suppresses its title, status and scrollback.
   *
   * The local paths MUST set it. They pass every payload-shaped predicate
   * already, so omitting the flag puts a tab this client just opened straight
   * back into the hydration candidate set: a redundant round-trip whose reply
   * is applied with none of the live handlers' guards, which drags a running
   * terminal back to STARTING, rewinds `lastOffset` (replaying bytes xterm has
   * already drawn) and overwrites an in-flight optimistic agent-settings edit.
   */
  hydrated?: boolean
  /**
   * Monotonic activation counter. Higher = more recently activated. Never enters
   * the CRDT (two devices should not fight over which tab was touched last) and
   * never reaches the projection; it orders the MRU views within one client.
   *
   * Persisted to sessionStorage as `KEY_TAB_MRU` (`Record<tabId, number>`) so a
   * reload restores the prior ordering instead of leaving every tab at zero. A
   * zero score after reload used to make `mruHead` silently fall back to
   * `tabs[0]` (position order), which corrupted close-promotion, `mruOrder`, and
   * the `workingDir`/`homeDir` seed a new agent inherits from the MRU tab.
   *
   * Seeded eagerly in `createTabMetadataStore` (see `loadMru`) because `mruHead`
   * is read during render before any CRDT-gated hook could rehydrate it.
   */
  mru?: number
  workingDir?: string
  createdAt?: string
  /** Repo toplevel linking this tab to the repo-keyed git store entry. */
  gitToplevel?: string
}

export interface AgentMeta {
  agentProvider?: AgentProvider
  agentStatus?: AgentStatus
  agentSessionId?: string
  optionValues?: Record<string, string>
  optionGroups?: AvailableOptionGroup[]
  /**
   * Subagent linkage. parentAgentId is set only for virtual child agents
   * (protoToAgentTabFields hydrates it from AgentInfo). Without it in AgentMeta
   * the field is written into the metadata store by patch() but never copied
   * back out by assemble(), so every consumer reading the assembled Tab sees
   * undefined -- composer gating, the corner icon, MRU exclusion and
   * rootAgentIdFor all silently break.
   */
  parentAgentId?: string
  /** Backend-authoritative: a child that accepts user messages is steerable. */
  acceptsMessages?: boolean
}

export interface TerminalMeta {
  terminalStatus?: TerminalStatus
  shellStartDir?: string
  screen?: Uint8Array
  /**
   * Cumulative PTY byte offset this tab has already applied to its xterm.
   * Seeded at hydration from the backend's `screen_end_offset` (the offset at
   * the end of `screen`, which equals `screen.length` before the ring wraps and
   * is larger once bytes fall off), then advanced monotonically as TerminalData
   * events arrive. Echoed back as `WatchTerminalEntry.after_offset` on
   * resubscribe so the handler skips bytes we already have.
   *
   * Read from HERE, never through the assembled `Tab` -- and that is the point.
   * The worker emits one `TerminalEvent_Data` per `ptmx.Read` with no
   * coalescing, so this is written at PTY-read frequency. `tabView`'s join
   * subscribes to every metadata field it reads, so keeping this one on the
   * assembled `Tab` made a busy terminal re-run the O(all-tabs) join and re-sort
   * every workspace on each chunk (measured 0.247ms at 100 tabs, 0.750ms at
   * 300). Reading it straight off the store is also strictly more correct for
   * the resubscribe cursor: it cannot lag behind a join that has not recomputed.
   */
  lastOffset?: number
  /**
   * Set when this client knows it lost terminal bytes (the pending-frame
   * queue evicted oldest frames for a terminal that never mounted). The next
   * watch plan subscribes with afterOffset 0, which the worker answers with a
   * full snapshot; the flag clears when that snapshot applies. Local
   * recovery state, like lastOffset — never synced.
   */
  needsResync?: boolean
  /** PTY-driven title from OSC 0/2; does not replace a user rename (`title`). */
  ptyTitle?: string
  /** Task progress from OSC 9;4 (ConEmu / Windows Terminal protocol). */
  progressState?: import('~/generated/leapmux/v1/terminal_pb').TerminalProgress_State
  progressPercent?: number
  cols?: number
  rows?: number
  contentReady?: boolean
}

/**
 * Shared by name, NOT by meaning: an agent's startup error is its provider
 * failing to launch, a terminal's is the shell. They live in one place only
 * because `assemble` copies them into both shapes.
 */
export interface StartupMeta {
  startupError?: string
  startupMessage?: string
}

export interface FileMeta {
  /**
   * Registered with the worker over E2EE so peers can resolve it; the hub never
   * sees a file path, which is why it cannot live in the CRDT.
   */
  filePath?: string
  fileOpenSource?: FileOpenSource
  /**
   * `TabRecord` HAS registers for these three (`display_mode`,
   * `file_view_mode`, `file_diff_base`) and `apply.ts` merges them INBOUND, but
   * this client never emits them — `ops.ts` deliberately has no builder for any
   * of the three. The old tab store wrote all three to local state only, so in
   * practice they have always been per-client and never synced. They stay here
   * to preserve exactly that: reading them from the projection instead would
   * return empty for every tab and silently break the file viewer's mode toggle.
   *
   * Syncing view mode across devices would mean adding both the builders and a
   * write path. That may well be desirable, but it is a behaviour change nobody
   * asked for and belongs in its own change, not smuggled in by a refactor.
   */
  displayMode?: string
  fileViewMode?: FileViewMode
  fileDiffBase?: FileDiffBase
}

/**
 * Everything about a tab the CRDT does not carry. The intersection is what
 * `patch` accepts, so a caller may still write any field -- see `SharedMeta`
 * for why that laxity is deliberate rather than an oversight.
 */
export type TabMetadata = SharedMeta & AgentMeta & TerminalMeta & StartupMeta & FileMeta

/** Narrowing helpers for callers that only hold a `FileViewMode`-ish value. */
export type { FileDiffBase, FileViewMode }

/**
 * The tab ids a metadata sweep must treat as live: the CRDT holds an
 * untombstoned record for them.
 *
 * A tombstone is what says the tab is gone. `applyTombstoneTab` REPLACES the
 * record with a tombstoned one rather than deleting the map key, so
 * `Object.keys(state.tabs)` alone lists every tab the account ever opened
 * -- reading that as "still here" made this sweep a no-op and let closed
 * terminals' `screen` buffers accumulate for the life of the page, the exact
 * leak its caller exists to prevent.
 *
 * Deliberately the raw `TabRecord` set, NOT the projection's `ownedTabs`. A tab
 * whose tile chain is momentarily unresolvable -- mid-batch during a close's
 * undo-split, or between a Batch frame and the `EntityMaterialized` frame that
 * creates its new tile -- drops out of the projection while remaining perfectly
 * alive. Sweeping on that signal deletes live state: the tab reappears a tick
 * later with its title, git badges and terminal scrollback gone for good, and
 * nothing refetches them because the tab already "exists".
 *
 * Whether a tab EXISTS and where it SITS are different questions. Only the
 * first one may retire its metadata.
 *
 * NOT-LIVE IS NOT THE SAME AS RETIRED, and that distinction belongs to the
 * caller -- see {@link useMetadataSweep}. An id this set omits may be a tab the
 * CRDT has not heard of YET rather than one that went away.
 */
export function liveTabIds(state: { tabs: Record<string, { tombstoneAt?: HLC } | undefined> }): Set<string> {
  const live = new Set<string>()
  for (const [tabId, rec] of Object.entries(state.tabs)) {
    if (rec && hlcIsZero(rec.tombstoneAt))
      live.add(tabId)
  }
  return live
}

/**
 * Merge `fields` into `target`, SKIPPING undefined values.
 *
 * The skip is load-bearing, not an optimisation: a partial update from one
 * source (a git-status event, a hydration reply) must not blank fields another
 * source owns. Both write paths below need exactly this loop, and they used to
 * carry byte-identical copies of it — which is one copy too many for a rule
 * whose whole point is that it holds everywhere. It also means a producer that
 * wants to CLEAR a field has to write a real value (`''`, `false`), never
 * `undefined`; see `protoToTerminalTabFields`.
 */
function mergeDefined(target: TabMetadata, fields: TabMetadata): void {
  // `unwrap` because `target` is a store DRAFT: Solid wraps every nested plain
  // object and array in a proxy, so reading `target.agentGitStatus` hands back a
  // proxy that can never be `Object.is`-equal to the raw object a producer is
  // trying to write. Comparing against the unwrapped row is what makes the
  // check below answer about VALUES rather than about proxy identity.
  const current = unwrap(target) as Record<string, unknown>
  for (const [k, v] of Object.entries(fields)) {
    if (v !== undefined && !sameStoredValue(current[k], v))
      (target as Record<string, unknown>)[k] = v
  }
}

/**
 * Would writing `next` over `prev` change anything a consumer can observe?
 *
 * Reference reuse for object-valued fields, enforced HERE — at the single write
 * point — rather than by each producer remembering to thread the previous value
 * in and report `undefined` for "unchanged".
 *
 * The worker re-decodes and re-ships whole payloads on every push, so an
 * unchanged repo or catalog arrives as an equal-but-FRESH object. A `Tab` is a
 * join result compared with `shallowEqual` (per-key `Object.is`) and `<For>`
 * keys its rows by that identity, so writing one of those back re-keys the tab
 * and tears down every row rendered from it. Skipping the write is
 * observationally identical — the values are equal by content, and the tab keeps
 * the object it already had — which is exactly what makes a no-op re-broadcast a
 * no-op. Primitives fall through to the assignment and Solid's own store
 * equality drops them, so this only ever decides the object cases.
 *
 * `ArrayBuffer.isView` FIRST, and it is not an optimisation. `screen` is a
 * `Uint8Array`; `Array.isArray` is false for it and `shallowEqual` would fall
 * through to `Object.keys`, allocating one index string PER BYTE of a serialized
 * terminal scrollback to answer a question reference identity already answers.
 * The writer replaces that buffer rather than mutating it, so reference identity
 * IS the correct test.
 *
 * Arrays element-wise: `optionGroups` reaches this already
 * reference-stabilised per group by `mergeStableOptionGroupRefs`, which knows
 * how to compare a proto and is where that belongs. All this needs to see is
 * whether the stabiliser handed back the same elements.
 */
function sameStoredValue(prev: unknown, next: unknown): boolean {
  if (Object.is(prev, next))
    return true
  if (ArrayBuffer.isView(prev) || ArrayBuffer.isView(next))
    return false
  if (Array.isArray(prev) && Array.isArray(next))
    return shallowEqualArrays(prev, next)
  return shallowEqual(prev, next)
}

/**
 * Read and validate the persisted MRU stamp map.
 *
 * Returns the cleaned `Record<tabId, number>` (finite, positive stamps only)
 * plus the high-water mark, or `{ stamps: {}, max: 0 }` if storage is empty or
 * every entry was invalid. Validating here means the seed loop below can index
 * straight in without re-checking.
 *
 * One JSON blob keyed by tab id — no workspace dimension, because `tabMetadata`
 * itself is keyed by globally-unique tab id and holds every workspace at once.
 * Exact-match (a singleton), not a templated prefix family: see `KEY_TAB_MRU`.
 */
function loadMru(): { stamps: Record<string, number>, max: number } {
  const raw = sessionStorageGet<unknown>(KEY_TAB_MRU)
  if (!raw || typeof raw !== 'object' || Array.isArray(raw))
    return { stamps: {}, max: 0 }
  const stamps: Record<string, number> = {}
  let max = 0
  for (const [tabId, stamp] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof stamp === 'number' && Number.isFinite(stamp) && stamp > 0) {
      stamps[tabId] = stamp
      if (stamp > max)
        max = stamp
    }
  }
  return { stamps, max }
}

/**
 * Derive the persisted MRU map from a `byTabId` snapshot: `{ tabId: mru }` for
 * every row carrying a stamp. Kept separate from the persister so the seed
 * (which builds the snapshot, not the store) and the dedupe cache (which holds
 * the last serialised value) can share one canonical projection.
 */
function mruSnapshot(byTabId: Record<string, TabMetadata>): Record<string, number> {
  const mru: Record<string, number> = {}
  for (const [tabId, meta] of Object.entries(byTabId)) {
    if (meta.mru !== undefined)
      mru[tabId] = meta.mru
  }
  return mru
}

/**
 * Write the MRU stamp map back, deduped against the last value written.
 *
 * Every mutation that changes an `mru` field (`touchMru`) or the live row set
 * (`remove`, `dropTabs`) calls this. The map is small (one entry per tab) and
 * `touchMru` is already a no-op at the head, so writes happen only on genuine
 * ordering changes. It additionally skips when nothing carries an `mru` stamp
 * yet — the very first activation writes, a metadata-only `patch` never does.
 *
 * `initialJson` primes the dedupe cache with the canonical serialisation of the
 * seed the store was constructed from, so the first mutation after a clean
 * reload rewrites only if it genuinely changes the map.
 */
function createMruPersister(initialJson: string) {
  let lastWritten = initialJson
  return (byTabId: Record<string, TabMetadata>) => {
    const mru = mruSnapshot(byTabId)
    const json = JSON.stringify(mru)
    if (json === lastWritten)
      return
    lastWritten = json
    sessionStorageSet(KEY_TAB_MRU, mru)
  }
}

export function createTabMetadataStore() {
  // Seed MRU eagerly, before any render can read `mruHead`. The bootstrap
  // sequence (auth → WebSocket → projection fills) means `mruHead` is consulted
  // during render before a CRDT-gated hook could rehydrate it, so a lazy/hook
  // read-back would race render and lose. An empty seed (first run, or the
  // sessionStorage key expired) leaves the counter at zero, matching the old
  // behaviour. See the `mru` field doc for why this is persisted at all.
  const { stamps: seedStamps, max: seedMax } = loadMru()
  const seedByTabId: Record<string, TabMetadata> = Object.fromEntries(
    Object.entries(seedStamps).map(([tabId, mru]) => [tabId, { mru }] satisfies [string, TabMetadata]),
  )
  const persistMru = createMruPersister(JSON.stringify(mruSnapshot(seedByTabId)))

  const [state, setState] = createStore<{ byTabId: Record<string, TabMetadata> }>({ byTabId: seedByTabId })
  let mruCounter = seedMax

  return {
    state,

    get(tabId: string): TabMetadata | undefined {
      return state.byTabId[tabId]
    },

    /**
     * Merge `fields` into a tab's metadata, creating the row if absent.
     * Undefined values are skipped rather than written, so a partial update from
     * one source (say a git-status event) can't blank fields another source owns.
     */
    patch(tabId: string, fields: TabMetadata) {
      setState(produce((s) => {
        const existing = s.byTabId[tabId] ?? {}
        mergeDefined(existing, fields)
        s.byTabId[tabId] = existing
      }))
    },

    /**
     * Merge `fields` into a tab's metadata ONLY when a row already exists.
     *
     * The counterpart to `patch` for a writer that fires on the way DOWN. `patch`
     * creates the row, which is what an open path needs -- it writes the metadata
     * before the op that creates the tab, and the row must survive that window
     * (see {@link useMetadataSweep}). A teardown writer has the opposite need: by
     * the time it runs, the tab may already be retired, and creating the row
     * again resurrects it permanently, because the sweep retires an id once and
     * that id can never be live again.
     *
     * The terminal screen-capture sink is the writer this exists for. It fires
     * from `disposeTerminalInstance`, which the view's unmount defers to a
     * microtask -- so for a terminal closed on another device it runs AFTER the
     * tombstone already reclaimed the row, and a `patch` there strands a full
     * serialized scrollback for the life of the page.
     */
    patchExisting(tabId: string, fields: TabMetadata) {
      if (state.byTabId[tabId] === undefined)
        return
      setState(produce((s) => {
        const existing = s.byTabId[tabId]
        if (!existing)
          return
        mergeDefined(existing, fields)
      }))
    },

    /**
     * Merge `fields` into every tab whose metadata matches. This is how a branch
     * rename or a worker-offline sweep reaches EVERY workspace at once — under
     * the old split it had to be a per-workspace fan-out across the active store
     * plus each registry snapshot.
     */
    patchMatching(predicate: (meta: TabMetadata, tabId: string) => boolean, fields: TabMetadata) {
      setState(produce((s) => {
        for (const [tabId, meta] of Object.entries(s.byTabId)) {
          if (!predicate(meta, tabId))
            continue
          mergeDefined(meta, fields)
        }
      }))
    },

    remove(tabId: string) {
      setState(produce((s) => {
        delete s.byTabId[tabId]
      }))
      persistMru(state.byTabId)
    },

    /**
     * Drop metadata for every tab id in `retired`.
     *
     * States what to DELETE, not what to keep, and that direction is the point:
     * a row the caller has formed no opinion about is never collateral. The one
     * caller is {@link useMetadataSweep} -- read its doc before changing this.
     * "Retired" is neither "not in the projection" (a tab whose tile chain is
     * momentarily unresolvable leaves the projection while remaining alive) nor
     * "not live" (a tab whose metadata is written before its creation op is not
     * live either, and it is about to be).
     *
     * "The worker has answered for this tab" is a field on the row swept here
     * (`hydrated`), deliberately NOT a companion cache keyed by tab id. Such a
     * cache would be swept on a different schedule than the row it describes,
     * so it could still say "already answered" for a row that is gone -- and
     * the FILE hydrator, reading that, would never ask again, stranding the tab
     * with no path for the life of the page.
     */
    dropTabs(retired: Set<string>) {
      if (retired.size === 0)
        return
      setState(produce((s) => {
        for (const tabId of retired)
          delete s.byTabId[tabId]
      }))
      persistMru(state.byTabId)
    },

    /**
     * Stamp a tab as most-recently-used and return the counter value used.
     *
     * IDEMPOTENT AT THE HEAD. A tab already carrying the newest stamp is already
     * the winner of every MRU comparison in the account, so re-stamping it changes
     * no ordering anywhere -- it only bumps a field on the tab, and a `Tab` is a
     * join result whose object identity every `<For>` keys its rows by (see
     * tabView). A no-op stamp therefore tore down and rebuilt the tab strip, the
     * sidebar tree, and the tile's whole pane for nothing.
     *
     * This is the common path, not an edge case: clicking anywhere inside a tile
     * re-activates that tile's ALREADY-active tab (TileRenderer's `onFocus`), so
     * every click paid a full rebuild -- including the click that ends a
     * drag-select, which destroyed the selection the user had just made.
     *
     * Persisted (deduped) so a reload restores the ordering rather than leaving
     * every tab at zero — see the `mru` field doc.
     */
    touchMru(tabId: string): number {
      if (state.byTabId[tabId]?.mru === mruCounter)
        return mruCounter
      mruCounter += 1
      this.patch(tabId, { mru: mruCounter })
      persistMru(state.byTabId)
      return mruCounter
    },
  }
}

export type TabMetadataStore = ReturnType<typeof createTabMetadataStore>

/**
 * Drop metadata rows for tabs the CRDT no longer has.
 *
 * Lives here rather than in the shell because every half it coordinates does —
 * {@link liveTabIds} and `dropTabs` — and the rule binding them ("read
 * `state.tabs`, NOT the projection's `ownedTabs`") was previously written out
 * three times, twice verbatim, because the caller sat in a different module.
 *
 * Nothing else drops these rows: a close is a tombstone, not a store call, and
 * terminal `screen` buffers are the largest thing in here, so without the sweep
 * a long session accumulates the scrollback of every terminal it ever opened.
 *
 * A row is retired when its tab was LIVE and then stopped being live — never
 * merely because it is not live now. `seen` is what encodes the difference, and
 * it is the whole reason this function holds state at all.
 *
 * Retiring every not-live id instead was a defect. An id absent from
 * `state.tabs` is not a tab that went away; it is one the CRDT has not heard of
 * YET. Every open path writes a tab's metadata BEFORE emitting the op that
 * creates it (`openTabInFocusedTile`, `openSubagentTab`), because the
 * projection renders the tab synchronously and patching afterwards paints it
 * untitled. Any effect flush inside that window — and the metadata write itself
 * causes one, since a store write outside a batch runs the effects a previous
 * update queued — caught the row of a tab that was about to exist and deleted
 * it. A file opened from a git filter tab lost `fileViewMode`, `fileDiffBase`
 * and `fileOpenSource` that way, so it opened as a plain read of the working
 * copy with no diff-mode toolbar at all; the FILE hydrator then re-fetched the
 * path, which is why the tab still looked right.
 *
 * "Was live, now is not" covers both ways a tab leaves: a tombstone (the
 * ordinary close), and `EntityRemoved`, which DELETES the record outright when
 * a workspace moves out of the subscriber's allowed set. Testing for a
 * tombstone alone would leak every row of the second kind.
 *
 * `seen` is pruned as it retires, so it holds the live tabs plus whatever is
 * in flight rather than every tab of the session.
 *
 * The memo compares the live tab-id SET, not the raw tick. `crdtState` is
 * `{ equals: false }` and fires on every CRDT batch — ~60/s while the user
 * drags a tile or a floating window, none of which create or retire a tab.
 * Without the set-equality memo each of those frames also paid a walk over
 * every metadata row. The `liveTabIds` walk itself still runs per tick (nothing
 * cheaper distinguishes "a tombstone landed" from any other op — a tombstone
 * REPLACES the record rather than removing the key, so neither identity nor key
 * count moves), but it is now the only per-tick cost and it feeds a memo that
 * stays quiet until the set actually changes.
 *
 * Must be called inside a SolidJS reactive root.
 */
export function useMetadataSweep(
  crdtState: () => UserCrdtState | null,
  metadata: TabMetadataStore,
): void {
  const liveTabIdSet = createMemo<Set<string> | null>(
    () => {
      const state = crdtState()
      return state ? liveTabIds(state) : null
    },
    null,
    { equals: sameKeys },
  )

  // The tab ids the CRDT has reported live at least once. See above: this is
  // what tells a tab that WENT AWAY from one that has not ARRIVED.
  //
  // Seeded with the rows the store already holds, which at construction are the
  // ones it restored from the persisted MRU map. Those are tabs that WERE live
  // in an earlier session, so "was live, now is not" is exactly true for them --
  // and without the seed nothing ever reclaims a row for a tab closed on another
  // device while this page was away, because an id the CRDT never reports can
  // never enter this set.
  const seen = new Set<string>(Object.keys(metadata.state.byTabId))

  // Whether the CRDT has reported a non-empty tab set yet. A cold start
  // publishes an EMPTY manager and fills it from the server a moment later, so
  // the seeded ids must not be retired against that first empty state -- every
  // restored MRU stamp would go, for tabs that are about to arrive.
  let sawTabs = false

  createEffect(() => {
    const live = liveTabIdSet()
    if (!live)
      return
    if (!sawTabs) {
      if (live.size === 0)
        return
      sawTabs = true
    }
    const retired = new Set<string>()
    for (const tabId of seen) {
      if (!live.has(tabId))
        retired.add(tabId)
    }
    for (const tabId of retired)
      seen.delete(tabId)
    for (const tabId of live)
      seen.add(tabId)
    metadata.dropTabs(retired)
  })
}
