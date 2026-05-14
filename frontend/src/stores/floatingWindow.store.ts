import type { CloseTileResult, GridAxis, LayoutNodeLocal, SplitOrientation } from './layout.store'
import type { LayoutOwner } from './layoutOwner'
import type { CRDTBridge } from '~/lib/crdt'
import { createComputed, createEffect, createMemo, createSignal, mapArray, on } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { buildChildIndex, floatingWindowToRendered, hlcIsZero, registeredRoots, renderTreeToLocal, withBridge } from '~/lib/crdt'
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
 * of the parent container. Both the store's `updateGeometry` clamp and the
 * chrome resize handle reference this so the window can't be dragged below
 * 5% of the viewport in either dimension.
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
 * Structural equality for string id sets. Backs the `liveWindowIds`
 * memo so geometry-only updates (which keep the membership stable
 * but produce a fresh Set instance each tick) don't notify the GC
 * effect.
 */
function idSetsEqual(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size)
    return false
  for (const id of a) {
    if (!b.has(id))
      return false
  }
  return true
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
export function createFloatingWindowStore() {
  // Z-order: the array of window ids in render order (last = topmost).
  // Local-only — z-order isn't a CRDT register.
  const [zOrder, setZOrder] = createSignal<string[]>([])
  // Per-window local focus state.
  const [focusByWindow, setFocusByWindow] = createSignal<Map<string, string | null>>(new Map())

  // Raw projection memo: produces a fresh ordered FloatingWindowState
  // array every time the CRDT speculative state, zOrder, or focusByWindow
  // changes. The values here are throw-away refs — they feed into the
  // store reconcile below so consumers see stable refs with granular
  // field updates.
  const rawProjection = createMemo<FloatingWindowState[]>(() => withBridge<FloatingWindowState[]>((bridge) => {
    const state = bridge.speculativeState()
    const wsId = bridge.workspaceId()
    if (!state || !wsId)
      return []
    const result: FloatingWindowState[] = []
    // Precompute roots + child index ONCE for the whole memo run so we
    // don't pay O(N) per floating window. Without this each
    // floatingWindowToRendered call walks state.nodes twice
    // (registeredRoots + buildChildIndex).
    const precomputed = { roots: registeredRoots(state), childIndex: buildChildIndex(state) }
    const focusMap = focusByWindow()
    for (const fw of Object.values(state.floatingWindows)) {
      if (!hlcIsZero(fw.tombstoneAt))
        continue
      const ownWs = fw.workspaceId?.value ?? ''
      if (ownWs !== wsId)
        continue
      const rendered = floatingWindowToRendered(state, fw, precomputed)
      if (!rendered)
        continue
      const layoutRoot = renderTreeToLocal(rendered.innerTree)
        ?? { type: 'leaf' as const, id: fw.rootNodeId || `__empty_${fw.windowId}` }
      const fallbackFocus = firstLeafId(layoutRoot) ?? null
      const localFocus = focusMap.get(fw.windowId)
      result.push({
        id: fw.windowId,
        x: rendered.x,
        y: rendered.y,
        width: rendered.width,
        height: rendered.height,
        opacity: rendered.opacity,
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
  }, []))

  // Store-backed projection. Solid's `<For>` keys by REFERENCE identity
  // and the CRDT bridge bumps `pendingVersion` on every mutation —
  // including high-frequency events like pointermove-driven
  // `updatePosition` calls during a drag. If we returned `rawProjection`
  // directly to `<For>`, every drag frame would produce a new array of
  // fresh objects and `<For>` would unmount + remount every container
  // (and its inner TilingLayout / TabBar / Terminal subtree). The
  // result was visibly sluggish drag and resize.
  //
  // `reconcile` keyed by `id` with `merge: true` mutates the existing
  // store entries in place when ids match — same array element ref
  // across frames, but individual fields update granularly. Crucially,
  // `merge: true` preserves NESTED refs (notably `layoutRoot`) when
  // the diff finds no field-level change inside them, so downstream
  // memos keyed on `layoutRoot` (e.g. `tileSetsByWindow`) don't
  // invalidate on geometry-only ticks. With `merge: false` every
  // pendingVersion bump replaced `layoutRoot` ref-by-ref even when
  // the inner tree hadn't moved, undermining the structural-equality
  // gate further down — the tests in this file's `projection ref
  // stability` block pin both layers (per-window Set ref-stable
  // across drag/resize scrubs, Map ref-stable across geometry-only
  // batches, invalidation on real splits).
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
    setStoreState('list', reconcile(rawProjection(), { key: 'id', merge: true }))
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

  // Live window-id set, memoized with a structural-equals comparator
  // so it only changes when the set membership actually changes (a
  // peer create or tombstone) — NOT on every geometry update / drag /
  // resize tick. Drives the GC effect below so the GC only runs when
  // the GC question can actually have a new answer.
  const liveWindowIds = createMemo<ReadonlySet<string>>(
    () => new Set(projectedWindows().map(w => w.id)),
    new Set(),
    { equals: idSetsEqual },
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
      const focusMap = focusByWindow()
      let stale = false
      for (const id of focusMap.keys()) {
        if (!live.has(id)) {
          stale = true
          break
        }
      }
      if (stale) {
        const next = new Map(focusMap)
        for (const id of focusMap.keys()) {
          if (!live.has(id))
            next.delete(id)
        }
        setFocusByWindow(next)
      }
    }),
  )

  // Per-window leaf-id sets, computed in a single pass over the
  // reconcile-backed `projectedWindows()`. With `reconcile({merge:
  // true})` above, each window entry's `layoutRoot` ref is preserved
  // across CRDT ticks that don't change its inner tree, so this memo
  // only re-runs (and only emits a new Map) when a window's tree
  // actually mutates. The structural `equals` check below is a
  // defense-in-depth backstop for the (rare) case where the upstream
  // diff produces a same-content fresh ref.
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

  const tileToWindowId = createMemo(() => {
    const m = new Map<string, string>()
    for (const [winId, set] of tileSetsByWindow()) {
      for (const tileId of set)
        m.set(tileId, winId)
    }
    return m
  })

  const allFloatingTileIdsMemo = createMemo(() => {
    const out: string[] = []
    for (const set of tileSetsByWindow().values()) {
      for (const id of set)
        out.push(id)
    }
    return out
  })

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
    setZOrder(z => z.includes(id) ? z.filter(x => x !== id) : z)
    setFocusByWindow((m) => {
      if (!m.has(id))
        return m
      const next = new Map(m)
      next.delete(id)
      return next
    })
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

    updatePosition(id: string, x: number, y: number) {
      withBridge((bridge) => {
        const w = findWindow(id)
        if (!w || (w.x === x && w.y === y))
          return
        emitUpdatePosition(bridge, id, x, y)
      }, undefined as void)
    },

    updateGeometry(id: string, x: number, y: number, width: number, height: number) {
      withBridge((bridge) => {
        const clampedW = Math.max(width, MIN_WINDOW_DIMENSION)
        const clampedH = Math.max(height, MIN_WINDOW_DIMENSION)
        const w = findWindow(id)
        if (!w || (w.x === x && w.y === y && w.width === clampedW && w.height === clampedH))
          return
        emitUpdateGeometry(bridge, id, x, y, clampedW, clampedH)
      }, undefined as void)
    },

    updateOpacity(id: string, opacity: number): boolean {
      return withBridge((bridge) => {
        const clamped = Math.max(0.2, Math.min(1, opacity))
        const w = findWindow(id)
        if (!w || w.opacity === clamped)
          return false
        emitUpdateOpacity(bridge, id, clamped)
        return true
      }, false)
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

    setFocusedTile(windowId: string, tileId: string) {
      const w = findWindow(windowId)
      if (!w || w.focusedTileId === tileId)
        return
      setFocusByWindow((m) => {
        const next = new Map(m)
        next.set(windowId, tileId)
        return next
      })
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

    getWindowForTile(tileId: string): string | null {
      return tileToWindowId().get(tileId) ?? null
    },

    getAllTileIds(): string[] {
      return allFloatingTileIdsMemo()
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
     */
    removeIfEmpty(
      windowId: string,
      getTabsForTile: (tileId: string) => unknown[],
      onRemoved?: (removedTileId: string) => void,
    ): boolean {
      return withBridge<boolean>((bridge) => {
        const win = findWindow(windowId)
        if (!win)
          return false
        if (hasMultipleLeaves(win.layoutRoot))
          return false
        const removedTileId = firstLeafId(win.layoutRoot)
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
