import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { setCRDTBridge } from '~/lib/crdt'
import { createFloatingWindowStore, MIN_WINDOW_DIMENSION } from '~/stores/floatingWindow.store'
import { withTestBridge } from '../helpers/crdtBridge'

/**
 * createFloatingWindowStore is projection-driven: the window list and
 * inner trees derive from the CRDT speculativeState; mutators emit op
 * batches via the bridge. These tests verify both halves — projected
 * state after a mutation, and the shape of the op batch enqueued.
 */
describe('createFloatingWindowStore (projection-driven)', () => {
  it('starts with no projected windows when none exist in CRDT', () => {
    withTestBridge((_harness) => {
      const store = createFloatingWindowStore()
      expect(store.state.windows).toEqual([])
    }, { rootTileId: 'main' })
  })

  it('addWindow emits a single 8-op creation batch and adds the projected window', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
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
      const store = createFloatingWindowStore()
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
      const store = createFloatingWindowStore()
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
      const store = createFloatingWindowStore()
      const before = harness.pending.state.pendingBatches.length
      store.removeWindow('does-not-exist')
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  it('updatePosition emits a 2-op batch (x + y) and reflects in the projection', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
      const before = harness.pending.state.pendingBatches.length

      store.updatePosition(windowId, 0.5, 0.6)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(2)
      expect(store.state.windows[0]!.x).toBeCloseTo(0.5, 6)
      expect(store.state.windows[0]!.y).toBeCloseTo(0.6, 6)
    }, { rootTileId: 'main' })
  })

  it('updatePosition is a no-op when the coordinates haven’t changed', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.2, width: 0.3, height: 0.3 })!
      const before = harness.pending.state.pendingBatches.length

      store.updatePosition(windowId, 0.1, 0.2)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  it('updateGeometry emits a 4-op batch and clamps below MIN_WINDOW_DIMENSION', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
      const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.4, height: 0.4 })!
      const before = harness.pending.state.pendingBatches.length

      // Pass a width below the floor; store must clamp before emitting.
      store.updateGeometry(windowId, 0.2, 0.3, 0.001, 0.7)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(4)
      expect(store.state.windows[0]!.width).toBe(MIN_WINDOW_DIMENSION)
      expect(store.state.windows[0]!.height).toBeCloseTo(0.7, 6)
    }, { rootTileId: 'main' })
  })

  it('updateOpacity clamps the value into [0.2, 1.0] and emits a single-op batch', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
      const { windowId } = store.addWindow()!

      // Start by dropping below 1.0 so the next bump shows the clamp
      // change (default 1.0 == clamp(5) == 1.0, which would no-op).
      expect(store.updateOpacity(windowId, 0.6)).toBe(true)
      const before = harness.pending.state.pendingBatches.length

      // Out-of-range high → clamped to 1.0.
      const changed = store.updateOpacity(windowId, 5)
      expect(changed).toBe(true)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(lastBatch?.ops.length).toBe(1)
      expect(store.state.windows[0]!.opacity).toBe(1)

      // Out-of-range low (zero / negative) → clamped to 0.2.
      expect(store.updateOpacity(windowId, -10)).toBe(true)
      expect(store.state.windows[0]!.opacity).toBe(0.2)
    }, { rootTileId: 'main' })
  })

  it('updateOpacity at the same clamped value is a no-op', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
      const { windowId } = store.addWindow()!
      // Window default opacity is 1.0; resetting to 1.0 is a no-op.
      const before = harness.pending.state.pendingBatches.length
      const changed = store.updateOpacity(windowId, 1)
      expect(changed).toBe(false)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    }, { rootTileId: 'main' })
  })

  it('bringToFront reorders the projection without emitting a batch (z-order is local)', () => {
    withTestBridge((harness) => {
      const store = createFloatingWindowStore()
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
      const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
    it('preserves FloatingWindowState refs across CRDT ticks that don\'t change any window field', async () => {
      await withTestBridge(async (harness) => {
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
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
        const store = createFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        await new Promise<void>(queueMicrotask)
        const before = store.getWindowTileIdSet(windowId)
        expect(before).not.toBeNull()
        // 20-frame scrub mimicking a real drag: 20 updatePosition
        // calls, each bumping the bridge version signal. None of them
        // change the window's tile membership.
        for (let i = 0; i < 20; i++) {
          store.updatePosition(windowId, 0.1 + i * 0.01, 0.1 + i * 0.01)
        }
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
        const store = createFloatingWindowStore()
        const { windowId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        await new Promise<void>(queueMicrotask)
        const before = store.getWindowTileIdSet(windowId)
        // 20-frame resize scrub: width + height change every frame,
        // tile membership doesn't.
        for (let i = 0; i < 20; i++) {
          store.updateGeometry(windowId, 0.1, 0.1, 0.3 + i * 0.01, 0.3 + i * 0.01)
        }
        await new Promise<void>(queueMicrotask)
        const after = store.getWindowTileIdSet(windowId)
        expect(after).toBe(before)
      }, { rootTileId: 'main' })
    })

    it('updates getWindowTileIdSet when the window\'s inner tree actually splits', async () => {
      await withTestBridge(async (_harness) => {
        const store = createFloatingWindowStore()
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
      // target of the merge:true switch. Production effects (chat-
      // trimming, focus invariant) typically track both
      // `tabStore.state` *and* `floatingWindowStore.getWindowTileIdSet
      // (windowId)`; a drag pointermove fires CRDT batches that bump
      // the bridge version signal, but no field these effects care
      // about actually changes. The contract: effects subscribed to
      // those reads see zero additional fires across N drag frames.
      await withTestBridge(async (_harness) => {
        const store = createFloatingWindowStore()
        const { createTabStore } = await import('~/stores/tab.store')
        const { TabType } = await import('~/generated/leapmux/v1/workspace_pb')
        const tabStore = createTabStore()
        const { windowId, tileId } = store.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        tabStore.addTab({ type: TabType.AGENT, id: 'agent-1', tileId, workerId: 'w-1' })
        await new Promise<void>(queueMicrotask)

        let effectRuns = 0
        createEffect(() => {
          // Subscribe to the tile-id set (the floating store's most-
          // expensive memo output) AND tabStore.state.tabs (the
          // downstream consumer the chat-trimming hot path reads).
          // Both must stay quiet during geometry-only updates.
          const set = store.getWindowTileIdSet(windowId)
          void set?.size
          void tabStore.state.tabs.length
          for (const t of tabStore.state.tabs) void t.tileId
          effectRuns++
        })
        await new Promise<void>(queueMicrotask)
        const baseline = effectRuns

        // 50-frame drag scrub. updatePosition fires per coalesced
        // pointermove in production — each one enqueues a CRDT batch
        // and bumps the bridge version. With merge:false this would
        // re-emit each window entry's layoutRoot ref every frame and
        // the effect would re-run 50 times despite no membership /
        // tab-store change.
        for (let i = 0; i < 50; i++)
          store.updatePosition(windowId, 0.1 + i * 0.005, 0.1 + i * 0.005)
        await new Promise<void>(queueMicrotask)

        expect(effectRuns).toBe(baseline)
      }, { rootTileId: 'main' })
    })

    it('keeps tile-id sets stable across a geometry-only batch on N windows', async () => {
      await withTestBridge(async (_harness) => {
        const store = createFloatingWindowStore()
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
          store.updateGeometry(a.windowId, 0.1, 0.1, 0.2 + i * 0.01, 0.2 + i * 0.01)
          store.updateGeometry(b.windowId, 0.3, 0.3, 0.2 + i * 0.01, 0.2 + i * 0.01)
          store.updateGeometry(c.windowId, 0.5, 0.5, 0.2 + i * 0.01, 0.2 + i * 0.01)
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
        const store = createFloatingWindowStore()
        const created = store.addWindow({ x: 0.1, y: 0.2, width: 0.4, height: 0.5 })!
        await new Promise<void>(queueMicrotask)
        const before = store.state.windows[0]!
        const beforeId = before.id
        store.updatePosition(created.windowId, 0.3, 0.4)
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
