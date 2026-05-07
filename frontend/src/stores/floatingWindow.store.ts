import type { CloseTileResult, GridAxis, LayoutNodeLocal, SplitOrientation } from './layout.store'
import type { LayoutOwner } from './layoutOwner'
import type { FloatingWindow as FloatingWindowProto } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { createMemo, mapArray } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { FloatingWindowSchema } from '~/generated/leapmux/v1/workspace_pb'
import { makeIdGenerator } from '~/lib/idGenerator'
import {
  applyGridRatios,
  applySplitRatios,
  cloneNode,
  containsTileId,
  findGridById,
  findHeirTileId,
  firstLeafId,
  fromProto,
  getAllTileIds,
  hasMultipleLeaves,
  makeGridInTree,
  planCloseTile,
  planRemoveGrid,
  planReplaceGridWithLeaf,
  rootFocusOf,
  splitTileInTree,
  toProto,
} from './layout.store'

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

export function createFloatingWindowStore() {
  // Per-store generators: counter resets per workspace so two stores
  // don't share counter state through a module-level singleton.
  const generateFwId = makeIdGenerator('fw')
  const generateFwTileId = makeIdGenerator('fwtile')
  const [state, setState] = createStore<FloatingWindowStoreState>({
    windows: [],
  })

  // Per-window leaf-id sets. `mapArray` keys by reference, so a window's
  // memo persists across other windows' mutations: only the window whose
  // `layoutRoot` actually changed re-walks its tree.
  const windowTileSets = mapArray(
    () => state.windows,
    (w) => {
      const memo = createMemo(() => new Set(getAllTileIds(w.layoutRoot)))
      return memo
    },
  )

  // Inverse index: tileId → windowId. Built atop `windowTileSets` so a
  // single window mutation re-walks one tree, then this memo flattens the
  // per-window sets in O(total tiles). Lookups become O(1) instead of the
  // O(W × tilesPerWindow) the per-window walk would do per consult.
  //
  // Intentionally rebuilt from scratch on each window mutation rather
  // than maintained incrementally. An incremental index would need either
  // a stable Map mutated in place behind a version signal, or per-window
  // `createEffect`+`onCleanup` bookkeeping that distinguishes "tiles
  // changed" from "window removed" — both add real complexity. Realistic
  // workspaces hold ≤10 floating windows × ≤10 tiles, so the rebuild is
  // ~100 Map.set calls and only fires on structural mutation (never on
  // pointer-move). Revisit only if profiling shows this on the hot path.
  const tileToWindowId = createMemo(() => {
    const m = new Map<string, string>()
    const sets = windowTileSets()
    for (let i = 0; i < sets.length; i++) {
      const id = state.windows[i].id
      for (const tileId of sets[i]())
        m.set(tileId, id)
    }
    return m
  })

  // Flat union of every floating-window tile id. Reuses `windowTileSets`
  // so structural mutations re-walk only the affected window's tree;
  // workspace-restore and similar non-render consumers consult this
  // without forcing a fresh getAllTileIds walk per call.
  //
  // Same rebuild-from-scratch trade-off as `tileToWindowId` — kept
  // simple deliberately. See the comment above before proposing an
  // incremental rewrite.
  const allFloatingTileIdsMemo = createMemo(() => {
    const sets = windowTileSets()
    const out: string[] = []
    for (let i = 0; i < sets.length; i++) {
      for (const tileId of sets[i]())
        out.push(tileId)
    }
    return out
  })

  /** Drop the window with `id` from `state.windows`. */
  function dropWindow(id: string) {
    setState('windows', w => w.filter(win => win.id !== id))
  }

  // O(1) windowId → index map. The wheel-opacity hot path on the titlebar
  // can fire ~60 times/sec; without this, every tick walks `state.windows`
  // linearly.
  const windowIdToIndex = createMemo(() => {
    const m = new Map<string, number>()
    for (let i = 0; i < state.windows.length; i++)
      m.set(state.windows[i].id, i)
    return m
  })

  function findWindowIndex(id: string): number {
    return windowIdToIndex().get(id) ?? -1
  }

  function findWindow(id: string): FloatingWindowState | undefined {
    const idx = findWindowIndex(id)
    return idx < 0 ? undefined : state.windows[idx]
  }

  /**
   * Run `fn` against an existing window and return its result. If the id
   * is gone, return `fallback`. Callers whose `fn` returns `void` should
   * use {@link withWindowVoid} instead, which avoids the dummy-fallback
   * boilerplate.
   */
  function withWindow<R>(
    id: string,
    fn: (idx: number, win: FloatingWindowState) => R,
    fallback: R,
  ): R {
    const idx = findWindowIndex(id)
    if (idx < 0)
      return fallback
    return fn(idx, state.windows[idx])
  }

  /** Void counterpart to {@link withWindow} — no-ops when the id is gone. */
  function withWindowVoid(
    id: string,
    fn: (idx: number, win: FloatingWindowState) => void,
  ): void {
    const idx = findWindowIndex(id)
    if (idx < 0)
      return
    fn(idx, state.windows[idx])
  }

  // Hoisted method impls. Both the store object and the per-window
  // `LayoutOwner` cache reference these, so each window's owner can be
  // built once via `mapArray` instead of allocated fresh per `owner()`
  // call.
  const splitTile = (windowId: string, tileId: string, direction: SplitOrientation): string | null => {
    return withWindow(windowId, (idx, win) => {
      // Pre-flight: skip ID generation when the tile isn't in this window's
      // tree (e.g. a race against close).
      if (!containsTileId(win.layoutRoot, tileId))
        return null
      const newTileId = generateFwTileId()
      const newRoot = splitTileInTree(win.layoutRoot, tileId, newTileId, direction, generateFwTileId)
      if (newRoot === win.layoutRoot)
        return null
      setState('windows', idx, 'layoutRoot', newRoot)
      return newTileId
    }, null)
  }

  const makeGrid = (windowId: string, tileId: string, rows: number, cols: number): { gridId: string, cellTileIds: string[] } | null => {
    return withWindow(windowId, (idx, win) => {
      // Pre-flight: avoid allocating gridId + rows*cols cell ids when the
      // target tile isn't in this window's tree.
      if (!containsTileId(win.layoutRoot, tileId))
        return null
      const { newRoot, gridId, cellTileIds } = makeGridInTree(win.layoutRoot, tileId, rows, cols, generateFwTileId)
      setState('windows', idx, 'layoutRoot', newRoot)
      return { gridId, cellTileIds }
    }, null)
  }

  const removeGrid = (windowId: string, gridId: string): void => {
    withWindowVoid(windowId, (idx, win) => {
      const next = planRemoveGrid(rootFocusOf(win), gridId, generateFwTileId)
      if (!next)
        return
      setState('windows', idx, produce((w) => {
        w.layoutRoot = next.root
        w.focusedTileId = next.focusedTileId
      }))
    })
  }

  const replaceGridWithLeaf = (windowId: string, gridId: string): string | null => {
    return withWindow(windowId, (idx, win) => {
      const next = planReplaceGridWithLeaf(rootFocusOf(win), gridId, generateFwTileId)
      if (!next)
        return null
      setState('windows', idx, produce((w) => {
        w.layoutRoot = next.root
        w.focusedTileId = next.focusedTileId
      }))
      return next.newTileId
    }, null)
  }

  // Build a `LayoutOwner` once per windowId. The closure captures only the
  // id, so the methods stay valid across `layoutRoot` mutations (they
  // dynamically resolve the current window via `findWindow`). Pre-building
  // here saves the allocation on every `owner()` call from `TileRenderer`.
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

  // Per-window owner cache. `mapArray` keys by reference so each id's
  // entry is created once on first sight and disposed when its window is
  // removed; the surrounding `Map` memo only rebuilds when the window list
  // reshapes.
  const ownerEntries = mapArray(
    () => state.windows,
    w => [w.id, buildOwner(w.id)] as const,
  )
  const ownersById = createMemo(() => new Map(ownerEntries()))

  return {
    state,

    /**
     * Append a new window. Array order encodes z-order — the last entry is
     * topmost — so a freshly-added window automatically lands above all
     * existing ones without an explicit `zIndex` field. The render layer
     * maps `idx` to a `z-index` value; see `FloatingWindowLayer`.
     *
     * When the caller passes neither `x` nor `y`, the new window cascades
     * off the default position by the current window count's slot so users
     * who pop out several tabs in a row see distinct stacking positions
     * instead of every window landing on top of the previous one.
     */
    addWindow(opts?: { x?: number, y?: number, width?: number, height?: number }): { windowId: string, tileId: string } {
      const tileId = generateFwTileId()
      const windowId = generateFwId()
      // Cascade only when no explicit coordinate is given — explicit
      // positions (e.g. spawn at drop point) should land exactly where the
      // caller asked.
      const slot = opts?.x === undefined && opts?.y === undefined
        ? state.windows.length % CASCADE_WRAP
        : 0
      setState(produce((s) => {
        s.windows.push({
          id: windowId,
          x: opts?.x ?? (DEFAULT_FW_GEOMETRY.x + slot * CASCADE_STEP),
          y: opts?.y ?? (DEFAULT_FW_GEOMETRY.y + slot * CASCADE_STEP),
          width: opts?.width ?? DEFAULT_FW_GEOMETRY.width,
          height: opts?.height ?? DEFAULT_FW_GEOMETRY.height,
          opacity: 1,
          layoutRoot: { type: 'leaf', id: tileId },
          focusedTileId: tileId,
        })
      }))
      return { windowId, tileId }
    },

    removeWindow(id: string) {
      if (findWindowIndex(id) < 0)
        return
      dropWindow(id)
    },

    updatePosition(id: string, x: number, y: number) {
      withWindowVoid(id, (idx, w) => {
        if (w.x === x && w.y === y)
          return
        setState('windows', idx, produce((w) => {
          w.x = x
          w.y = y
        }))
      })
    },

    /**
     * Apply position + size in one reactive transaction. Resize pointermove
     * mutates both per frame; writing them separately would fire two
     * `setState` cascades per move event. Width/height are clamped to
     * `MIN_WINDOW_DIMENSION`.
     */
    updateGeometry(id: string, x: number, y: number, width: number, height: number) {
      const clampedW = Math.max(width, MIN_WINDOW_DIMENSION)
      const clampedH = Math.max(height, MIN_WINDOW_DIMENSION)
      withWindowVoid(id, (idx, w) => {
        if (w.x === x && w.y === y && w.width === clampedW && w.height === clampedH)
          return
        setState('windows', idx, produce((w) => {
          w.x = x
          w.y = y
          w.width = clampedW
          w.height = clampedH
        }))
      })
    },

    /**
     * Clamp `opacity` to [0.2, 1] and store. Returns true iff the value
     * actually changed — callers (e.g. wheel handlers firing ~60×/sec) use
     * this to gate downstream persist calls so a saturated wheel doesn't
     * trigger a persist on every tick once opacity is pinned at a clamp.
     */
    updateOpacity(id: string, opacity: number): boolean {
      const clamped = Math.max(0.2, Math.min(1, opacity))
      return withWindow(id, (idx, w) => {
        if (w.opacity === clamped)
          return false
        setState('windows', idx, 'opacity', clamped)
        return true
      }, false)
    },

    /**
     * Move the window to the end of `state.windows` so the render layer
     * paints it on top. The last entry is canonical "topmost"; explicit
     * z-index values aren't stored anywhere. Skipped when already at the
     * end — every pointerdown/drag-start/resize-start routes here, so the
     * guard prevents reactive churn on repeated clicks against the
     * foreground window.
     */
    bringToFront(id: string) {
      withWindowVoid(id, (idx) => {
        if (idx === state.windows.length - 1)
          return
        setState('windows', produce((wins) => {
          const [w] = wins.splice(idx, 1)
          wins.push(w)
        }))
      })
    },

    setFocusedTile(windowId: string, tileId: string) {
      withWindowVoid(windowId, (idx, w) => {
        if (w.focusedTileId === tileId)
          return
        setState('windows', idx, 'focusedTileId', tileId)
      })
    },

    splitTile,

    closeTile(windowId: string, tileId: string): CloseTileResult {
      return withWindow(windowId, (idx, win) => {
        const plan = planCloseTile(rootFocusOf(win), tileId, generateFwTileId)
        if (plan.kind === 'noop')
          return { kind: 'noop' }
        if (plan.kind === 'empty') {
          // Snapshot the about-to-disappear tile ids before dropping the
          // window — the per-window memo is invalidated as soon as
          // `dropWindow` re-keys `state.windows`, and callers need this
          // set to scrub tab-store entries for every disposed tile.
          const tileIds = windowTileSets()[idx]?.() ?? new Set<string>()
          dropWindow(windowId)
          return { kind: 'disposed', tileIds }
        }
        setState('windows', idx, produce((w) => {
          w.layoutRoot = plan.root
          w.focusedTileId = plan.focusedTileId
        }))
        return { kind: 'changed' }
      }, { kind: 'noop' })
    },

    makeGrid,

    removeGrid,

    replaceGridWithLeaf,

    updateGridRatios(windowId: string, gridId: string, axis: GridAxis, ratios: number[]): boolean {
      return withWindow(windowId, (idx) => {
        let mutated = false
        setState('windows', idx, 'layoutRoot', produce((root) => {
          mutated = applyGridRatios(root, gridId, axis, ratios)
        }))
        return mutated
      }, false)
    },

    updateRatios(windowId: string, splitId: string, ratios: number[]): boolean {
      return withWindow(windowId, (idx) => {
        let mutated = false
        setState('windows', idx, 'layoutRoot', produce((root) => {
          mutated = applySplitRatios(root, splitId, ratios)
        }))
        return mutated
      }, false)
    },

    getWindowForTile(tileId: string): string | null {
      return tileToWindowId().get(tileId) ?? null
    },

    getAllTileIds(): string[] {
      return allFloatingTileIdsMemo()
    },

    getWindow(id: string): FloatingWindowState | undefined {
      return findWindow(id)
    },

    /**
     * Returns the cached `Set<string>` of tile ids for the window, reusing
     * `windowTileSets` (per-window memo) so structural mutations re-walk
     * only the affected window's tree. Returns `null` if the window has
     * been disposed.
     */
    getWindowTileIdSet(windowId: string): ReadonlySet<string> | null {
      const idx = findWindowIndex(windowId)
      if (idx < 0)
        return null
      return windowTileSets()[idx]?.() ?? null
    },

    owner(windowId: string): LayoutOwner {
      // Cached owner whose lifetime is bound to the window's presence in
      // `state.windows` via `mapArray`. The fallback `buildOwner(windowId)`
      // covers the rare race where a caller asks for an owner of a window
      // that isn't in the cache yet (or has just been disposed) — its
      // methods all guard with `findWindow` and degrade to no-op / null,
      // matching the prior behaviour.
      return ownersById().get(windowId) ?? buildOwner(windowId)
    },

    /**
     * Remove a single-tile floating window when its tile becomes empty (e.g.
     * the popped-out tab is closed). Multi-tile windows are left alone — the
     * user explicitly built that structure, so empty tiles stick around the
     * same way they do in the main layout, and the chrome close button is
     * the only way to dispose the window.
     *
     * `onRemoved` fires with the disposed tile id so callers can refresh
     * focus or persist state. Returns true when the window was removed.
     */
    removeIfEmpty(
      windowId: string,
      getTabsForTile: (tileId: string) => unknown[],
      onRemoved?: (removedTileId: string) => void,
    ): boolean {
      const win = findWindow(windowId)
      if (!win)
        return false
      // Probe with `hasMultipleLeaves` (early-exits at the second leaf) +
      // `firstLeafId` instead of materializing the full tile-id array, so
      // the common close-tab path doesn't walk every leaf in the window.
      if (hasMultipleLeaves(win.layoutRoot))
        return false
      const removedTileId = firstLeafId(win.layoutRoot)
      if (!removedTileId)
        return false
      if (getTabsForTile(removedTileId).length !== 0)
        return false
      dropWindow(windowId)
      onRemoved?.(removedTileId)
      return true
    },

    toProto(): FloatingWindowProto[] {
      // `floatingWindowsToProto` only reads (`.map` allocates a new array),
      // so the live store array is safe to pass without a defensive copy.
      return floatingWindowsToProto(state.windows)
    },

    fromProto(protos: FloatingWindowProto[]) {
      // Proto order is preserved as array order, which is also z-order
      // (last = topmost). Saved layouts therefore round-trip the user's
      // foreground/background stacking without persisting a separate
      // z-index field.
      const windows: FloatingWindowState[] = protos.map((p) => {
        const layoutRoot = p.layout ? fromProto(p.layout, generateFwTileId) : { type: 'leaf' as const, id: generateFwTileId() }
        // Use `> 0` instead of `||` so a legitimately-tiny stored value still
        // wins over the default; subsequent clamping (MIN_WINDOW_DIMENSION,
        // and a [0, 1] clamp on opacity in updateOpacity) handles bounds.
        return {
          id: p.id || generateFwId(),
          x: p.x,
          y: p.y,
          width: p.width > 0 ? p.width : DEFAULT_FW_GEOMETRY.width,
          height: p.height > 0 ? p.height : DEFAULT_FW_GEOMETRY.height,
          opacity: p.opacity > 0 ? p.opacity : 1,
          layoutRoot,
          focusedTileId: firstLeafId(layoutRoot) ?? null,
        }
      })
      setState('windows', windows)
    },

    snapshot(): FloatingWindowStoreState {
      return {
        windows: state.windows.map(w => ({
          ...w,
          layoutRoot: cloneNode(w.layoutRoot),
        })),
      }
    },

    restore(snap: FloatingWindowStoreState) {
      setState('windows', snap.windows.map(w => ({
        ...w,
        layoutRoot: cloneNode(w.layoutRoot),
      })))
    },
  }
}

export type FloatingWindowStoreType = ReturnType<typeof createFloatingWindowStore>

/** Pure function to convert floating window state to proto, usable outside a reactive root. */
export function floatingWindowsToProto(windows: FloatingWindowState[]): FloatingWindowProto[] {
  return windows.map(w => create(FloatingWindowSchema, {
    id: w.id,
    x: w.x,
    y: w.y,
    width: w.width,
    height: w.height,
    opacity: w.opacity,
    layout: toProto(w.layoutRoot),
  }))
}
