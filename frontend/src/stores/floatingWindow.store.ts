import type { CloseTileResult, GridAxis, LayoutNodeLocal, SplitOrientation } from './layout.store'
import type { LayoutOwner } from './layoutOwner'
import type { CRDTBridge, LocalTreeCache, Projection, RenderedFloatingWindow } from '~/lib/crdt'
import { createComputed, createEffect, createMemo, createSignal, mapArray, on, onCleanup } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { renderTreeToLocal, withBridge } from '~/lib/crdt'
import { sameKeys } from '~/lib/sameKeys'
import {
  emitAddFloatingWindow,
  emitFwCloseTile,
  emitFwRemoveGrid,
  emitFwReplaceGridWithLeaf,
  emitRemoveFloatingWindow,
  emitUpdateGeometry,
  emitUpdateOpacity,
  emitUpdatePosition,
} from './floatingWindowOps'
import {
  containsTileId,
  findGridById,
  findHeirTileId,
  firstLeafId,
  getAllTileIds,
  hasMultipleLeaves,
} from './layout.store'
import {
  emitMakeGrid,
  emitSplitTile,
  emitUpdateGridRatios,
  emitUpdateRatios,
} from './layoutOps'

export interface FloatingWindowState {
  id: string
  x: number
  y: number
  width: number
  height: number
  opacity: number
  layoutRoot: LayoutNodeLocal
  focusedTileId: string | null
}

export interface FloatingWindowStoreState {
  windows: FloatingWindowState[]
}

/**
 * Floor for a floating window's `width` and `height`, expressed as a fraction
 * of the parent container. Both the store's `updateDragResize` clamp and the
 * chrome resize handle reference this so the window can't be dragged below
 * 5% of the viewport in either dimension.
 *
 * `updateDragResize` is the ONLY entry point that can set a window's size, so
 * clamping there covers every path: `updateDragMove` takes no dimensions at all.
 */
export const MIN_WINDOW_DIMENSION = 0.05

/**
 * Default geometry (fractional of the parent container) for a freshly added
 * floating window — also used as the fallback when a restored proto is
 * missing width/height. Tweaks here propagate to both call sites.
 */
const DEFAULT_FW_GEOMETRY = { x: 0.2, y: 0.15, width: 0.4, height: 0.5 } as const

/**
 * Cascade offset (fraction of the parent container) applied to each new
 * window so back-to-back `addWindow()` calls land at visibly distinct
 * positions instead of stacking exactly on top of each other. Wraps every
 * `CASCADE_WRAP` slots so a long pop-out spree doesn't push windows past
 * the viewport edge — the worst-case bottom edge at slot
 * `CASCADE_WRAP - 1` is `0.15 + 7×0.025 + 0.5 = 0.825`, comfortably under
 * the 1.0 floor. Only applied when the caller passes neither `x` nor `y`;
 * any explicit coordinate (e.g. spawn at drop position) bypasses the
 * cascade entirely.
 */
const CASCADE_STEP = 0.025
const CASCADE_WRAP = 8

/**
 * Structural equality on the per-window tile-set Map produced by
 * `tileSetsByWindow`. Returns true iff both maps carry the same set
 * of window ids and each window's tile id set matches by content.
 * Used as a `createMemo({ equals })` short-circuit so geometry-only
 * CRDT updates (drag/resize) don't notify downstream consumers.
 */
function tileSetMapsEqual(
  a: Map<string, ReadonlySet<string>>,
  b: Map<string, ReadonlySet<string>>,
): boolean {
  if (a.size !== b.size)
    return false
  for (const [winId, av] of a) {
    const bv = b.get(winId)
    if (!bv || av.size !== bv.size)
      return false
    for (const tileId of av) {
      if (!bv.has(tileId))
        return false
    }
  }
  return true
}

/**
 * A window's inner tree in local shape, with the fallback leaf every consumer
 * has to agree on.
 *
 * `renderTreeToLocal` returns null only for an empty `node_id`, which is what a
 * window whose root has not been seeded projects to. Such a window is still
 * LIVE, so it needs a leaf to render and to own -- and a STABLE id for it, since
 * minting one per tick would remount the tile.
 *
 * The window id is the only id available here. Naming the window's `rootNodeId`
 * instead would be unreachable, not merely unused: `buildTreeFromRoot` returns
 * the empty-node_id tree ONLY when `rootNodeId` is itself `""`, so the branch
 * that would read it can only run when there is nothing to read.
 */
function windowLayoutRoot(rendered: RenderedFloatingWindow, cache: LocalTreeCache): LayoutNodeLocal {
  return renderTreeToLocal(rendered.innerTree, cache)
    ?? { type: 'leaf', id: `__empty_${rendered.windowId}` }
}

/**
 * A per-window local overlay: `Map<string, T>` behind a Solid signal, rebuilt
 * copy-on-write on every write that actually changes something.
 *
 * Three of this store's overlays are exactly this shape -- the drag preview,
 * the debounced-opacity gesture, and per-window focus -- and each used to hand-
 * write its own updater, six copies in all, two of them byte-identical apart
 * from the signal name. Each copy also had to re-derive the same two rules by
 * eye: a write must not mutate the previous Map (the projection memo and
 * Solid's `<For>` key on its identity), and a removal that removes nothing must
 * hand back the PREVIOUS reference, because that is the only thing that makes
 * `setSignal` suppress the notify. Every mutator below obeys both.
 *
 * `clearAll` and `retain` exist because a bulk edit expressed as a loop of
 * per-key `clear` calls rebuilds the whole map -- and re-runs every downstream
 * projection -- once per key removed.
 *
 * This store's private primitive despite being exported: the export exists so
 * the co-located suite can pin the two rules above directly, rather than
 * inferring them from three overlays' behaviour. Nothing else imports it.
 */
export interface KeyedOverrides<T> {
  /** `key`'s override, or undefined when it has none. */
  get: (key: string) => T | undefined
  /** Install (or replace) `key`'s override. */
  set: (key: string, value: T) => void
  /** Drop `key`'s override. No rebuild and no notify when it has none. */
  clear: (key: string) => void
  /** Drop every override, in ONE rebuild. */
  clearAll: () => void
  /** Keep only the keys `keep` accepts, in ONE rebuild. */
  retain: (keep: (key: string) => boolean) => void
  /** The whole map, for consumers that iterate it or read many keys per pass. */
  snapshot: () => ReadonlyMap<string, T>
}

export function createKeyedOverrides<T>(): KeyedOverrides<T> {
  const [overrides, setOverrides] = createSignal<ReadonlyMap<string, T>>(new Map())
  return {
    get: key => overrides().get(key),
    set: (key, value) => setOverrides((prev) => {
      const next = new Map(prev)
      next.set(key, value)
      return next
    }),
    clear: key => setOverrides((prev) => {
      if (!prev.has(key))
        return prev
      const next = new Map(prev)
      next.delete(key)
      return next
    }),
    clearAll: () => setOverrides(prev => prev.size === 0 ? prev : new Map()),
    retain: keep => setOverrides((prev) => {
      // Copy lazily, on the first key that actually goes: a sweep that finds
      // nothing stale must hand back `prev` so no consumer re-derives.
      let next: Map<string, T> | undefined
      for (const key of prev.keys()) {
        if (keep(key))
          continue
        next ??= new Map(prev)
        next.delete(key)
      }
      return next ?? prev
    }),
    snapshot: overrides,
  }
}

/**
 * createFloatingWindowStore — projection-driven floating-window store.
 * Window list + inner trees derive from `project(bridge.speculativeState())
 * [bridge.workspaceId()].floatingWindows`. Z-order is local-only (a
 * derived array of window ids that the store owns), and per-window
 * focus stays in a local Map<windowId, tileId>.
 *
 * Mutators emit op batches via the bridge: addWindow / removeWindow /
 * geometry / opacity / inner-tree (split / makeGrid / closeTile / etc.).
 * The store doesn't hold a parallel imperative `windows: []` array
 * — it derives from the projection and overlays the local z-order.
 */
export interface CreateFloatingWindowStoreOpts {
  /** Which workspace is on screen; its windows are the ones RENDERED. */
  getWorkspaceId: () => string | null | undefined
  /**
   * The shared user-wide projection, mirroring `createLayoutStore`.
   *
   * Taken as an opt rather than re-derived through the global bridge because
   * `project()` ALREADY produces every floating window's geometry and inner
   * tree. Deriving them again here meant walking the whole CRDT state twice more
   * per tick -- and, worse, being a second implementation of the projection's
   * floating-window rules, which had already drifted: `project()` drops a window
   * whose workspace record is gone, and the local walk kept it.
   */
  projection: () => Projection | null
}

export function createFloatingWindowStore(opts: CreateFloatingWindowStoreOpts) {
  // Z-order: the array of window ids in render order (last = topmost).
  // Local-only — z-order isn't a CRDT register.
  const [zOrder, setZOrder] = createSignal<string[]>([])
  // Per-window local focus state. `null` is a RECORDED "no focused tile",
  // distinct from an absent entry (which falls back to the window's first leaf).
  const focusByWindow = createKeyedOverrides<string | null>()

  // Local-only geometry override for the window currently being dragged /
  // resized. During a drag the container writes the live preview here so the
  // window renders at the pointer WITHOUT emitting a per-frame CRDT op (peers
  // see the window jump to its final position on drop, matching the splitter
  // drag's existing design). `commitDragGeometry` emits the single op on drop
  // and clears the override; `cancelDragGeometry` clears it without committing
  // (used when the container unmounts mid-gesture — see its doc).
  //
  // KEYED BY WINDOW, like the opacity override below, and for the same reason.
  // Nothing serializes drags ACROSS windows: each FloatingWindowContainer
  // constructs its OWN `useWindowPointerDrag` controller, so that controller's
  // `cancel()` only supersedes a gesture on the SAME window, and the hook
  // deliberately uses document-level listeners rather than `setPointerCapture`.
  // Two pointers on two title bars (touch, or a second button) therefore run two
  // live drags. With a single slot the second `updateDrag*` call replaced the
  // first, so the first window snapped back to CRDT geometry mid-gesture and its
  // `commitDragGeometry` returned early on the id mismatch -- discarding the
  // whole drag with no op and no error path.
  //
  // This override still earns its keep alongside `~/lib/crdt/coalesce`, which
  // merges same-register writes inside the submitter's 16ms flush window: that
  // gives ~1 op per FLUSH, while the override gives 1 op per GESTURE and, more
  // importantly, 0 ops during it. Coalescing is the floor for any register that
  // has no override; this is the ceiling for one that does.
  // A REAL discriminated union: a move override has no width/height at all.
  //
  // The two gestures commit DIFFERENT register sets. A resize legitimately
  // writes all four; a MOVE must write x/y only. When a move override still
  // carried width/height, those were the values captured at pointer-down, so a
  // peer that resized the window mid-drag had its resize overwritten by stale
  // numbers under a newer HLC -- reverted on every client, permanently. Making
  // the fields absent rather than present-but-ignored means the projection and
  // the commit path cannot read them by mistake; `override.width` on a move is
  // a type error instead of a stale number.
  type DragOverride
    = | { kind: 'move', x: number, y: number }
      | { kind: 'resize', x: number, y: number, width: number, height: number }
  const dragOverrides = createKeyedOverrides<DragOverride>()

  // Local-only opacity overrides, keyed BY WINDOW. The wheel fires dozens of
  // times per scroll gesture; writing each tick to the override gives instant
  // visual feedback, and a trailing debounce emits ONE op when scrolling pauses.
  // Without this, a single scroll emitted one SetFloatingWindowRegister(opacity)
  // op per wheel tick that changed the clamped value.
  //
  // A MAP, like the drag override above, and for the same reason: nothing
  // serializes WHEEL events either. They need no pointer capture, every visible
  // window binds its own title-bar listener, and scrolling window A and then
  // window B within the debounce window used to clear A's timer and replace A's
  // override -- so A's op was never emitted and its rendered opacity silently
  // snapped back. The whole gesture was lost, with no error path.
  //
  // ONE record per window holding BOTH the pending value and its timer. They are
  // written and cleared together at every site, so splitting them across two
  // maps keyed by the same id made two broken states representable: an override
  // with no timer (never flushed -- the opacity sticks at a value no op will
  // ever carry) and a timer with no override (fires and emits nothing). Pairing
  // them means neither can be constructed.
  //
  // `base` is the CRDT-side opacity captured when the gesture STARTED, and it is
  // what the flush compares against. Re-reading the projection at flush time was
  // wrong on both teardown paths: Solid's cleanNode disposes `owned` (including
  // the createComputed that maintains storeState.list) BEFORE running the
  // owner's cleanups, so the disposal flush saw the override value and its
  // same-value guard swallowed the op; and on a workspace switch the <For> item
  // cleanup runs after the window already left the active-workspace slice, so
  // findWindow returned null and the `!w` guard dropped it. Capturing the
  // baseline up front makes the flush independent of the projection's lifetime.
  interface OpacityGesture { value: number, base: number | undefined, timer: ReturnType<typeof setTimeout> }
  const opacityGestures = createKeyedOverrides<OpacityGesture>()
  const OPACITY_FLUSH_DELAY_MS = 600

  /**
   * Emit `id`'s pending debounced-opacity op NOW and clear its timer, or every
   * window's when `id` is omitted. Idempotent if nothing is pending. Declared
   * as a free function (rather than only as a store method) so the disposal
   * hook and the `pagehide` listener below can call it — both are registered
   * before the store object exists.
   *
   * PASS THE ID from a per-window teardown. Unscoped, a container unmounting
   * for its own reasons flushed whichever window happened to have a gesture in
   * flight -- so closing window B mid-scroll of window A committed A's
   * half-finished value and split A's single gesture into two ops. The store's
   * disposal hook and the unload listener are the legitimate unscoped callers:
   * there, every window really is going away.
   */
  function flushAllOpacity(): void {
    flushGestures(undefined)
  }

  function flushOpacity(id: string): void {
    flushGestures(id)
  }

  function flushGestures(id: string | undefined): void {
    const gestures = opacityGestures.snapshot()
    // Take the victims and drop their entries in ONE map rebuild, BEFORE
    // emitting. Clearing per victim inside the emit loop rebuilt the whole map
    // -- re-running every projection consumer -- once per window flushed.
    const victims: [string, OpacityGesture][] = []
    if (id === undefined) {
      victims.push(...gestures)
      opacityGestures.clearAll()
    }
    else {
      const gesture = gestures.get(id)
      if (!gesture)
        return
      victims.push([id, gesture])
      opacityGestures.clear(id)
    }
    for (const [wid, gesture] of victims) {
      // A no-op when the timer already fired (this IS its callback).
      clearTimeout(gesture.timer)
      // Compare against the gesture's captured baseline, NOT a fresh projection
      // read: this runs from teardown paths where the projection is already
      // torn down or no longer carries this window. See OpacityGesture.base.
      if (gesture.value === gesture.base)
        continue
      withBridge(bridge => emitUpdateOpacity(bridge, wid, gesture.value), undefined as void)
    }
  }

  // Disposal hook. Two jobs, both of which were previously mis-handled:
  //
  //  - FLUSH the pending debounced-opacity op. This block used to only
  //    clearTimeout, silently DROPPING the op despite a comment claiming it
  //    flushed. In practice FloatingWindowContainer's own cleanup runs first
  //    (Solid disposes the <For> item roots before the owner's cleanups), so
  //    the op survived — but the store must not depend on that ordering.
  //  - CLEAR any drag override. `useWindowPointerDrag`'s cleanup calls stop(),
  //    which deliberately fires no onUp, so a container torn down mid-gesture
  //    left that window's override set forever. The projection reads overrides
  //    by id, so that window rendered frozen at never-committed geometry AND
  //    masked incoming CRDT geometry until some later drag on it replaced the
  //    entry. The whole store is going away here, so every entry goes.
  onCleanup(() => {
    flushAllOpacity()
    dragOverrides.clearAll()
  })

  // A browser unload runs NO Solid cleanup, so neither hook above fires on a
  // refresh or a tab close. Before the debounce existed the op was emitted
  // synchronously and only the submitter's 16ms window was at risk; now a scrub
  // finished within OPACITY_FLUSH_DELAY_MS of Cmd+R is lost outright -- the op is
  // never created, so the checkpoint/op-log cannot recover it either.
  //
  // `pagehide` rather than `beforeunload`: it fires for bfcache navigations too,
  // and does not risk the unload-blocking prompt.
  //
  // WHAT THIS DOES AND DOES NOT CLOSE. The flush CREATES the op and hands it to
  // `useOpsSubmitter.enqueue`, which pushes it on the queue and arms a 16 ms
  // `setTimeout`. On a bfcache navigation that timer survives (the page is
  // frozen, then resumed) and the op is sent on restore. On a REAL unload --
  // Cmd+R, tab close, the case this listener was written for -- the timer never
  // fires and the op is still lost, just one step later than before.
  //
  // Getting the rest requires sending from the unload handler itself, which
  // means a transport that survives it (`fetch(..., {keepalive: true})` or
  // `navigator.sendBeacon`); `useOpsSubmitter` already exports `flush` for this
  // and has no production caller. That belongs at the submission layer, not
  // here: this listener can only rescue the ONE gesture that registered it,
  // while the drag override (discarded outright by `cancelDragGeometry`) and
  // every future debounced gesture have the same hole.
  // https://github.com/leapmux/leapmux/issues/359
  if (typeof window !== 'undefined') {
    const flushOnUnload = (): void => {
      // Two steps, in this order, in ONE handler. The first CREATES the op from
      // the in-flight debounced gesture; the second SENDS it. Creating it alone
      // was never enough -- `enqueue` only queues and arms a ~16 ms timer, and
      // on a real unload that timer never fires -- and splitting the send into
      // its own listener would make the outcome depend on registration order.
      flushAllOpacity()
      withBridge(bridge => bridge.flushNow(), undefined as void)
    }
    window.addEventListener('pagehide', flushOnUnload)
    onCleanup(() => window.removeEventListener('pagehide', flushOnUnload))
  }

  /**
   * This store's OWN `RenderTree` -> `LayoutNodeLocal` conversions, kept out of
   * `renderTreeToLocal`'s shared map.
   *
   * The `reconcile` below updates a matched node's fields IN PLACE, so whatever
   * it is handed is writable state, not a value. The shared map's node would be
   * scribbled on for every other consumer; a fresh conversion per tick would
   * throw away the identity `reconcile` and `tileSetsByWindow` both key on. A
   * private cache keeps both -- identity across ticks that leave a window's inner
   * tree alone, and a mutation surface no other module can observe.
   */
  const windowTrees: LocalTreeCache = new WeakMap()

  // Raw projection memo: produces a fresh ordered FloatingWindowState
  // array every time the CRDT speculative state, zOrder, or focusByWindow
  // changes. The values here are throw-away refs — they feed into the
  // store reconcile below so consumers see stable refs with granular
  // field updates.
  const rawProjection = createMemo<FloatingWindowState[]>(() => {
    const proj = opts.projection()
    const wsId = opts.getWorkspaceId()
    if (!proj || !wsId)
      return []
    const result: FloatingWindowState[] = []
    const focusMap = focusByWindow.snapshot()
    // Local drag/resize preview override: when a drag is in flight, render the
    // window at the live pointer geometry instead of the CRDT-projected one, so
    // the drag feels responsive without emitting a per-frame op. Peers see the
    // final geometry land in one op on drop (matching the splitter drag).
    const overrides = dragOverrides.snapshot()
    // Local opacity override: title-bar scrolling writes the live value here so
    // the window's opacity tracks the wheel instantly; one op is emitted when
    // scrolling pauses (trailing debounce).
    const opGestures = opacityGestures.snapshot()
    // Straight off the shared projection: `project()` already resolved every
    // window's geometry and inner tree. It used to be re-derived here from raw
    // state, which walked the whole state again per tick AND was a second
    // implementation of the projection's floating-window rules.
    for (const rendered of proj.workspaces.get(wsId)?.floatingWindows ?? []) {
      const layoutRoot = windowLayoutRoot(rendered, windowTrees)
      const fallbackFocus = firstLeafId(layoutRoot) ?? null
      const localFocus = focusMap.get(rendered.windowId)
      const override = overrides.get(rendered.windowId)
      // A MOVE override owns x/y only -- exactly the registers commitDragGeometry
      // emits for it -- so a peer's resize landing mid-drag stays visible
      // instead of being masked by the pointer-down size until the drop. The
      // union makes that structural: a move override has no width/height to read.
      const size = override?.kind === 'resize' ? override : undefined
      const opOverride = opGestures.get(rendered.windowId)?.value
      result.push({
        id: rendered.windowId,
        x: override ? override.x : rendered.x,
        y: override ? override.y : rendered.y,
        width: size ? size.width : rendered.width,
        height: size ? size.height : rendered.height,
        opacity: opOverride ?? rendered.opacity,
        layoutRoot,
        focusedTileId: localFocus !== undefined ? localFocus : fallbackFocus,
      })
    }
    // Apply local z-order overlay so explicit bringToFront calls land
    // their target on top. Windows not present in zOrder land in
    // descending CRDT-id order at the bottom (deterministic but
    // arbitrary; the user only sees this on initial load before any
    // bringToFront fires).
    const order = zOrder()
    const seen = new Set<string>()
    const ordered: FloatingWindowState[] = []
    const byId = new Map(result.map(w => [w.id, w]))
    for (const id of order) {
      const w = byId.get(id)
      if (w) {
        ordered.push(w)
        seen.add(id)
      }
    }
    for (const w of result) {
      if (!seen.has(w.id))
        ordered.push(w)
    }
    return ordered
  }, [])

  // Store-backed projection. Solid's `<For>` keys by REFERENCE identity
  // and the CRDT bridge bumps `pendingVersion` on every mutation — and the
  // drag/resize override signal (`dragOverrides`) flips on every pointermove
  // during a floating-window drag. If we returned `rawProjection` directly to
  // `<For>`, every drag frame would produce a new array of fresh objects and
  // `<For>` would unmount + remount every container (and its inner
  // TilingLayout / TabBar / Terminal subtree). The result was visibly sluggish
  // drag and resize.
  //
  // `reconcile` keyed by `id` mutates the existing store entries in place when
  // ids match — same array element ref across frames, but individual fields
  // update granularly. It recurses into any nested object whose `id` matches
  // too, so a `layoutRoot` the projection left alone is compared and left
  // untouched, and downstream memos keyed on it (e.g. `tileSetsByWindow`) don't
  // invalidate on geometry-only ticks. The tests in this file's `projection ref
  // stability` block pin both layers (per-window Set ref-stable across
  // drag/resize scrubs, Map ref-stable across geometry-only batches,
  // invalidation on real splits).
  //
  // NOTE that "in place" is literal: on a tick where the inner tree DID change,
  // reconcile writes the new fields into the node it retained rather than
  // swapping the reference. The nodes it writes come from this store's own
  // `windowTrees` cache, so those writes cannot reach any other consumer of
  // `renderTreeToLocal`.
  const [storeState, setStoreState] = createStore<{ list: FloatingWindowState[] }>({ list: [] })
  // `createComputed` (not `createEffect` or `createRenderEffect`) is
  // the only primitive that propagates synchronously inside the same
  // signal-update transaction in Solid's current scheduler. Mutators
  // like `addWindow` enqueue a CRDT batch and then immediately expect
  // `state.windows` to reflect the new window; tests and callers rely
  // on synchronous-after-call semantics that the prior memo-based
  // implementation provided by being lazy-on-read.
  //
  // `createEffect` runs in the next microtask (Solid's user-effect
  // phase); `createRenderEffect` likewise defers in non-Solid-component
  // contexts (verified empirically with a Solid `setSignal` →
  // `createRenderEffect` callback count, which stayed stale until a
  // microtask flushed). `createComputed` is the documented escape
  // hatch for "I need this side effect to run during the current
  // propagation pass" — it joins the same queue as memos and runs
  // before user effects.
  createComputed(() => {
    setStoreState('list', reconcile(rawProjection(), { key: 'id' }))
  })

  // Internal accessor used by every helper memo / mutator that needs
  // the current window list. Reads from the reconcile-backed store so
  // consumers see stable per-id refs.
  const projectedWindows = (): FloatingWindowState[] => storeState.list

  const stateView: FloatingWindowStoreState = {
    get windows(): FloatingWindowState[] {
      return storeState.list
    },
  } as FloatingWindowStoreState

  // Per-window leaf-id sets, computed in a single pass over the
  // reconcile-backed `projectedWindows()`. Each window entry's
  // `layoutRoot` ref survives a CRDT tick that didn't change its inner
  // tree, so this memo only re-runs (and only emits a new Map) when a
  // window's tree actually mutates. That preservation is the `key: 'id'`
  // match above -- Solid's `applyState` returns early on an identical
  // ref, and otherwise recurses into the id-matched entry and writes
  // fields into the node it retained instead of replacing it. (It is
  // NOT the `merge` option, which reaches only the array branch and was
  // dropped once that was checked against Solid's source.) The
  // structural `equals` check below is a defense-in-depth backstop for
  // the (rare) case where the upstream diff produces a same-content
  // fresh ref.
  const tileSetsByWindow = createMemo<Map<string, ReadonlySet<string>>>(
    () => {
      const out = new Map<string, ReadonlySet<string>>()
      for (const w of projectedWindows())
        out.set(w.id, new Set(getAllTileIds(w.layoutRoot)))
      return out
    },
    new Map(),
    { equals: tileSetMapsEqual },
  )

  const allFloatingTileIdsMemo = createMemo(() => {
    const out: string[] = []
    for (const set of tileSetsByWindow().values()) {
      for (const id of set)
        out.push(id)
    }
    return out
  })

  /**
   * Tile -> window and workspace -> tiles, across EVERY workspace.
   *
   * `projectedWindows()` is deliberately the ACTIVE workspace's slice: it
   * carries z-order and per-window focus, which only mean something for what is
   * on screen. But three questions are not about rendering and must be
   * answerable for any workspace:
   *
   *   - `getWindowForTile` — `tileLifecycle.focusTile` needs it for a
   *     cross-workspace sidebar click, and `removeEmptyFloatingWindow` for the
   *     SOURCE tile of a tab dragged out of a background workspace. Both
   *     silently answered "not a floating tile" before, so the window's inner
   *     focus was never recorded and an emptied background window was never
   *     disposed.
   *   - `getAllTileIdsFor(wsId)` — persistence and selection cleanup need a
   *     workspace's floating tiles whether or not it is on screen.
   *   - `trees` — the MUTATORS those two feed. Answering "which window owns this
   *     tile" account-wide is useless if `setFocusedTile` and `removeIfEmpty`
   *     then resolve the id back through the rendered slice: they would find
   *     nothing and silently no-op, which is exactly what they did. They resolve
   *     through this map instead, so the escape is not undone one call later.
   *     `liveWindowIds` is derived from here for the same reason — a GC keyed on
   *     the rendered slice would delete a background window's focus entry on the
   *     next membership change.
   *
   * Built from the raw projection rather than the rendered slice, so it does
   * not inherit the active-workspace filter it exists to escape. Geometry is
   * ignored, so a drag does not invalidate it.
   */
  const floatingTileIndex = createMemo<{
    tileToWindow: Map<string, string>
    tilesByWorkspace: Map<string, ReadonlySet<string>>
    trees: Map<string, LayoutNodeLocal>
  }>(() => {
    const tileToWindow = new Map<string, string>()
    const tilesByWorkspace = new Map<string, Set<string>>()
    const trees = new Map<string, LayoutNodeLocal>()
    const proj = opts.projection()
    if (!proj)
      return { tileToWindow, tilesByWorkspace, trees }
    // Every workspace's windows, from the same projection the rendered slice
    // above reads -- so "which window owns this tile" and "what is on screen"
    // can no longer answer from two different walks of the state.
    for (const [wsId, ws] of proj.workspaces) {
      for (const rendered of ws.floatingWindows) {
        // A window with no resolvable inner tree is still LIVE here. Dropping it
        // would make the GC below treat it as tombstoned and evict its focus
        // entry.
        const local = windowLayoutRoot(rendered, windowTrees)
        trees.set(rendered.windowId, local)
        let bucket = tilesByWorkspace.get(wsId)
        if (!bucket) {
          bucket = new Set<string>()
          tilesByWorkspace.set(wsId, bucket)
        }
        for (const tileId of getAllTileIds(local)) {
          tileToWindow.set(tileId, rendered.windowId)
          bucket.add(tileId)
        }
      }
    }
    return { tileToWindow, tilesByWorkspace, trees }
  })

  /**
   * A live window's inner tree, in ANY workspace; null when no such window
   * exists. The rendered slice is preferred when it has the window so an
   * in-flight local edit is seen at the same instant the on-screen tree is.
   */
  function layoutRootForWindow(windowId: string): LayoutNodeLocal | null {
    return findWindow(windowId)?.layoutRoot ?? floatingTileIndex().trees.get(windowId) ?? null
  }

  // Live window-id set, memoized with a structural-equals comparator
  // so it only changes when the set membership actually changes (a
  // peer create or tombstone) — NOT on every geometry update / drag /
  // resize tick. Drives the GC effect below so the GC only runs when
  // the GC question can actually have a new answer.
  //
  // Spans EVERY workspace, via `floatingTileIndex` — which is also why it is
  // declared here rather than beside the other projection memos above: a memo
  // body runs once at creation, so it has to follow what it reads. Keyed on
  // the rendered slice instead, it would call a background workspace's windows
  // dead and evict the focus entries `setFocusedTile` records for them,
  // deleting on the next membership change exactly what the cross-workspace
  // path just wrote.
  const liveWindowIds = createMemo<ReadonlySet<string>>(
    () => new Set(floatingTileIndex().trees.keys()),
    new Set(),
    { equals: sameKeys },
  )

  // Garbage-collect z-order and focus entries whose windows have been
  // tombstoned (locally or by a peer). Without this sweep, a peer-
  // tombstoned window's id leaks in `zOrder` / `focusByWindow` for the
  // lifetime of the session — the projection memo silently drops the
  // entry at read time, but the underlying array/map grows unboundedly
  // across churning peer creates/removes.
  //
  // Reactivity scope: explicit `on(liveWindowIds)` so the effect only
  // runs when the set of live windows actually changes. Without it,
  // every drag/resize tick that rewrites `projectedWindows` invoked
  // this body, walked zOrder/focusByWindow, and exited at the early-
  // return guards. The guards are still here defensively (a window
  // can disappear without a peer event firing the GC), but the
  // common-case "geometry-only update" no longer pays the walk cost.
  createEffect(
    on(liveWindowIds, (live) => {
      const current = zOrder()
      if (current.some(id => !live.has(id)))
        setZOrder(current.filter(id => live.has(id)))
      // `retain` is itself the "nothing stale" short-circuit: it hands back the
      // previous Map untouched when every key survives, so a sweep that finds
      // nothing notifies nobody.
      focusByWindow.retain(id => live.has(id))
    }),
  )

  const windowIdToIndex = createMemo(() => {
    const m = new Map<string, number>()
    const wins = projectedWindows()
    for (let i = 0; i < wins.length; i++)
      m.set(wins[i].id, i)
    return m
  })

  function findWindowIndex(id: string): number {
    return windowIdToIndex().get(id) ?? -1
  }

  function findWindow(id: string): FloatingWindowState | null {
    const idx = findWindowIndex(id)
    return idx < 0 ? null : projectedWindows()[idx]
  }

  // disposeWindowLocally is the teardown sequence shared by every
  // path that removes a window (explicit remove, last-tile close,
  // empty-after-pop-back). The CRDT op tells the projection to drop
  // the record; the local z-order and per-window focus map are
  // purely client-side overlays, so they need explicit cleanup or
  // we leak stale entries.
  function disposeWindowLocally(bridge: CRDTBridge, id: string) {
    emitRemoveFloatingWindow(bridge, id)
    // Short-circuit when `id` isn't in the local overlays — Solid's
    // `setSignal` only suppresses the notify when the updater returns
    // the same reference. The hot path here (removeIfEmpty on a
    // window the user never brought to front or focused) hits this
    // common case; without the guards, every empty-window cleanup
    // would re-trigger `projectedWindows` even when nothing changed.
    // (`focusByWindow.clear` carries its own; only zOrder needs one here.)
    setZOrder(z => z.includes(id) ? z.filter(x => x !== id) : z)
    focusByWindow.clear(id)
  }

  const splitTile = (windowId: string, tileId: string, direction: SplitOrientation): string | null =>
    withBridge((bridge) => {
      const win = findWindow(windowId)
      if (!win || !containsTileId(win.layoutRoot, tileId))
        return null
      const result = emitSplitTile(bridge, tileId, direction)
      return result?.childB ?? null
    }, null)

  const makeGrid = (windowId: string, tileId: string, rows: number, cols: number): { gridId: string, cellTileIds: string[] } | null =>
    withBridge((bridge) => {
      const win = findWindow(windowId)
      if (!win || !containsTileId(win.layoutRoot, tileId))
        return null
      return emitMakeGrid(bridge, tileId, rows, cols)
    }, null)

  const removeGrid = (windowId: string, gridId: string): void => {
    withBridge((bridge) => {
      if (!findWindow(windowId))
        return
      emitFwRemoveGrid(bridge, gridId)
    }, undefined as void)
  }

  const replaceGridWithLeaf = (windowId: string, gridId: string): string | null =>
    withBridge((bridge) => {
      if (!findWindow(windowId))
        return null
      return emitFwReplaceGridWithLeaf(bridge, gridId)
    }, null)

  function buildOwner(windowId: string): LayoutOwner {
    return {
      collectTileIdsInGrid: (gridId) => {
        const win = findWindow(windowId)
        if (!win)
          return []
        const grid = findGridById(win.layoutRoot, gridId)
        return grid ? getAllTileIds(grid) : []
      },
      findHeirTile: (tileId) => {
        const win = findWindow(windowId)
        return win ? findHeirTileId(win.layoutRoot, tileId) : null
      },
      firstLeafId: () => {
        const win = findWindow(windowId)
        return win ? firstLeafId(win.layoutRoot) ?? null : null
      },
      splitTile: (tileId, direction) => { splitTile(windowId, tileId, direction) },
      makeGrid: (tileId, rows, cols) => { makeGrid(windowId, tileId, rows, cols) },
      removeGrid: gridId => removeGrid(windowId, gridId),
      replaceGridWithLeaf: gridId => replaceGridWithLeaf(windowId, gridId),
    }
  }

  const ownerEntries = mapArray(
    () => projectedWindows(),
    w => [w.id, buildOwner(w.id)] as const,
  )
  const ownersById = createMemo(() => new Map(ownerEntries()))

  return {
    state: stateView,

    /**
     * Append a new window. Submits the create batch (root node + window
     * registers) and returns the freshly-minted ids. Z-order is updated
     * locally to put the new window on top.
     *
     * Returns null when the bridge isn't wired (pre-bootstrap or
     * non-bootstrap test harness) — callers MUST guard, otherwise a
     * tab move into the would-be window silently routes nowhere.
     */
    addWindow(opts?: { x?: number, y?: number, width?: number, height?: number }): { windowId: string, tileId: string } | null {
      return withBridge<{ windowId: string, tileId: string } | null>((bridge) => {
        // Cascade only when no explicit coordinate is given.
        const slot = opts?.x === undefined && opts?.y === undefined
          ? projectedWindows().length % CASCADE_WRAP
          : 0
        const x = opts?.x ?? (DEFAULT_FW_GEOMETRY.x + slot * CASCADE_STEP)
        const y = opts?.y ?? (DEFAULT_FW_GEOMETRY.y + slot * CASCADE_STEP)
        const width = opts?.width ?? DEFAULT_FW_GEOMETRY.width
        const height = opts?.height ?? DEFAULT_FW_GEOMETRY.height
        const result = emitAddFloatingWindow(bridge, { x, y, width, height, opacity: 1 })
        if (!result)
          return null
        setZOrder((z) => {
          const without = z.filter(id => id !== result.windowId)
          without.push(result.windowId)
          return without
        })
        return { windowId: result.windowId, tileId: result.rootTileId }
      }, null)
    },

    removeWindow(id: string) {
      withBridge((bridge) => {
        if (findWindowIndex(id) < 0)
          return
        disposeWindowLocally(bridge, id)
      }, undefined as void)
    },

    /**
     * Write the opacity to the LOCAL override immediately (instant visual
     * feedback) and arm a trailing debounce that emits ONE CRDT op when the
     * wheel pauses for OPACITY_FLUSH_DELAY_MS. Each tick re-arms the timer, so
     * a continuous scroll collapses to a single op regardless of how many
     * wheel events it produced. Returns whether the clamped value actually
     * moved — compared against the EFFECTIVE opacity (this window's pending
     * override if a scroll is in flight, else the CRDT-projected value), so the
     * short-circuit stays accurate mid-gesture and a wheel parked at the clamp
     * floor stops re-arming the timer. Used by the title-bar wheel handler —
     * without this, a single scroll emitted one op per wheel tick.
     */
    updateOpacityDebounced(id: string, opacity: number): boolean {
      const clamped = Math.max(0.2, Math.min(1, opacity))
      // Read the EFFECTIVE opacity (override if a scroll is in flight, else
      // CRDT-projected) so the same-value short-circuit is accurate mid-gesture.
      const current = opacityGestures.get(id)?.value ?? findWindow(id)?.opacity
      if (current === undefined || current === clamped)
        return false
      // Re-arm only THIS window's timer; another window's pending gesture is
      // untouched.
      const existing = opacityGestures.get(id)
      if (existing !== undefined)
        clearTimeout(existing.timer)
      // Keep the ORIGINAL CRDT baseline across re-arms: a continuing gesture
      // must still be compared against where it started, not against its own
      // previous intermediate value (which would make every flush look like a
      // change).
      const base = existing !== undefined ? existing.base : findWindow(id)?.opacity
      // The debounce fires the SAME scoped `flushOpacity` the per-window teardown
      // store method call, rather than re-implementing its body here -- a
      // second copy would drift from its same-value short-circuit. The third
      // setTimeout argument is passed through as `id`, so the timer flushes
      // THIS window only.
      const timer = setTimeout(flushOpacity, OPACITY_FLUSH_DELAY_MS, id)
      opacityGestures.set(id, { value: clamped, base, timer })
      return true
    },

    /**
     * Flush `id`'s pending debounced-opacity op immediately, or every window's
     * when `id` is omitted. Used on teardown so a scroll in flight when the
     * window unmounts isn't lost. See the free function for the per-window
     * scoping rule callers have to honour.
     */
    flushOpacity,
    flushAllOpacity,

    /**
     * Begin / continue a drag-MOVE: write the live position into `id`'s local
     * override WITHOUT emitting a CRDT op. The projection returns this position
     * for the window so the container renders at the pointer. One op is emitted
     * on drop via `commitDragGeometry`.
     *
     * Takes NO width/height, which is the point: a move commits x/y only, and a
     * size it never receives is a size it can never write over a peer's
     * concurrent resize. Overrides are per window, so dragging a second window
     * does not disturb this one's gesture.
     */
    updateDragMove(id: string, x: number, y: number) {
      dragOverrides.set(id, { kind: 'move', x, y })
    },

    /**
     * Begin / continue a drag-RESIZE. Same override-then-commit contract as
     * `updateDragMove`, but this gesture owns all four registers, so the
     * override carries the size and the projection renders it.
     *
     * Width/height are clamped to MIN_WINDOW_DIMENSION here, at the only entry
     * point that can set them.
     */
    updateDragResize(id: string, x: number, y: number, width: number, height: number) {
      dragOverrides.set(id, {
        kind: 'resize',
        x,
        y,
        width: Math.max(width, MIN_WINDOW_DIMENSION),
        height: Math.max(height, MIN_WINDOW_DIMENSION),
      })
    },

    /**
     * End a drag by emitting ONE CRDT op for the final geometry and clearing
     * the local override. Idempotent if no drag is in flight (a pointercancel
     * that already cleared it, or a drop with no movement). Compares against
     * the CRDT-projected geometry (not the override) so a drag that returned
     * to its start emits nothing.
     */
    commitDragGeometry(id: string) {
      const override = dragOverrides.get(id)
      if (!override)
        return
      dragOverrides.clear(id)
      // Read the CRDT-projected geometry to short-circuit a no-op drag.
      withBridge((bridge) => {
        const w = findWindow(id)
        if (!w)
          return
        if (override.kind === 'move') {
          // A move touches only x/y. Emitting width/height too would write the
          // pointer-down snapshot over whatever the register holds NOW, which is
          // a concurrent peer's resize whenever one landed during the drag.
          if (w.x === override.x && w.y === override.y)
            return
          emitUpdatePosition(bridge, id, override.x, override.y)
          return
        }
        if (w.x === override.x && w.y === override.y && w.width === override.width && w.height === override.height)
          return
        // emitUpdateGeometry writes four registers (x, y, width, height) in one
        // batch; opacity is a separate emitter.
        emitUpdateGeometry(bridge, id, override.x, override.y, override.width, override.height)
      }, undefined as void)
    },

    /**
     * Abort an in-flight drag WITHOUT committing: clear the override so the
     * window snaps back to its CRDT-projected geometry.
     *
     * Used when the container UNMOUNTS mid-gesture (a workspace switch or a
     * peer closing the window while the pointer is down). `useWindowPointerDrag`
     * responds to unmount by calling stop(), which deliberately fires no onUp,
     * so nothing else clears the override and the window would otherwise render
     * frozen at geometry that was never committed.
     *
     * NOT used on pointercancel: `windowPointerDrag` routes pointercancel
     * through the same handleUp as pointerup, so an OS-interrupted drag COMMITS
     * its partial motion. (That is a deliberate product choice — matching a
     * clean drop — not an oversight; this doc previously claimed the opposite.)
     */
    cancelDragGeometry(id: string) {
      dragOverrides.clear(id)
    },

    /**
     * Move the window to the end of `state.windows` (topmost). Z-order
     * is purely local — the projection's window list is fed by CRDT
     * record order; the local zOrder array overlays that.
     *
     * Short-circuits when the window is already topmost so that
     * `FloatingWindowContainer.onMouseDown` (which fires on every
     * mouse interaction in the chrome, including tab clicks) doesn't
     * pay for a full projection rebuild on a no-op activation. Solid's
     * `setSignal` skips the notify when the updater returns the same
     * array reference, so callers see zero reactivity downstream.
     */
    bringToFront(id: string) {
      if (!findWindow(id))
        return
      setZOrder((z) => {
        if (z.length > 0 && z[z.length - 1] === id)
          return z
        const without = z.filter(x => x !== id)
        // Pad: if the id wasn't in z previously, the projection's
        // implicit "by record id" order put it somewhere; now it's
        // explicitly on top.
        without.push(id)
        return without
      })
    },

    /**
     * Record which inner tile holds the cursor, for a window in ANY workspace.
     *
     * Resolved through `layoutRootForWindow`, not the rendered slice: the
     * caller (`tileLifecycle.focusTile`) already looked the window up
     * account-wide via `getWindowForTile`, and re-resolving through the
     * active-workspace filter made this a silent no-op for a background
     * workspace — a sidebar click into another workspace's floating window
     * left it fronting `firstLeafId` instead of the tab the user picked.
     */
    setFocusedTile(windowId: string, tileId: string) {
      const root = layoutRootForWindow(windowId)
      if (!root)
        return
      // Compare against the EFFECTIVE focus — the explicit entry when there is
      // one, else the same first-leaf fallback the projection applies — so the
      // no-op check answers the same question for an off-screen window as for
      // an on-screen one.
      const recorded = focusByWindow.get(windowId)
      const effective = recorded !== undefined ? recorded : firstLeafId(root)
      if (effective === tileId)
        return
      focusByWindow.set(windowId, tileId)
    },

    splitTile,

    closeTile(windowId: string, tileId: string): CloseTileResult {
      return withBridge((bridge) => {
        const win = findWindow(windowId)
        if (!win || !containsTileId(win.layoutRoot, tileId))
          return { kind: 'noop' } as CloseTileResult
        // Closing the only tile in a window disposes the whole window.
        if (!hasMultipleLeaves(win.layoutRoot)) {
          const tileIds = new Set(getAllTileIds(win.layoutRoot))
          disposeWindowLocally(bridge, windowId)
          return { kind: 'disposed', tileIds } as CloseTileResult
        }
        emitFwCloseTile(bridge, tileId)
        return { kind: 'changed' } as CloseTileResult
      }, { kind: 'noop' } as CloseTileResult)
    },

    makeGrid,

    removeGrid,

    replaceGridWithLeaf,

    updateGridRatios(windowId: string, gridId: string, axis: GridAxis, ratios: number[]): boolean {
      return withBridge((bridge) => {
        if (!findWindow(windowId))
          return false
        return emitUpdateGridRatios(bridge, gridId, axis, ratios)
      }, false)
    },

    updateRatios(windowId: string, splitId: string, ratios: number[]): boolean {
      return withBridge((bridge) => {
        if (!findWindow(windowId))
          return false
        return emitUpdateRatios(bridge, splitId, ratios)
      }, false)
    },

    /**
     * Which floating window holds `tileId`, in ANY workspace.
     *
     * Not scoped to the active workspace: the callers that ask (focus, and the
     * empty-window sweep) are reached from cross-workspace paths, where a
     * null answer is indistinguishable from "this is a main-tree tile" and
     * silently skips the work.
     */
    getWindowForTile(tileId: string): string | null {
      return floatingTileIndex().tileToWindow.get(tileId) ?? null
    },

    /**
     * Floating tile ids in the ACTIVE workspace (the rendered slice).
     *
     * `readonly`, matching `layoutStore.getAllTileIds`: this is the memo's
     * RETAINED array, handed to every caller until the memo next re-runs, so an
     * in-place sort by one of them reorders the rest. (`getAllTileIdsFor` below
     * builds a fresh array per call and is safe to mutate -- hence the
     * different type.)
     */
    getAllTileIds(): readonly string[] {
      return allFloatingTileIdsMemo()
    },

    /**
     * Floating tile ids owned by `workspaceId`, on screen or not.
     *
     * Persistence and selection cleanup both need a workspace's floating tiles
     * for a workspace that is NOT active — deleting a background workspace, or
     * writing the per-tile selection map for one. `getAllTileIds` answers only
     * for the rendered slice, so those paths silently saw none.
     */
    getAllTileIdsFor(workspaceId: string): string[] {
      const set = floatingTileIndex().tilesByWorkspace.get(workspaceId)
      return set ? [...set] : []
    },

    getWindow(id: string): FloatingWindowState | null {
      return findWindow(id)
    },

    getWindowTileIdSet(windowId: string): ReadonlySet<string> | null {
      return tileSetsByWindow().get(windowId) ?? null
    },

    owner(windowId: string): LayoutOwner {
      return ownersById().get(windowId) ?? buildOwner(windowId)
    },

    /**
     * Remove a single-tile floating window when its tile becomes empty
     * (e.g. the popped-out tab is closed). Multi-tile windows are left
     * alone — the user explicitly built that structure.
     *
     * Resolved through `layoutRootForWindow`, not the rendered slice: the
     * caller (`tileLifecycle.removeEmptyFloatingWindow`) already looked the
     * window up account-wide via `getWindowForTile`, and re-resolving through
     * the active-workspace filter made this a silent no-op for a background
     * workspace — dragging the last tab out of a floating window in a workspace
     * that was not on screen left a phantom empty window behind it, with this
     * as its only collector.
     */
    removeIfEmpty(
      windowId: string,
      getTabsForTile: (tileId: string) => unknown[],
      onRemoved?: (removedTileId: string) => void,
    ): boolean {
      return withBridge<boolean>((bridge) => {
        const root = layoutRootForWindow(windowId)
        if (!root)
          return false
        if (hasMultipleLeaves(root))
          return false
        const removedTileId = firstLeafId(root)
        if (!removedTileId)
          return false
        if (getTabsForTile(removedTileId).length !== 0)
          return false
        disposeWindowLocally(bridge, windowId)
        onRemoved?.(removedTileId)
        return true
      }, false)
    },

  }
}

export type FloatingWindowStoreType = ReturnType<typeof createFloatingWindowStore>
