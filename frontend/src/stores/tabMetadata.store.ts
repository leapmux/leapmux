import type { FileDiffBase, FileOpenSource, FileViewMode } from './tab.types'
import type { AgentGitStatus, AgentProvider, AgentStatus, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import type { HLC, UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import { createEffect, createMemo } from 'solid-js'
import { createStore, produce, unwrap } from 'solid-js/store'
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
   * Local-only monotonic activation counter. Higher = more recently activated.
   * Never persisted and never in the projection; it orders the MRU views within
   * a single client session.
   */
  mru?: number
  workingDir?: string
  createdAt?: string
  // ---- git (worker-reported, mirrored onto every tab kind) ----
  gitBranch?: string
  gitOriginUrl?: string
  gitToplevel?: string
  gitIsWorktree?: boolean
  gitDiffAdded?: number
  gitDiffDeleted?: number
  gitDiffUntracked?: number
}

export interface AgentMeta {
  agentProvider?: AgentProvider
  agentStatus?: AgentStatus
  agentSessionId?: string
  optionValues?: Record<string, string>
  optionGroups?: AvailableOptionGroup[]
  agentGitStatus?: AgentGitStatus
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
 * The tab ids a metadata sweep must treat as live.
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
 * A tombstone answers the FIRST question, so it must be excluded here.
 * `applyTombstoneTab` REPLACES the record with a tombstoned one rather than
 * deleting the map key, so `Object.keys(state.tabs)` alone still names every
 * tab the account has ever opened -- which made this sweep a no-op and let
 * closed terminals' `screen` buffers accumulate for the life of the page, the
 * exact leak its caller exists to prevent.
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

export function createTabMetadataStore() {
  const [state, setState] = createStore<{ byTabId: Record<string, TabMetadata> }>({ byTabId: {} })

  let mruCounter = 0

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
    },

    /**
     * Drop metadata for every tab id not in `live`.
     *
     * Pass {@link liveTabIds}, NOT a set built from the projection's
     * `ownedTabs` — read that function's doc before changing this call. A tab
     * whose tile chain is momentarily unresolvable leaves the projection while
     * remaining alive, and sweeping on that signal deletes its title, git
     * badges and terminal scrollback for good.
     *
     * "The worker has answered for this tab" is a field on the row swept here
     * (`hydrated`), deliberately NOT a companion cache keyed by tab id. Such a
     * cache would be swept on a different schedule than the row it describes,
     * so it could still say "already answered" for a row that is gone -- and
     * the FILE hydrator, reading that, would never ask again, stranding the tab
     * with no path for the life of the page.
     */
    retainOnly(live: Set<string>) {
      setState(produce((s) => {
        for (const tabId of Object.keys(s.byTabId)) {
          if (!live.has(tabId))
            delete s.byTabId[tabId]
        }
      }))
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
     */
    touchMru(tabId: string): number {
      if (state.byTabId[tabId]?.mru === mruCounter)
        return mruCounter
      mruCounter += 1
      this.patch(tabId, { mru: mruCounter })
      return mruCounter
    },
  }
}

export type TabMetadataStore = ReturnType<typeof createTabMetadataStore>

/**
 * Drop metadata rows for tabs the CRDT no longer has.
 *
 * Lives here rather than in the shell because both halves it coordinates do —
 * {@link liveTabIds} and `retainOnly` — and the rule binding them ("pass
 * `liveTabIds`, NOT the projection's `ownedTabs`") was previously written out
 * three times, twice verbatim, because the caller sat in a different module.
 *
 * Nothing else drops these rows: a close is a tombstone, not a store call, and
 * terminal `screen` buffers are the largest thing in here, so without the sweep
 * a long session accumulates the scrollback of every terminal it ever opened.
 *
 * Keyed on the raw TabRecord set, NOT on `projection.ownedTabs`. A tab whose
 * tile chain is momentarily unresolvable — mid-batch during a close's
 * undo-split, or between a Batch frame and the EntityMaterialized frame that
 * creates its new tile — leaves `ownedTabs` while remaining perfectly alive.
 * Sweeping on that signal deletes live state: the tab reappears a tick later
 * with its title, git badges and scrollback gone for good. `state.tabs` says
 * whether a tab exists without asking where it sits.
 *
 * Gated on the live tab-id SET, not on the raw tick. `crdtState` is
 * `{ equals: false }` and fires on every CRDT batch — ~60/s while a tile or
 * floating window is being dragged, none of which create or tombstone a tab.
 * Without the set-equality memo each of those frames also paid `retainOnly`'s
 * walk over every metadata row. The `liveTabIds` walk itself still runs per
 * tick (nothing cheaper distinguishes "a tombstone landed" from any other op —
 * a tombstone REPLACES the record rather than removing the key, so neither
 * identity nor key count moves), but it is now the only per-tick cost and it
 * feeds a memo that stays quiet until the set actually changes.
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

  createEffect(() => {
    const live = liveTabIdSet()
    if (live)
      metadata.retainOnly(live)
  })
}
