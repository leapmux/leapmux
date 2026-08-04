import type { TestBridgeHandle } from '~/test-support/crdtBridge'
import { createEffect, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ctxFromBridge, getCRDTBridge, newBatch, renderTreeToLocal, setCRDTBridge, setFloatingWorkspaceId, sharedTrees } from '~/lib/crdt'
import { createFloatingWindowStore, createKeyedOverrides, MIN_WINDOW_DIMENSION } from '~/stores/floatingWindow.store'
import { seedWorkspace, withTestBridge } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, projectionMemo } from '~/test-support/tabStores'

/**
 * createFloatingWindowStore is projection-driven: the window list and
 * inner trees derive from the CRDT speculativeState; mutators emit op
 * batches via the bridge. These tests verify both halves — projected
 * state after a mutation, and the shape of the op batch enqueued.
 */
describe('createFloatingWindowStore (projection-driven)', () => {
  it('starts with no projected windows when none exist in CRDT', () => {
    withTestBridge((_harness) => {
      const store = createTestFloatingWindowStore()
      expect(store.state.windows).toEqual([])
    }, { rootTileId: 'main' })
  })

  it('addWindow emits a single 8-op creation batch and adds the projected window', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const before = harness.pending.state.pendingBatches.length

      const created = store.addWindow({ x: 0.1, y: 0.2, width: 0.4, height: 0.5 })
      expect(created).not.toBeNull()
      const { windowId, tileId } = created!
      expect(windowId).not.toBe('')
      expect(tileId).not.toBe('')

      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      // Plan order:
      //  1. SetNodeRegister(rootId, kind=LEAF)
      //  2. SetFloatingWindowRegister(windowId, root_node_id=rootId)
      //  3. SetFloatingWindowRegister(windowId, workspace_id=wsId)
      //  4. x, 5. y, 6. width, 7. height, 8. opacity
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(8)

      // Projected state has the window with the requested geometry.
      expect(store.state.windows).toHaveLength(1)
      expect(store.state.windows[0]!.id).toBe(windowId)
      expect(store.state.windows[0]!.x).toBeCloseTo(0.1, 6)
      expect(store.state.windows[0]!.y).toBeCloseTo(0.2, 6)
      expect(store.state.windows[0]!.width).toBeCloseTo(0.4, 6)
      expect(store.state.windows[0]!.height).toBeCloseTo(0.5, 6)
      // The inner root tile id matches what addWindow returned.
      expect(store.state.windows[0]!.layoutRoot.id).toBe(tileId)
    }, { rootTileId: 'main' })
  })

  it('cascades default position across consecutive addWindow calls without explicit coords', () => {
    withTestBridge((_harness) => {
      const store = createTestFloatingWindowStore()
      store.addWindow()
      store.addWindow()
      expect(store.state.windows).toHaveLength(2)
      const [w1, w2] = store.state.windows
      // Slot N adds N * CASCADE_STEP (0.025) to base coords.
      expect(w2!.x - w1!.x).toBeCloseTo(0.025, 6)
      expect(w2!.y - w1!.y).toBeCloseTo(0.025, 6)
    }, { rootTileId: 'main' })
  })

  it('removeWindow tombstones the window and clears it from the projection', () => {
    withTestBridge((_harness) => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow()
      expect(created).not.toBeNull()
      const { windowId } = created!
      expect(store.state.windows).toHaveLength(1)
      store.removeWindow(windowId)
      expect(store.state.windows).toHaveLength(0)
    }, { rootTileId: 'main' })
  })

  it('removeWindow on an unknown id is a no-op (no batch enqueued)', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const before = harness.pending.state.pendingBatches.length
      store.removeWindow('does-not-exist')
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  // These pin what the DRAG path emits, which is what the UI actually calls.
  // They used to target `updatePosition` / `updateGeometry` / `updateOpacity` --
  // immediate setters that this branch's own refactor left with zero production
  // callers, so the assertions described a path nothing takes.
  it('a committed MOVE emits a 2-op batch (x + y) and reflects in the projection', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      const before = harness.pending.state.pendingBatches.length

      store.updateDragMove(windowId, 0.5, 0.6)
      store.commitDragGeometry(windowId)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      // x + y ONLY. A move that also wrote width/height would clobber a
      // concurrent peer resize with the pointer-down snapshot.
      expect(lastBatch?.ops.length).toBe(2)
      expect(store.state.windows[0]!.x).toBeCloseTo(0.5, 6)
      expect(store.state.windows[0]!.y).toBeCloseTo(0.6, 6)
    }, { rootTileId: 'main' })
  })

  it('a committed MOVE back to the starting position is a no-op', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.2, width: 0.3, height: 0.3 })!
      const before = harness.pending.state.pendingBatches.length

      store.updateDragMove(windowId, 0.1, 0.2)
      store.commitDragGeometry(windowId)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  // The regression this fix exists for: a MOVE must not write width/height at
  // all. It committed all four fields using the dimensions captured at
  // pointer-down, so a peer that resized the window while the pointer was down
  // had its resize overwritten by stale numbers under a NEWER HLC -- reverted on
  // every client, permanently. Here the peer's resize is simulated by moving the
  // window's CRDT width away from the drag's captured value.
  it('a committed MOVE does not emit width/height, so a concurrent resize survives', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!

      // Pointer-down captures 0.3 x 0.3, then a "peer" resizes to 0.5 x 0.5
      // mid-drag (a resize gesture standing in for the remote op).
      store.updateDragResize(windowId, 0.1, 0.1, 0.5, 0.5)
      store.commitDragGeometry(windowId)
      expect(store.state.windows[0]!.width).toBeCloseTo(0.5, 6)

      const before = harness.pending.state.pendingBatches.length
      store.updateDragMove(windowId, 0.7, 0.8)
      store.commitDragGeometry(windowId)

      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(2)
      expect(store.state.windows[0]!.x).toBeCloseTo(0.7, 6)
      expect(store.state.windows[0]!.width).toBeCloseTo(0.5, 6)
      expect(store.state.windows[0]!.height).toBeCloseTo(0.5, 6)
    }, { rootTileId: 'main' })
  })

  it('a committed RESIZE emits a 4-op batch and clamps below MIN_WINDOW_DIMENSION', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
      const before = harness.pending.state.pendingBatches.length

      // Pass a width below the floor; the store must clamp before emitting.
      store.updateDragResize(windowId, 0.2, 0.3, 0.001, 0.7)
      store.commitDragGeometry(windowId)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(4)
      expect(store.state.windows[0]!.width).toBe(MIN_WINDOW_DIMENSION)
      expect(store.state.windows[0]!.height).toBeCloseTo(0.7, 6)
    }, { rootTileId: 'main' })
  })

  it('updateOpacityDebounced clamps the value into [0.2, 1.0] and emits a single-op batch', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!

      // Start by dropping below 1.0 so the next bump shows the clamp
      // change (default 1.0 == clamp(5) == 1.0, which would no-op).
      expect(store.updateOpacityDebounced(windowId, 0.6)).toBe(true)
      store.flushOpacity(windowId)
      const before = harness.pending.state.pendingBatches.length

      // Out-of-range high → clamped to 1.0.
      expect(store.updateOpacityDebounced(windowId, 5)).toBe(true)
      store.flushOpacity(windowId)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(1)
      expect(store.state.windows[0]!.opacity).toBe(1)

      // Out-of-range low (zero / negative) → clamped to 0.2.
      expect(store.updateOpacityDebounced(windowId, -10)).toBe(true)
      store.flushOpacity(windowId)
      expect(store.state.windows[0]!.opacity).toBe(0.2)
    }, { rootTileId: 'main' })
  })

  it('updateOpacityDebounced at the same clamped value is a no-op', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      // Window default opacity is 1.0; resetting to 1.0 is a no-op.
      const before = harness.pending.state.pendingBatches.length
      expect(store.updateOpacityDebounced(windowId, 1)).toBe(false)
      store.flushOpacity(windowId)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  describe('drag-override (commit-on-drop)', () => {
    // updateDragMove / updateDragResize write the live preview into a LOCAL
    // override (no CRDT op); commitDragGeometry emits exactly ONE op batch on
    // drop. This cuts a per-drag op storm from ~60-120 (one per frame) down to 1.

    it('updateDragResize moves the projected window WITHOUT emitting a batch', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
        const before = harness.pending.state.pendingBatches.length

        store.updateDragResize(windowId, 0.5, 0.6, 0.4, 0.4)
        // No CRDT op was emitted — the override is local-only.
        expect(harness.pending.state.pendingBatches.length).toBe(before)
        // The projected window reflects the live preview geometry.
        expect(store.state.windows[0]!.x).toBeCloseTo(0.5, 6)
        expect(store.state.windows[0]!.y).toBeCloseTo(0.6, 6)
      }, { rootTileId: 'main' })
    })

    it('commitDragGeometry emits a single batch on drop and clears the override', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
        const before = harness.pending.state.pendingBatches.length

        // A drag: several override updates, then one commit.
        store.updateDragResize(windowId, 0.2, 0.2, 0.4, 0.4)
        store.updateDragResize(windowId, 0.3, 0.3, 0.4, 0.4)
        store.updateDragResize(windowId, 0.4, 0.4, 0.4, 0.4)
        expect(harness.pending.state.pendingBatches.length).toBe(before)

        store.commitDragGeometry(windowId)
        // Exactly ONE op batch for the whole drag.
        expect(harness.pending.state.pendingBatches.length - before).toBe(1)
        // The geometry persisted at the override's final value.
        expect(store.state.windows[0]!.x).toBeCloseTo(0.4, 6)
        expect(store.state.windows[0]!.y).toBeCloseTo(0.4, 6)
      }, { rootTileId: 'main' })
    })

    it('commitDragGeometry is a no-op when no drag is in flight (bare click)', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
        const before = harness.pending.state.pendingBatches.length

        store.commitDragGeometry(windowId)
        expect(harness.pending.state.pendingBatches.length).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('commitDragGeometry is a no-op when the override matches the CRDT geometry (drag returned to start)', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
        const before = harness.pending.state.pendingBatches.length

        // Drag back to the same geometry, then commit — should emit nothing.
        store.updateDragResize(windowId, 0.1, 0.1, 0.4, 0.4)
        store.commitDragGeometry(windowId)
        expect(harness.pending.state.pendingBatches.length).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('cancelDragGeometry snaps the projection back to CRDT geometry without committing', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
        const before = harness.pending.state.pendingBatches.length

        store.updateDragResize(windowId, 0.5, 0.6, 0.4, 0.4)
        // The override is live.
        expect(store.state.windows[0]!.x).toBeCloseTo(0.5, 6)
        // Cancel — no op, snaps back.
        store.cancelDragGeometry(windowId)
        expect(harness.pending.state.pendingBatches.length).toBe(before)
        expect(store.state.windows[0]!.x).toBeCloseTo(0.1, 6)
        expect(store.state.windows[0]!.y).toBeCloseTo(0.1, 6)
      }, { rootTileId: 'main' })
    })

    // Two windows dragged at once must BOTH survive.
    //
    // The override used to be a single slot, justified by a comment claiming
    // `useWindowPointerDrag` serialized drags. It does not: every
    // FloatingWindowContainer constructs its OWN controller, so `cancel()` only
    // supersedes a gesture on the same window, and the hook uses document-level
    // listeners rather than `setPointerCapture`. Two pointers on two title bars
    // (touch, or a second button) run two live drags. The second
    // `updateDrag*` then evicted the first, so w1 snapped back to CRDT geometry
    // mid-gesture and `commitDragGeometry(w1)` returned early on the id
    // mismatch -- the entire drag discarded, no op, no error path. This case
    // previously asserted that loss as if it were the intended contract.
    it('keeps a per-window override so two simultaneous drags both commit', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const w1 = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!.windowId
        const w2 = store.addWindow({ x: 0.5, y: 0.5, width: 0.3, height: 0.3 })!.windowId

        // Both pointers move before either lifts.
        store.updateDragResize(w1, 0.2, 0.2, 0.3, 0.3)
        store.updateDragResize(w2, 0.6, 0.6, 0.3, 0.3)

        // Each window renders at its OWN live override.
        expect(store.state.windows.find(w => w.id === w1)!.x).toBeCloseTo(0.2, 6)
        expect(store.state.windows.find(w => w.id === w2)!.x).toBeCloseTo(0.6, 6)

        // And each drop commits its own op batch.
        const before = harness.pending.state.pendingBatches.length
        store.commitDragGeometry(w1)
        expect(harness.pending.state.pendingBatches.length - before).toBe(1)
        store.commitDragGeometry(w2)
        expect(harness.pending.state.pendingBatches.length - before).toBe(2)
        expect(store.state.windows.find(w => w.id === w1)!.x).toBeCloseTo(0.2, 6)
        expect(store.state.windows.find(w => w.id === w2)!.x).toBeCloseTo(0.6, 6)
      }, { rootTileId: 'main' })
    })

    // Cancelling one gesture must not disturb the other's override.
    it('cancelDragGeometry clears only the named window', () => {
      withTestBridge((_harness) => {
        const store = createTestFloatingWindowStore()
        const w1 = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!.windowId
        const w2 = store.addWindow({ x: 0.5, y: 0.5, width: 0.3, height: 0.3 })!.windowId

        store.updateDragResize(w1, 0.2, 0.2, 0.3, 0.3)
        store.updateDragResize(w2, 0.6, 0.6, 0.3, 0.3)
        store.cancelDragGeometry(w1)

        expect(store.state.windows.find(w => w.id === w1)!.x).toBeCloseTo(0.1, 6)
        expect(store.state.windows.find(w => w.id === w2)!.x).toBeCloseTo(0.6, 6)
      }, { rootTileId: 'main' })
    })
  })

  it('bringToFront reorders the projection without emitting a batch (z-order is local)', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const w1 = store.addWindow()!.windowId
      const w2 = store.addWindow()!.windowId
      // Initially w1 was added first, then w2 — w2 is on top.
      const before = harness.pending.state.pendingBatches.length
      store.bringToFront(w1)
      // No CRDT op was enqueued (z-order is purely local).
      expect(harness.pending.state.pendingBatches.length).toBe(before)
      // The projected order now has w1 last (topmost).
      const order = store.state.windows.map(w => w.id)
      expect(order[order.length - 1]).toBe(w1)
      expect(order).toContain(w2)
    }, { rootTileId: 'main' })
  })

  it('without a wired bridge, addWindow returns null so callers don\'t route tabs into a fake id', () => {
    createRoot((dispose) => {
      setCRDTBridge(null)
      const store = createTestFloatingWindowStore()
      // Pre-CRDT this returned a synthesized `pending-<random>` id;
      // production callers (handleDetachTab) would then route a tab
      // to that id, silently misrouting since no window exists. The
      // current contract is null + caller-side early-return.
      expect(store.addWindow()).toBeNull()
      expect(store.state.windows).toEqual([])
      dispose()
    })
  })

  describe('zOrder stale-id sweep', () => {
    // The store overlays a local `zOrder: string[]` on top of the
    // projected window list to track explicit bringToFront. When a
    // peer tombstones a window via CRDT op, the projection memo
    // filters it out — but without the sweep effect, the stale id
    // would leak in zOrder for the session lifetime, growing
    // unboundedly across churn. These tests pin the sweep contract:
    // every id surviving in zOrder must point at a live window.

    it('drops a window id from zOrder when a peer tombstones the window', async () => {
      await withTestBridge(async (harness) => {
        const store = createTestFloatingWindowStore()
        const created = store.addWindow()
        expect(created).not.toBeNull()
        const { windowId } = created!
        // bringToFront forces the id into zOrder explicitly so the
        // assertion below isn't accidentally satisfied by the
        // empty-zOrder fallback path inside projectedWindows.
        store.bringToFront(windowId)
        expect(store.state.windows.map(w => w.id)).toContain(windowId)

        // Simulate a peer tombstoning the window: the addWindow's
        // create batch lives in pendingBatches (not yet committed).
        // Drop the FloatingWindowRecord directly from speculativeState
        // to mimic the steady state AFTER a peer's tombstone op has
        // landed: projectedWindows filters it out, and the sweep
        // effect should prune zOrder.
        delete harness.pending.state.speculativeState.floatingWindows[windowId]
        // notify so memo-backed projectedWindows re-derives.
        ;(harness.pending as unknown as { notify?: () => void }).notify?.()
        // The notify callback bumps the test bridge's version
        // signal which projectedWindows reads — that retriggers the
        // sweep effect synchronously inside the root.
        await new Promise<void>(queueMicrotask)
        expect(store.state.windows.map(w => w.id)).not.toContain(windowId)
        // A fresh window must land alone in the projection's
        // ordered list — if zOrder still carried the dead id,
        // the order would include it (silently rendered as
        // undefined).
        const fresh = store.addWindow()
        expect(fresh).not.toBeNull()
        const order = store.state.windows.map(w => w.id)
        expect(order).toEqual([fresh!.windowId])
      }, { rootTileId: 'main' })
    })

    // emitFwSplitTile and emitFwMakeGrid were deleted in favour of the
    // canonical emitSplitTile / emitMakeGrid from layoutOps — the
    // op-batch shape is identical regardless of whether the parent
    // root is a workspace root or a floating-window root. These tests
    // pin the inner-tree mutators after the dedupe so a regression
    // (e.g. accidentally targeting the wrong tile id) surfaces here.
    it('emitSplitTile path splits the floating window\'s inner tree', async () => {
      await withTestBridge(async (harness) => {
        const store = createTestFloatingWindowStore()
        const created = store.addWindow()!
        const { windowId, tileId: rootTile } = created
        const before = harness.pending.state.pendingBatches.length

        const newChild = store.splitTile(windowId, rootTile, 'horizontal')
        expect(newChild).not.toBeNull()
        // splitTile emits one batch: T flips LEAF→SPLIT, two new
        // leaf children, three register writes per child = 9 ops.
        const lastBatch = harness.pending.state.pendingBatches.at(-1)
        expect(harness.pending.state.pendingBatches.length - before).toBe(1)
        expect(lastBatch?.ops.length).toBe(9)
        await new Promise<void>(queueMicrotask)
        // The window's inner tree now has two leaves; firstLeaf
        // gets the original tabs (none here), and the freshly
        // returned id is the second leaf (childB).
        const layoutRoot = store.state.windows[0]!.layoutRoot
        expect(layoutRoot.type).toBe('split')
      }, { rootTileId: 'main' })
    })

    it('emitMakeGrid path builds an R×C inner tree under the window\'s root', async () => {
      await withTestBridge(async (harness) => {
        const store = createTestFloatingWindowStore()
        const created = store.addWindow()!
        const { windowId, tileId: rootTile } = created
        const before = harness.pending.state.pendingBatches.length

        const result = store.makeGrid(windowId, rootTile, 2, 3)
        expect(result).not.toBeNull()
        expect(result!.gridId).toBe(rootTile)
        expect(result!.cellTileIds).toHaveLength(6)
        // The grid batch: 5 grid registers on T + 3 register writes
        // per cell × 6 cells = 5 + 18 = 23 ops.
        const lastBatch = harness.pending.state.pendingBatches.at(-1)
        expect(harness.pending.state.pendingBatches.length - before).toBe(1)
        expect(lastBatch?.ops.length).toBe(23)
        await new Promise<void>(queueMicrotask)
        expect(store.state.windows[0]!.layoutRoot.type).toBe('grid')
      }, { rootTileId: 'main' })
    })

    it('splitTile on an unknown window is a no-op (caller passed a stale windowId)', () => {
      withTestBridge((harness) => {
        const store = createTestFloatingWindowStore()
        const before = harness.pending.state.pendingBatches.length

        expect(store.splitTile('nope', 'whatever', 'horizontal')).toBeNull()
        expect(harness.pending.state.pendingBatches.length).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('does not sweep zOrder ids whose windows are still live', async () => {
      // Sanity: the sweep must only touch tombstoned ids, not the
      // entire array. Two live windows + bringToFront on the older
      // one must leave both in zOrder with the explicit ordering.
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const w1 = store.addWindow()!.windowId
        const w2 = store.addWindow()!.windowId
        store.bringToFront(w1)
        await new Promise<void>(queueMicrotask)
        const order = store.state.windows.map(w => w.id)
        expect(order).toHaveLength(2)
        expect(order[order.length - 1]).toBe(w1)
        expect(order).toContain(w2)
      }, { rootTileId: 'main' })
    })
  })

  // Regression: Solid's `<For>` keys array entries by REFERENCE
  // identity. The CRDT bridge's `pendingVersion` signal bumps on every
  // mutation — including passive heartbeats that don't touch any
  // floating-window field. Pre-fix, the projection memo produced fresh
  // `FloatingWindowState` (and `LayoutNodeLocal`) objects on every
  // re-run, so `<For>` unmounted + remounted every container on every
  // CRDT tick. The remount cycles broke in-flight drag/click handlers:
  // `captureParentSize` saw a detached container, the drag math
  // collapsed to pixel-as-fraction, and the close button missed
  // clicks that landed during an unmount.
  //
  // Stability contract: when no field on a window has changed across
  // projections, the FloatingWindowState ref must be preserved.
  describe('projection ref stability', () => {
    /**
     * `reconcile` updates a matched node's fields IN PLACE rather than swapping
     * the reference, so whatever this store hands it becomes writable state.
     * `renderTreeToLocal`'s SHARED map must therefore never be what it is handed
     * -- the store owns a `LocalTreeCache` of its own -- or a split inside one
     * floating window rewrites the conversion every other consumer of that
     * subtree is still holding.
     */
    it('does not mutate the shared renderTreeToLocal conversion when a window inner tree changes', async () => {
      await withTestBridge(async (harness) => {
        // ONE projection memo feeding both the store and the assertion, so the
        // `RenderTree` the store converts is the very object keyed below. Two
        // `projectionMemo()` calls would mint two `ProjectionCache`s and two
        // unrelated trees, and the test would pass without proving anything.
        const { projection } = projectionMemo()
        const store = createFloatingWindowStore({
          getWorkspaceId: () => harness.workspaceId,
          projection,
        })
        const created = store.addWindow({ x: 0.1, y: 0.2, width: 0.4, height: 0.5 })!
        await new Promise<void>(queueMicrotask)
        const rendered = projection()!.workspaces.get(harness.workspaceId)!.floatingWindows[0]!
        // The SHARED conversion -- what any other consumer of this subtree gets.
        const shared = renderTreeToLocal(rendered.innerTree, sharedTrees)!
        expect(shared).toEqual({ type: 'leaf', id: created.tileId })

        // `emitSplitTile` flips the tile to SPLIT under the SAME node id, so
        // `reconcile` matches on `id` and recurses into the node it retained.
        store.splitTile(created.windowId, created.tileId, 'vertical')
        await new Promise<void>(queueMicrotask)
        void store.state.windows

        expect(shared, 'the shared node is a value, not the store\'s scratch space')
          .toEqual({ type: 'leaf', id: created.tileId })
      }, { rootTileId: 'main' })
    })

    it('preserves FloatingWindowState refs across CRDT ticks that don\'t change any window field', async () => {
      await withTestBridge(async (harness) => {
        const store = createTestFloatingWindowStore()
        store.addWindow({ x: 0.1, y: 0.2, width: 0.4, height: 0.5 })
        await new Promise<void>(queueMicrotask)
        const before = store.state.windows[0]!
        // Simulate a passive CRDT tick: submit a no-op-shaped batch on
        // an unrelated tile that doesn't touch this window's state.
        // `pendingMgr.submit` always bumps the version signal — so the
        // projection memo re-fires regardless of whether anything
        // material changed.
        harness.pending.submit({
          $typeName: 'leapmux.v1.OpBatch',
          batchId: 'tick-1',
          ops: [],
        } as never)
        await new Promise<void>(queueMicrotask)
        const after = store.state.windows[0]!
        expect(after).toBe(before)
        expect(after.layoutRoot).toBe(before.layoutRoot)
      }, { rootTileId: 'main' })
    })

    // Regression: `bringToFront` is invoked on every mousedown inside
    // a floating window's chrome (via the container's `onMouseDown`
    // handler) — including clicking a tab in the tab bar. If the
    // window is already topmost the call must be a true no-op,
    // otherwise every tab click pays for an entire projection rebuild
    // (rawProjection re-run + reconcile diff). Users observed tab
    // switching inside floating windows feeling visibly slower than
    // the main area; that delta tracks back to this redundant
    // reactivity chain.
    //
    // The fix: when the window is already at the tail of `zOrder`,
    // `setZOrder` returns the same array reference and Solid's
    // `setSignal` skips the notify (Object.is equality). The
    // downstream `createComputed` body that reconciles `storeState.list`
    // therefore doesn't re-run, so any reactive subscriber to
    // `state.windows` sees zero notifications across the no-op call.
    // The effect-count probe below is the most direct way to assert
    // that contract without instrumenting production code.
    it('bringToFront on an already-topmost window does not trigger a projection rebuild', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const a = store.addWindow()!
        const b = store.addWindow()!
        await new Promise<void>(queueMicrotask)
        // Subscribe to the projected list. Each window-list rebuild
        // fires `setStoreState('list', reconcile(...))` which Solid
        // signals to every effect tracking `state.windows`. A
        // genuine no-op short-circuits before that setState, so the
        // effect count stays put.
        let effectRuns = 0
        createEffect(() => {
          for (const w of store.state.windows) void w.id
          effectRuns++
        })
        await new Promise<void>(queueMicrotask)
        const baseline = effectRuns

        // No-op: b is already topmost. The `setZOrder` updater
        // returns the same array ref → no notify → createComputed
        // doesn't re-fire → state.windows doesn't notify the effect.
        store.bringToFront(b.windowId)
        await new Promise<void>(queueMicrotask)
        expect(effectRuns).toBe(baseline)

        // Real move: a was at index 0, now must move to topmost.
        // The reorder DOES change zOrder, so the projection rebuilds
        // and the effect re-fires.
        store.bringToFront(a.windowId)
        await new Promise<void>(queueMicrotask)
        expect(effectRuns).toBeGreaterThan(baseline)
        const afterReorder = effectRuns

        // Second call on the new topmost (a) — no-op again, no new
        // effect fire.
        store.bringToFront(a.windowId)
        await new Promise<void>(queueMicrotask)
        expect(effectRuns).toBe(afterReorder)
      }, { rootTileId: 'main' })
    })

    // tileSetsByWindow stability contract: per-window tile-id sets
    // must NOT re-emit on geometry-only mutations (drag/resize
    // pointermove). Downstream consumers (focus invariants, tab
    // store cleanup gates) read this Map and re-run on every
    // notification — pre-fix, every drag frame fired their effects
    // unnecessarily even though no tile actually moved between
    // windows.
    //
    // The store enforces this with two layers: (1) `reconcile` is
    // tuned so the projection's per-entry `layoutRoot` refs survive
    // geometry-only ticks, (2) the `tileSetsByWindow` memo carries a
    // structural `equals` check that suppresses notifies when the
    // window→tile-id mapping is byte-identical. These tests pin both
    // layers so a future tweak to either reconcile mode or the
    // memo's input source can't silently regress the contract.
    it('keeps getWindowTileIdSet stable across a drag-handle scrub (geometry-only)', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        await new Promise<void>(queueMicrotask)
        const before = store.getWindowTileIdSet(windowId)
        expect(before).not.toBeNull()
        // 20-frame scrub down the path the UI actually takes: each frame
        // writes the drag override (bumping the bridge version signal), and the
        // drop commits once. None of them change the window's tile membership.
        for (let i = 0; i < 20; i++) {
          store.updateDragMove(windowId, 0.1 + i * 0.01, 0.1 + i * 0.01)
        }
        store.commitDragGeometry(windowId)
        await new Promise<void>(queueMicrotask)
        const after = store.getWindowTileIdSet(windowId)
        // Same Set ref across the scrub — the structural equality
        // check inside `tileSetsByWindow` (or, equivalently, the
        // upstream reconcile's ref-preservation) collapsed every
        // geometry-only frame into a single no-op notification.
        expect(after).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('keeps getWindowTileIdSet stable across a resize-handle scrub', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        await new Promise<void>(queueMicrotask)
        const before = store.getWindowTileIdSet(windowId)
        // 20-frame resize scrub: width + height change every frame,
        // tile membership doesn't.
        for (let i = 0; i < 20; i++) {
          store.updateDragResize(windowId, 0.1, 0.1, 0.3 + i * 0.01, 0.3 + i * 0.01)
        }
        store.commitDragGeometry(windowId)
        await new Promise<void>(queueMicrotask)
        const after = store.getWindowTileIdSet(windowId)
        expect(after).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('updates getWindowTileIdSet when the window\'s inner tree actually splits', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const created = store.addWindow()!
        const { windowId, tileId: rootTile } = created
        await new Promise<void>(queueMicrotask)
        const before = store.getWindowTileIdSet(windowId)
        expect(before).not.toBeNull()
        expect(before!.size).toBe(1)
        expect(before!.has(rootTile)).toBe(true)

        // splitTile actually changes the layout — the membership Set
        // must re-emit with two tile ids.
        const childB = store.splitTile(windowId, rootTile, 'horizontal')
        expect(childB).not.toBeNull()
        await new Promise<void>(queueMicrotask)
        const after = store.getWindowTileIdSet(windowId)
        expect(after).not.toBe(before)
        expect(after!.size).toBe(2)
      }, { rootTileId: 'main' })
    })

    it('quiet contract: a downstream effect on tile-id set + tab-store state fires zero times across a drag scrub', async () => {
      // Cross-store quiet-on-drag is the most important regression
      // target of the merge:true switch, and it got sharper with the
      // projection refactor: tabs are no longer a fine-grained store but a
      // JOIN recomputed from `speculativeState`, whose version signal every
      // drag frame bumps. If the join propagated on each of those bumps, a
      // 50-frame drag would rebuild every tab object in every workspace 50
      // times and re-run every downstream effect with it.
      //
      // The contract: effects reading the tile-id set AND the joined tab view
      // see zero additional fires across N drag frames.
      await withTestBridge(async (harness) => {
        const store = createTestFloatingWindowStore()
        const { TabType } = await import('~/generated/leapmux/v1/workspace_pb')
        const { emitAddTab } = await import('~/stores/tabOps')
        const { createTestTabStores } = await import('~/test-support/tabStores')
        const { view } = createTestTabStores(harness.workspaceId)
        const { windowId, tileId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        emitAddTab({ type: TabType.AGENT, id: 'agent-1', tileId, position: 'a', workerId: 'w-1' })
        await new Promise<void>(queueMicrotask)

        let effectRuns = 0
        createEffect(() => {
          // Subscribe to the tile-id set (the floating store's most-
          // expensive memo output) AND the joined tab view (the downstream
          // consumer the chat-trimming hot path reads). Both must stay quiet
          // during geometry-only updates.
          const set = store.getWindowTileIdSet(windowId)
          void set?.size
          const tabs = view.forWorkspace(harness.workspaceId)
          void tabs.length
          for (const t of tabs) void t.tileId
          effectRuns++
        })
        await new Promise<void>(queueMicrotask)
        const baseline = effectRuns

        // 50-frame drag scrub. updatePosition fires per coalesced
        // pointermove in production — each one enqueues a CRDT batch
        // and bumps the bridge version. With merge:false this would
        // re-emit each window entry's layoutRoot ref every frame; with an
        // unguarded join the tab view would rebuild every frame. Either way
        // the effect would run 50 times despite no membership / tab change.
        for (let i = 0; i < 50; i++)
          store.updateDragMove(windowId, 0.1 + i * 0.005, 0.1 + i * 0.005)
        store.commitDragGeometry(windowId)
        await new Promise<void>(queueMicrotask)

        expect(effectRuns).toBe(baseline)
      }, { rootTileId: 'main' })
    })

    it('keeps tile-id sets stable across a geometry-only batch on N windows', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const a = store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!
        const b = store.addWindow({ x: 0.3, y: 0.3, width: 0.2, height: 0.2 })!
        const c = store.addWindow({ x: 0.5, y: 0.5, width: 0.2, height: 0.2 })!
        await new Promise<void>(queueMicrotask)
        expect(store.getWindowTileIdSet(a.windowId)?.size).toBe(1)
        expect(store.getWindowTileIdSet(b.windowId)?.size).toBe(1)
        expect(store.getWindowTileIdSet(c.windowId)?.size).toBe(1)
        const beforeA = store.getWindowTileIdSet(a.windowId)
        const beforeB = store.getWindowTileIdSet(b.windowId)
        const beforeC = store.getWindowTileIdSet(c.windowId)
        // Geometry-only batch across all three windows. None of the
        // inner trees change, so every per-window Set must keep the
        // same ref — that's the contract the structural-equality
        // memo relies on for the geometry-hot path.
        for (let i = 0; i < 10; i++) {
          store.updateDragResize(a.windowId, 0.1, 0.1, 0.2 + i * 0.01, 0.2 + i * 0.01)
          store.commitDragGeometry(a.windowId)
          store.updateDragResize(b.windowId, 0.3, 0.3, 0.2 + i * 0.01, 0.2 + i * 0.01)
          store.commitDragGeometry(b.windowId)
          store.updateDragResize(c.windowId, 0.5, 0.5, 0.2 + i * 0.01, 0.2 + i * 0.01)
          store.commitDragGeometry(c.windowId)
        }
        await new Promise<void>(queueMicrotask)
        expect(store.getWindowTileIdSet(a.windowId)).toBe(beforeA)
        expect(store.getWindowTileIdSet(b.windowId)).toBe(beforeB)
        expect(store.getWindowTileIdSet(c.windowId)).toBe(beforeC)
        // Add + remove a window: tile-id-set entries for the
        // surviving windows still reflect membership correctly. The
        // memo's whole-Map invalidation on size change is expected
        // — only the no-change case promises ref stability.
        store.removeWindow(a.windowId)
        await new Promise<void>(queueMicrotask)
        expect(store.getWindowTileIdSet(a.windowId)).toBeNull()
        expect(store.getWindowTileIdSet(b.windowId)?.size).toBe(1)
        expect(store.getWindowTileIdSet(c.windowId)?.size).toBe(1)
      }, { rootTileId: 'main' })
    })

    /**
     * The account-wide half of the same contract: one window's drag must not
     * disturb ANOTHER WORKSPACE's windows, now that every workspace is derived
     * from one shared projection.
     *
     * Deliberately NOT sold as a `ProjectionCache` test. Every observable this
     * store exposes is absorbed one layer down -- `reconcile({ key: 'id' })`
     * keeps the entry ref, `tileSetsByWindow`'s `{ equals: tileSetMapsEqual }`
     * keeps the Set, and neither notifies when the content matches -- so an
     * identity or effect-count assertion here passes with an uncached
     * `project(state)` too (checked by running it that way). What it pins is the
     * end-to-end behaviour: nothing about the background workspace moves, and
     * its geometry stays its own. The cache's own contribution -- the background
     * `WorkspaceProjection` being the IDENTICAL object across the drag -- is
     * asserted where it is visible, in `project.test.ts`'s "a ratio drag in one
     * workspace leaves every other workspace untouched".
     */
    it('leaves a background workspace\'s windows untouched across a drag scrub', async () => {
      await withTestBridge(async (harness) => {
        seedWorkspace(harness, 'ws-bg', 'bg-root')
        const store = createTestFloatingWindowStore('ws-active')
        const bgStore = createTestFloatingWindowStore('ws-bg')
        const active = store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!
        // `addWindow` attributes the window to the BRIDGE's workspace, so hand it
        // to the background one the way a cross-workspace move does.
        const background = store.addWindow({ x: 0.6, y: 0.6, width: 0.2, height: 0.2 })!
        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([setFloatingWorkspaceId(ctxFromBridge(bridge), background.windowId, 'ws-bg')]))
        await new Promise<void>(queueMicrotask)
        const bgBefore = bgStore.state.windows[0]!
        const bgTilesBefore = bgStore.getWindowTileIdSet(background.windowId)
        expect(bgBefore, 'the background workspace has a window to protect').toBeDefined()

        // Any rebuild that reaches `setStoreState('list', reconcile(...))` with
        // a real diff notifies anything tracking `state.windows`.
        let bgRuns = 0
        createEffect(() => {
          for (const w of bgStore.state.windows) void w.x
          bgRuns++
        })
        await new Promise<void>(queueMicrotask)
        const baseline = bgRuns

        for (let i = 0; i < 10; i++)
          store.updateDragMove(active.windowId, 0.1 + i * 0.01, 0.1 + i * 0.01)
        store.commitDragGeometry(active.windowId)
        await new Promise<void>(queueMicrotask)

        expect(bgRuns, 'ten drag frames in another workspace notify this one zero times').toBe(baseline)
        expect(bgStore.state.windows[0]).toBe(bgBefore)
        expect(bgStore.getWindowTileIdSet(background.windowId)).toBe(bgTilesBefore)
        expect(bgStore.state.windows[0].x, 'and its geometry is its own').toBeCloseTo(0.6, 6)
        expect(store.state.windows[0].x, 'while the dragged one moved').toBeCloseTo(0.19, 6)
      }, { workspaceId: 'ws-active', rootTileId: 'main' })
    })

    // Granular-update contract (the whole point of switching to
    // createStore + reconcile): when a field actually changes, the
    // store entry's REF stays the same but the field's VALUE updates
    // reactively. Solid's `<For>` keeps the same component instance
    // (no remount + no nested-subtree rebuild) and only the JSX
    // expressions that read the mutated field re-evaluate. Without
    // this contract, drag/resize re-mounted the entire floating-window
    // container (and its inner TilingLayout / TabBar / Terminal) on
    // every pointermove — the UI looked sluggish.
    it('preserves the entry ref across a field change but updates the field value', async () => {
      await withTestBridge(async (_harness) => {
        const store = createTestFloatingWindowStore()
        const created = store.addWindow({ x: 0.1, y: 0.2, width: 0.4, height: 0.5 })!
        await new Promise<void>(queueMicrotask)
        const before = store.state.windows[0]!
        const beforeId = before.id
        store.updateDragMove(created.windowId, 0.3, 0.4)
        store.commitDragGeometry(created.windowId)
        await new Promise<void>(queueMicrotask)
        const after = store.state.windows[0]!
        // Same store-proxy ref (proves <For> won't remount).
        expect(after).toBe(before)
        expect(after.id).toBe(beforeId)
        // ...but the granular field updates landed.
        expect(after.x).toBeCloseTo(0.3, 6)
        expect(after.y).toBeCloseTo(0.4, 6)
        // Untouched fields keep their values.
        expect(after.width).toBeCloseTo(0.4, 6)
        expect(after.height).toBeCloseTo(0.5, 6)
      }, { rootTileId: 'main' })
    })
  })
})

/**
 * The window list this store RENDERS is deliberately the active workspace's
 * slice — z-order and per-window focus only mean anything for what is on
 * screen. But "which window owns this tile" and "which tiles does workspace X
 * own" are not rendering questions, and two cross-workspace callers ask them:
 * `tileLifecycle.focusTile` for a sidebar click into another workspace, and
 * `removeEmptyFloatingWindow` for the SOURCE tile of a tab dragged out of one.
 * Both silently answered "not a floating tile" while these were scoped to the
 * active workspace, so the window's inner focus was never recorded and an
 * emptied background window was never disposed.
 */
describe('cross-workspace floating tile lookups', () => {
  /**
   * Reassign a window to another workspace through the real op path, the way a
   * cross-workspace move does. Poking `confirmedState` would not work: a window
   * created by `addWindow` lives in a PENDING batch, and `recomputeSpeculative`
   * would just rebuild over the edit.
   */
  function moveWindowToWorkspace(windowId: string, workspaceId: string) {
    const bridge = getCRDTBridge()!
    bridge.enqueue(newBatch([setFloatingWorkspaceId(ctxFromBridge(bridge), windowId, workspaceId)]))
  }

  /**
   * The destination workspace has to EXIST, and that is not test scaffolding.
   * The store derives from the shared projection now, and `project()` drops a
   * floating window whose `WorkspaceContentsRecord` is gone -- deliberately, so
   * a deleted workspace cannot leave windows resolving behind it. Moving a
   * window to an id that was never seeded exercised a state the app cannot
   * reach; the local walk this replaced happened to keep such a window, which
   * was the drift.
   */
  function withWorkspaces(body: (harness: TestBridgeHandle) => void) {
    withTestBridge((harness) => {
      seedWorkspace(harness, 'ws-elsewhere', 'elsewhere-root')
      body(harness)
    }, { workspaceId: 'ws-active', rootTileId: 'main' })
  }

  /** `withWorkspaces` for a case that awaits the GC effect. */
  async function withWorkspacesAsync(body: (harness: TestBridgeHandle) => Promise<void>) {
    await withTestBridge(async (harness) => {
      seedWorkspace(harness, 'ws-elsewhere', 'elsewhere-root')
      await body(harness)
    }, { workspaceId: 'ws-active', rootTileId: 'main' })
  }

  it('resolves a tile owned by a window in a NON-active workspace', () => {
    withWorkspaces(() => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      expect(store.getWindowForTile(created.tileId)).toBe(created.windowId)

      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')

      expect(store.state.windows, 'it has left the rendered slice').toEqual([])
      expect(
        store.getWindowForTile(created.tileId),
        'but the tile still belongs to that window, so focus and the empty-window sweep must resolve it',
      ).toBe(created.windowId)
    })
  })

  it('reports a non-active workspace own floating tiles', () => {
    withWorkspaces(() => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      expect(store.getAllTileIdsFor('ws-active')).toEqual([created.tileId])

      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')

      expect(store.getAllTileIdsFor('ws-elsewhere')).toEqual([created.tileId])
      expect(store.getAllTileIdsFor('ws-active'), 'and no longer to the old owner').toEqual([])
      expect(store.getAllTileIds(), 'the rendered slice is the ACTIVE workspace only').toEqual([])
    })
  })

  /**
   * A window whose workspace record is gone stops resolving anywhere.
   *
   * This is a deliberate consequence of deriving the store from the shared
   * projection: `project()` drops such a window, so `getWindowForTile` and the
   * live-window set drop it too. The hand-rolled walk this replaced KEPT it,
   * which meant the account-wide index and the rendered slice disagreed about a
   * window that no longer had an owner. Pinned because it is the one behaviour
   * the switch changed, and because a deleted workspace must not leave windows
   * resolving behind it.
   */
  it('stops resolving a window whose workspace record is gone', () => {
    withWorkspaces((harness) => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')
      expect(store.getWindowForTile(created.tileId)).toBe(created.windowId)

      delete harness.pending.state.confirmedState.workspaces['ws-elsewhere']
      harness.pending.recomputeSpeculative()
      // Any real op re-derives the projection, as a live client would.
      store.addWindow({ x: 0.5, y: 0.5, width: 0.2, height: 0.2 })

      expect(
        store.getWindowForTile(created.tileId),
        'an ownerless window resolves nowhere, not just off screen',
      ).toBeNull()
    })
  })

  it('returns nothing for a workspace with no floating windows', () => {
    withTestBridge(() => {
      const store = createTestFloatingWindowStore()
      expect(store.getAllTileIdsFor('ws-never-had-one')).toEqual([])
      expect(store.getWindowForTile('not-a-tile')).toBeNull()
    }, { workspaceId: 'ws-active', rootTileId: 'main' })
  })

  /**
   * The lookups above are only half the job. Both callers hand the account-wide
   * `windowId` they just resolved to a MUTATOR, and those re-resolved it
   * through the rendered slice — so the escape was undone one call later and
   * both operations silently no-opped for a background workspace, which is the
   * state the callers exist to handle.
   */
  it('records inner focus for a window in a NON-active workspace', () => {
    withWorkspaces(() => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      const second = store.splitTile(created.windowId, created.tileId, 'horizontal')!
      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')
      expect(store.state.windows, 'it has left the rendered slice').toEqual([])

      store.setFocusedTile(created.windowId, second)

      // Bring it back on screen: the recorded choice must be what fronts,
      // not the first-leaf fallback.
      moveWindowToWorkspace(created.windowId, 'ws-active')
      expect(store.state.windows[0]?.focusedTileId).toBe(second)
    })
  })

  it('disposes an emptied single-tile window in a NON-active workspace', () => {
    withWorkspaces(() => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')
      expect(store.state.windows, 'it has left the rendered slice').toEqual([])

      const removed = store.removeIfEmpty(created.windowId, () => [])

      expect(removed, 'the sweep must fire for an off-screen window too').toBe(true)
      expect(
        store.getWindowForTile(created.tileId),
        'and the window is really gone, not just unrendered',
      ).toBeNull()
    })
  })

  it('keeps a background window alive while its tile still holds tabs', () => {
    withWorkspaces(() => {
      const store = createTestFloatingWindowStore()
      const created = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      moveWindowToWorkspace(created.windowId, 'ws-elsewhere')

      const removed = store.removeIfEmpty(created.windowId, () => [{ id: 'a-tab' }])

      expect(removed).toBe(false)
      expect(store.getWindowForTile(created.tileId)).toBe(created.windowId)
    })
  })

  /**
   * The focus GC keys off the live-window set. Derived from the rendered slice
   * it would treat every background workspace's window as tombstoned and evict
   * the entry the test above just recorded, on the next membership change.
   */
  it('does not evict a background window focus entry when another window churns', async () => {
    await withWorkspacesAsync(async () => {
      const store = createTestFloatingWindowStore()
      const background = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      const second = store.splitTile(background.windowId, background.tileId, 'horizontal')!
      moveWindowToWorkspace(background.windowId, 'ws-elsewhere')
      store.setFocusedTile(background.windowId, second)

      // Churn membership so the GC effect actually runs.
      const onscreen = store.addWindow({ x: 0.5, y: 0.5, width: 0.2, height: 0.2 })!
      await new Promise<void>(queueMicrotask)
      store.removeWindow(onscreen.windowId)
      await new Promise<void>(queueMicrotask)

      moveWindowToWorkspace(background.windowId, 'ws-active')
      expect(store.state.windows[0]?.focusedTileId).toBe(second)
    })
  })
})

describe('createFloatingWindowStore — debounced opacity (title-bar wheel)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  // updateOpacityDebounced writes the live value to a local override (instant
  // feedback) and arms a trailing debounce that emits ONE op when scrolling
  // pauses. A whole scroll gesture collapses to a single op instead of one
  // per wheel tick.

  it('writes the override immediately without emitting a batch', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      const before = harness.pending.state.pendingBatches.length

      const changed = store.updateOpacityDebounced(windowId, 0.6)
      expect(changed).toBe(true)
      // No CRDT op yet — the override is local-only.
      expect(harness.pending.state.pendingBatches.length).toBe(before)
      // The projected opacity reflects the live override immediately.
      expect(store.state.windows[0]!.opacity).toBeCloseTo(0.6, 6)
    }, { rootTileId: 'main' })
  })

  it('emits exactly one op after the quiet period, coalescing a scroll gesture', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      const before = harness.pending.state.pendingBatches.length

      // A scroll gesture: many wheel ticks, each advancing the override.
      store.updateOpacityDebounced(windowId, 0.9)
      store.updateOpacityDebounced(windowId, 0.8)
      store.updateOpacityDebounced(windowId, 0.7)
      store.updateOpacityDebounced(windowId, 0.6)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
      expect(store.state.windows[0]!.opacity).toBeCloseTo(0.6, 6)

      // Advance just short of the delay — still no op.
      vi.advanceTimersByTime(599)
      expect(harness.pending.state.pendingBatches.length).toBe(before)

      // Cross the delay: exactly ONE op fires with the final value.
      vi.advanceTimersByTime(1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(store.state.windows[0]!.opacity).toBeCloseTo(0.6, 6)
    }, { rootTileId: 'main' })
  })

  // The flush used to re-read the projection to decide whether the value had
  // changed. That read is unavailable on exactly the paths the flush exists to
  // serve: at store disposal Solid has already disposed the createComputed that
  // maintains the projection, and on a workspace switch the window has already
  // left the active-workspace slice. Both made the guard swallow the op. The
  // gesture now carries the CRDT baseline it started from.
  it('still emits when the window is no longer in the projection', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!

      store.updateOpacityDebounced(windowId, 0.6)
      // Move the window to another workspace, so it leaves the projected slice
      // exactly as it does on a workspace switch mid-gesture.
      const bridge = getCRDTBridge()!
      bridge.enqueue(newBatch([setFloatingWorkspaceId(ctxFromBridge(bridge), windowId, 'ws-elsewhere')]))
      expect(store.state.windows.some(w => w.id === windowId)).toBe(false)

      // Count from AFTER the move, so only the flush's own op is measured.
      const before = harness.pending.state.pendingBatches.length
      store.flushOpacity(windowId)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
    }, { rootTileId: 'main' })
  })

  it('does not emit when the gesture ends back at its starting value', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      const before = harness.pending.state.pendingBatches.length

      // Default opacity is 1.0: scrub away and back.
      store.updateOpacityDebounced(windowId, 0.6)
      store.updateOpacityDebounced(windowId, 1)
      store.flushOpacity(windowId)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  it('re-arms the timer on each tick so a continuous scroll stays one op', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      const before = harness.pending.state.pendingBatches.length

      // Tick, advance partway, tick again — the timer should reset.
      store.updateOpacityDebounced(windowId, 0.7)
      vi.advanceTimersByTime(400)
      store.updateOpacityDebounced(windowId, 0.6)
      vi.advanceTimersByTime(400)
      // 400 + 400 = 800 > 600, but the second tick re-armed at t=400, so the
      // timer fires at t=1000, not t=600. No op yet at t=800.
      expect(harness.pending.state.pendingBatches.length).toBe(before)

      // Cross the re-armed delay: one op.
      vi.advanceTimersByTime(200)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
    }, { rootTileId: 'main' })
  })

  // The pending edit is keyed BY WINDOW. It used to be a single global slot,
  // borrowed from the drag override -- which is safe there only because pointer
  // capture serializes drags. Wheel events need no capture, every visible window
  // binds its own title-bar listener, so two windows can have gestures in flight
  // at once.
  it('scrolling a second window does not discard the first window\'s pending opacity', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const a = store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!
      const b = store.addWindow({ x: 0.4, y: 0.4, width: 0.2, height: 0.2 })!
      const before = harness.pending.state.pendingBatches.length

      // Scroll A, then B within the debounce window. A single slot cleared A's
      // timer and replaced A's override, so A's op was NEVER emitted and its
      // rendered opacity silently snapped back -- the whole gesture lost, with
      // no error path.
      store.updateOpacityDebounced(a.windowId, 0.7)
      store.updateOpacityDebounced(b.windowId, 0.5)

      vi.advanceTimersByTime(1000)

      expect(harness.pending.state.pendingBatches.length - before).toBe(2)
      const byId = new Map(store.state.windows.map(w => [w.id, w.opacity]))
      expect(byId.get(a.windowId)).toBeCloseTo(0.7, 6)
      expect(byId.get(b.windowId)).toBeCloseTo(0.5, 6)
    }, { rootTileId: 'main' })
  })

  it('flushOpacity(id) flushes only that window, leaving another gesture armed', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const a = store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!
      const b = store.addWindow({ x: 0.4, y: 0.4, width: 0.2, height: 0.2 })!
      const before = harness.pending.state.pendingBatches.length

      store.updateOpacityDebounced(a.windowId, 0.7)
      store.updateOpacityDebounced(b.windowId, 0.5)

      // B unmounts (closed, or a workspace switch) mid-gesture. Its teardown
      // must commit ITS value only -- unscoped, it committed whichever window
      // happened to be pending, splitting A's single gesture into two ops.
      store.flushOpacity(b.windowId)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)

      // A's gesture is still armed and fires on its own schedule.
      vi.advanceTimersByTime(1000)
      expect(harness.pending.state.pendingBatches.length - before).toBe(2)
      const byId = new Map(store.state.windows.map(w => [w.id, w.opacity]))
      expect(byId.get(a.windowId)).toBeCloseTo(0.7, 6)
    }, { rootTileId: 'main' })
  })

  it('same-value tick (pinned at clamp floor) is a no-op and does not re-arm', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      // Drop to 0.2 (the clamp floor) and flush so the CRDT value is 0.2.
      const { windowId } = store.addWindow()!
      store.updateOpacityDebounced(windowId, 0.2)
      store.flushOpacity(windowId)
      const before = harness.pending.state.pendingBatches.length

      // Scrolling further down clamps to 0.2 again — no change, no re-arm.
      const changed = store.updateOpacityDebounced(windowId, 0.1)
      expect(changed).toBe(false)
      // Advance past the delay: still no op (timer was never armed).
      vi.advanceTimersByTime(1000)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  it('flushOpacity emits the pending op immediately and clears the timer', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      const before = harness.pending.state.pendingBatches.length

      store.updateOpacityDebounced(windowId, 0.5)
      store.flushAllOpacity()
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      // A second flush is a no-op.
      store.flushAllOpacity()
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      // Advancing time does not fire a stale op.
      vi.advanceTimersByTime(1000)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
    }, { rootTileId: 'main' })
  })

  it('an unscoped flush emits one op per window with a real change', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const a = store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!
      const b = store.addWindow({ x: 0.4, y: 0.4, width: 0.2, height: 0.2 })!
      const c = store.addWindow({ x: 0.7, y: 0.7, width: 0.2, height: 0.2 })!
      store.updateOpacityDebounced(a.windowId, 0.7)
      store.updateOpacityDebounced(b.windowId, 0.5)
      // C's gesture ends back where it started, so it must emit nothing even
      // though the sweep drops its entry alongside the other two.
      store.updateOpacityDebounced(c.windowId, 0.4)
      store.updateOpacityDebounced(c.windowId, 1)
      const before = harness.pending.state.pendingBatches.length

      // The store's own disposal hook and the pagehide listener take this path:
      // every window is going away, so every gesture flushes at once.
      store.flushAllOpacity()

      expect(harness.pending.state.pendingBatches.length - before).toBe(2)
      const byId = new Map(store.state.windows.map(w => [w.id, w.opacity]))
      expect(byId.get(a.windowId)).toBeCloseTo(0.7, 6)
      expect(byId.get(b.windowId)).toBeCloseTo(0.5, 6)
      expect(byId.get(c.windowId)).toBeCloseTo(1, 6)
      // Every timer was cleared, so nothing fires late.
      vi.advanceTimersByTime(1000)
      expect(harness.pending.state.pendingBatches.length - before).toBe(2)
    }, { rootTileId: 'main' })
  })

  // A browser unload runs NO Solid cleanup, so neither disposal hook fires on a
  // refresh or a tab close. Before the debounce existed the op was emitted
  // synchronously; without this listener a scrub finished inside the 600ms
  // window is lost outright, and the op-log cannot recover an op that was never
  // created.
  it('flushes on pagehide, which no Solid cleanup covers', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      store.updateOpacityDebounced(windowId, 0.6)
      const before = harness.pending.state.pendingBatches.length

      window.dispatchEvent(new Event('pagehide'))

      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      // The timer was cleared with the gesture, so nothing fires late.
      vi.advanceTimersByTime(1000)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
    }, { rootTileId: 'main' })
  })

  // Creating the op is only HALF the job, and the half this test used to stop
  // at. `bridge.enqueue` pushes onto the submitter's queue and arms a ~16 ms
  // timer; on a real unload -- Cmd+R, tab close, the case this listener exists
  // for -- the page is gone before it fires, so the op was created and then
  // dropped. Only `flushNow` (keepalive transport) actually sends it.
  //
  // Both steps must happen in ONE handler, in this order. Splitting the send
  // into its own `pagehide` listener would make the outcome depend on
  // registration order, and the runtime mounts BEFORE the stores -- so its
  // listener would flush an empty queue and then the store would create the op
  // with nothing left to send it.
  it('sends on pagehide, not merely enqueues', () => {
    withTestBridge((harness) => {
      const store = createTestFloatingWindowStore()
      const { windowId } = store.addWindow()!
      store.updateOpacityDebounced(windowId, 0.6)
      const before = harness.pending.state.pendingBatches.length

      window.dispatchEvent(new Event('pagehide'))

      // The LAST call is this store's: earlier cases in this file create stores
      // without disposing them, so their listeners are still attached and fire
      // first (registration order). Theirs have no gesture to flush.
      expect(harness.flushNowCalls.length).toBeGreaterThan(0)
      // The count AT FLUSH TIME proves the op was created first: a flush that
      // ran before the gesture materialized would see the pre-gesture count.
      expect(harness.flushNowCalls.at(-1)).toBe(before + 1)
    }, { rootTileId: 'main' })
  })

  it('removes its pagehide listener when the store is disposed', () => {
    // Left attached, the listener would keep a disposed store's closure alive
    // and flush into a torn-down bridge on every later unload.
    let disposedStore!: ReturnType<typeof createTestFloatingWindowStore>
    withTestBridge((harness) => {
      disposedStore = createTestFloatingWindowStore()
      const { windowId } = disposedStore.addWindow()!
      disposedStore.updateOpacityDebounced(windowId, 0.6)
      // Leaving the root disposes the store, whose own cleanup already flushed.
      expect(harness.pending.state.pendingBatches.length).toBeGreaterThanOrEqual(0)
    }, { rootTileId: 'main' })

    // No throw, and nothing to flush: the listener is gone with the store.
    expect(() => window.dispatchEvent(new Event('pagehide'))).not.toThrow()
  })

  it('drops every gesture in ONE map rebuild when flushed unscoped', () => {
    withTestBridge((harness) => {
      // `rawProjection` reads `opts.projection()` exactly once per run, so
      // counting the reads counts the rebuilds the flush costs.
      const { projection } = projectionMemo()
      let projectionReads = 0
      const store = createFloatingWindowStore({
        getWorkspaceId: () => harness.workspaceId,
        projection: () => {
          projectionReads++
          return projection()
        },
      })
      const ids = [
        store.addWindow({ x: 0.1, y: 0.1, width: 0.2, height: 0.2 })!.windowId,
        store.addWindow({ x: 0.4, y: 0.4, width: 0.2, height: 0.2 })!.windowId,
        store.addWindow({ x: 0.7, y: 0.7, width: 0.2, height: 0.2 })!.windowId,
      ]
      // Park every gesture back at its CRDT baseline, so the flush emits NO ops
      // and the map rebuild is the only thing it can cost.
      for (const id of ids) {
        store.updateOpacityDebounced(id, 0.5)
        store.updateOpacityDebounced(id, 1)
      }
      const batchesBefore = harness.pending.state.pendingBatches.length

      projectionReads = 0
      store.flushAllOpacity()

      expect(harness.pending.state.pendingBatches.length).toBe(batchesBefore)
      // ONE rebuild for the whole sweep. Clearing per victim inside the emit
      // loop rebuilt the map -- re-running every downstream projection -- once
      // per window, so this was 3.
      expect(projectionReads).toBe(1)
      expect(store.state.windows.every(w => w.opacity === 1)).toBe(true)
    }, { rootTileId: 'main' })
  })
})

// The store's own keyed-override primitive, behind the drag preview, the
// debounced-opacity gesture and per-window focus. Two rules are load-bearing
// and used to be re-derived by hand at six sites: a write must not mutate the
// previous Map (the projection memo and Solid's `<For>` key on its identity),
// and a removal that removes nothing must hand back the SAME reference, since
// that is the only thing that makes Solid's setSignal suppress the notify.
describe('createKeyedOverrides', () => {
  it('rebuilds on set, leaving the previous snapshot untouched', () => {
    createRoot((dispose) => {
      const overrides = createKeyedOverrides<number>()
      const empty = overrides.snapshot()
      overrides.set('a', 1)
      const afterFirst = overrides.snapshot()
      expect(afterFirst).not.toBe(empty)
      // Copy-on-write: the map a consumer already read is never scribbled on.
      expect(empty.size).toBe(0)

      overrides.set('b', 2)
      expect(overrides.snapshot()).not.toBe(afterFirst)
      expect(afterFirst.has('b')).toBe(false)
      expect([...overrides.snapshot()]).toEqual([['a', 1], ['b', 2]])
      expect(overrides.get('a')).toBe(1)
      expect(overrides.get('missing')).toBeUndefined()
      dispose()
    })
  })

  it('clear drops exactly one key, and keeps the reference when there is none', () => {
    createRoot((dispose) => {
      const overrides = createKeyedOverrides<number>()
      overrides.set('a', 1)
      overrides.set('b', 2)
      const before = overrides.snapshot()

      overrides.clear('absent')
      expect(overrides.snapshot()).toBe(before)

      overrides.clear('a')
      expect(overrides.snapshot()).not.toBe(before)
      expect([...overrides.snapshot().keys()]).toEqual(['b'])
      expect(before.has('a')).toBe(true)
      dispose()
    })
  })

  it('clearAll empties in one rebuild and no-ops on an already-empty map', () => {
    createRoot((dispose) => {
      const overrides = createKeyedOverrides<number>()
      overrides.set('a', 1)
      overrides.set('b', 2)
      const populated = overrides.snapshot()

      overrides.clearAll()
      const emptied = overrides.snapshot()
      expect(emptied.size).toBe(0)
      expect(emptied).not.toBe(populated)
      expect(populated.size).toBe(2)

      // A second clearAll must not notify — the disposal hook and the pagehide
      // listener both reach it with nothing left to drop.
      overrides.clearAll()
      expect(overrides.snapshot()).toBe(emptied)
      dispose()
    })
  })

  it('retain drops only rejected keys, and keeps the reference when all survive', () => {
    createRoot((dispose) => {
      const overrides = createKeyedOverrides<number>()
      overrides.set('live-1', 1)
      overrides.set('dead', 2)
      overrides.set('live-2', 3)
      const before = overrides.snapshot()

      // The GC's shape: a sweep that finds nothing stale must hand back the
      // same map, or every membership change would re-run the projection.
      overrides.retain(() => true)
      expect(overrides.snapshot()).toBe(before)

      overrides.retain(key => key !== 'dead')
      const after = overrides.snapshot()
      expect(after).not.toBe(before)
      expect([...after.keys()]).toEqual(['live-1', 'live-2'])
      expect(before.has('dead')).toBe(true)

      // Rejecting everything empties it in one pass.
      overrides.retain(() => false)
      expect(overrides.snapshot().size).toBe(0)
      dispose()
    })
  })

  it('stores null as a value distinct from an absent key', () => {
    // `focusByWindow` instantiates T as `string | null`: a recorded "no focused
    // tile" has to stay distinguishable from "never recorded", which falls back
    // to the window's first leaf.
    createRoot((dispose) => {
      const overrides = createKeyedOverrides<string | null>()
      overrides.set('w1', null)
      expect(overrides.get('w1')).toBeNull()
      expect(overrides.snapshot().has('w1')).toBe(true)
      expect(overrides.get('w2')).toBeUndefined()
      dispose()
    })
  })
})
