import type { TabMetadataStore } from './tabMetadata.store'
import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { Projection } from '~/lib/crdt'
import type { createTestLayoutStore } from '~/test-support/tabStores'

/// <reference types="vitest/globals" />
import { create } from '@bufbuild/protobuf'
import { createEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { NodeKind } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { EntityRemovedSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ctxFromBridge, getCRDTBridge, hlcIsZero, newBatch, setNodeKind, setNodeParentId, setNodePosition, setTabTileId, setTabWorkerId, tombstoneNode, tombstoneTab } from '~/lib/crdt'
import { installTestBridge, seedWorkspace, withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores, projectionMemo } from '~/test-support/tabStores'
import { createTabMetadataStore, liveTabIds } from './tabMetadata.store'
import { emitAddTab } from './tabOps'
import { createTabView } from './tabView'

/**
 * What the join buys, stated as behaviour.
 *
 * Two of these were impossible before: the tab store held only the active
 * workspace and every other workspace was a snapshot refreshed on switch. A
 * remote change to an inactive workspace reached nothing, and switching
 * workspaces cleared and refetched.
 */

function mountView() {
  const metadata = createTabMetadataStore()
  // `projectionMemo` rather than a local copy of it: it holds the state accessor's
  // load-bearing `equals: false` AND the `ProjectionCache` in the one place the
  // shell wires them, and `placedTabs`' short-circuit only exists because the
  // cache keeps `renderedTabs` identity-stable. A hand-rolled projection here
  // would be a different reactive graph than the app runs.
  const { state, projection } = projectionMemo()
  const view = createTabView({ projection, state, metadata })
  return { view, metadata }
}

describe('tabView', () => {
  it('renders tabs for a workspace that is not the active one', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        seedWorkspace(harness, 'ws-other', 'other-root')
        const { view } = mountView()

        emitAddTab({ type: TabType.AGENT, id: 'a-here', tileId: harness.rootTileId, position: 'M' })
        emitAddTab({ type: TabType.AGENT, id: 'a-there', tileId: 'other-root', position: 'M' })

        // No notion of "active" anywhere in the view -- both answer.
        expect(view.forWorkspace(harness.workspaceId).map(t => t.id)).toEqual(['a-here'])
        expect(view.forWorkspace('ws-other').map(t => t.id)).toEqual(['a-there'])
        dispose()
      })
    })
  })

  it('reflects a change to a workspace nobody is looking at', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        seedWorkspace(harness, 'ws-other', 'other-root')
        const { view } = mountView()
        emitAddTab({ type: TabType.AGENT, id: 'a-there', tileId: 'other-root', position: 'M' })
        expect(view.forWorkspace('ws-other')).toHaveLength(1)

        // A remote client moves that tab into the OTHER workspace. Under the
        // old split this reached a frozen snapshot and changed nothing until
        // the user switched in.
        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([
          setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a-there', harness.rootTileId),
        ]))

        expect(view.forWorkspace('ws-other')).toHaveLength(0)
        expect(view.forWorkspace(harness.workspaceId).map(t => t.id)).toEqual(['a-there'])
        dispose()
      })
    })
  })

  it('carries worker metadata that the CRDT does not have', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        const { view, metadata } = mountView()
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M', workerId: 'wkr-1' })
        metadata.patch('a1', {
          title: 'My Agent',
          gitToplevel: '/repo',
          supportsSteering: true,
          rootAgentId: 'root-1',
        })

        const tab = view.getAgentTab('a1')!
        // Placement from the projection...
        expect(tab.tileId).toBe(harness.rootTileId)
        expect(tab.workerId).toBe('wkr-1')
        expect(tab.workspaceId).toBe(harness.workspaceId)
        // ...metadata from the store, joined on tab id.
        expect(tab.title).toBe('My Agent')
        expect(tab.gitToplevel).toBe('/repo')
        expect(tab.supportsSteering).toBe(true)
        expect(tab.rootAgentId).toBe('root-1')
        dispose()
      })
    })
  })

  // A tab whose tile is not in the projection yet must stay put rather than
  // blink out. The hub now orders a split's frames so this cannot happen for
  // the routine case, but a tile tombstoned by another client mid-flight, or
  // frames crossing different sockets, still produce it.
  it('holds a tab in place while its new tile has not materialized', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        const { view, metadata } = mountView()
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
        metadata.patch('a1', { title: 'My Agent' })
        expect(view.forWorkspace(harness.workspaceId)).toHaveLength(1)

        // The Batch frame lands; the cell node's EntityMaterialized frame has not.
        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([
          setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'cell-not-yet-materialized'),
        ]))

        const tabs = view.forWorkspace(harness.workspaceId)
        expect(tabs).toHaveLength(1)
        // Held at its last resolved tile, with its metadata intact.
        expect(tabs[0].tileId).toBe(harness.rootTileId)
        expect(tabs[0].title).toBe('My Agent')
        dispose()
      })
    })
  })

  // Control for the case above: a tab that genuinely leaves must not be held
  // forever by the placement memory.
  it('drops a tombstoned tab rather than holding it', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        const { view } = mountView()
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
        expect(view.forWorkspace(harness.workspaceId)).toHaveLength(1)

        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([
          // Point it at a dead node AND tombstone it: the placement memory must
          // not resurrect it.
          setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'gone'),
        ]))
        bridge.enqueue(newBatch([tombstoneTab(ctxFromBridge(bridge), TabType.AGENT, 'a1')]))

        expect(view.forWorkspace(harness.workspaceId)).toHaveLength(0)
        dispose()
      })
    })
  })

  /**
   * The join must not propagate on ticks that changed nothing about tabs.
   *
   * `state` notifies on EVERY CRDT tick by necessity (PendingOpsManager mutates
   * in place), and a tile drag fires one batch per coalesced pointermove. The
   * old tab store absorbed this for free by being a fine-grained store; the
   * join needs `placedTabs`' structural `equals`, or every drag frame hands
   * `assembled` a fresh array whose own output array is then fresh too, so
   * `byWorkspace` re-groups and re-sorts every tab in the account and
   * `byKey` / `byTile` rebuild behind it.
   */
  describe('propagation', () => {
    const flush = () => new Promise<void>(queueMicrotask)

    it('does not re-run consumers on a tick that leaves every placement unchanged', async () => {
      await withTestBridge(async (harness) => {
        const dispose = createRoot((d) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })

          let runs = 0
          createEffect(() => {
            void view.forWorkspace(harness.workspaceId).length
            runs++
          })
          return { d, view, runs: () => runs }
        })
        await flush()
        const baseline = dispose.runs()
        expect(baseline, 'the effect must have run at least once').toBeGreaterThan(0)

        // 20 ticks that touch no tab. Submitting a batch is what bumps the
        // bridge version signal, exactly as a geometry-only drag frame does;
        // `recomputeSpeculative` alone would not notify and the test would
        // pass without proving anything.
        const bridge = getCRDTBridge()!
        for (let i = 0; i < 20; i++)
          bridge.enqueue(newBatch([]))
        await flush()

        expect(dispose.runs()).toBe(baseline)
        dispose.d()
      })
    })

    it('still propagates a real placement change', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })

          let runs = 0
          createEffect(() => {
            void view.forWorkspace(harness.workspaceId).map(t => t.position).join()
            runs++
          })
          return { d, view, runs: () => runs }
        })
        await flush()
        const baseline = h.runs()

        emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'b' })
        await flush()

        expect(h.runs()).toBeGreaterThan(baseline)
        expect(h.view.forWorkspace(harness.workspaceId)).toHaveLength(2)
        h.d()
      })
    })

    /**
     * Consumers iterate with `<For>`, which keys by item IDENTITY. An
     * unchanged tab must therefore come back as the SAME object, or every
     * recompute tears down and re-creates every row: DOM churn on each
     * metadata write, and a detached element under anything holding a
     * reference — a drag in flight, a `boundingBox()` read, a focused input.
     */
    it('hands back the same object for a tab that did not change', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view, metadata } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'b' })
          return { d, view, metadata }
        })
        await flush()
        const before = h.view.forWorkspace(harness.workspaceId)
        expect(before.map(t => t.id)).toEqual(['a1', 'a2'])

        // Touch only a2; a1 must survive with its identity intact.
        h.metadata.patch('a2', { title: 'Renamed' })
        await flush()

        const after = h.view.forWorkspace(harness.workspaceId)
        expect(after[1].title, 'the memo really did re-run').toBe('Renamed')
        expect(after[0], 'the untouched tab is the same object').toBe(before[0])
        h.d()
      })
    })

    it('keeps identity across a tick that changes nothing at all', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          return { d, view }
        })
        await flush()
        const before = h.view.forWorkspace(harness.workspaceId)[0]

        const bridge = getCRDTBridge()!
        for (let i = 0; i < 5; i++)
          bridge.enqueue(newBatch([]))
        await flush()

        expect(h.view.forWorkspace(harness.workspaceId)[0]).toBe(before)
        h.d()
      })
    })

    /**
     * The commit echo, end to end.
     *
     * An LWW write storing the value already there, under a newer HLC, replaces
     * the register object, so `ProjectionCache.tab` misses on its INPUTS and
     * rebuilds the row. Two layers then have to agree that nothing happened: the
     * cache value-compares the rebuilt row and keeps the previous one, and
     * `placedTabs`' `samePlacements` would still absorb a fresh array if it did
     * not. This asserts the outcome both exist for -- had either let it through,
     * every per-tab computation would be disposed and rebuilt and `<For>` would
     * tear down the tab strip and the sidebar tree.
     */
    it('does not re-key a tab when a same-value register rewrite lands', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a', workerId: 'wkr-1' })
          emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'b' })
          return { d, view }
        })
        await flush()
        const before = h.view.forWorkspace(harness.workspaceId)
        expect(before.map(t => t.id)).toEqual(['a1', 'a2'])

        // Re-write `worker_id` with the value it already holds. The register
        // object is replaced, so the projection rebuilds a1's row byte-identical.
        const bridge = getCRDTBridge()!
        for (let i = 0; i < 5; i++)
          bridge.enqueue(newBatch([setTabWorkerId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'wkr-1')]))
        await flush()

        const after = h.view.forWorkspace(harness.workspaceId)
        expect(after[0].workerId, 'the value is what it always was').toBe('wkr-1')
        expect(after[0], 'and the rewrite re-keyed nothing').toBe(before[0])
        expect(after[1]).toBe(before[1])
        h.d()
      })
    })

    /**
     * The HELD half of the same guarantee, and the one the projection cache
     * cannot supply.
     *
     * A held tab's row is not a `RenderedTab` the projection handed out -- it is
     * synthesized here from `lastResolved` plus the tab's live registers. Minting
     * a fresh object for it on every walk would re-key `mapArray`, which disposes
     * that tab's computation and hands `<For>` a new `Tab` with byte-identical
     * fields, remounting the row: an in-flight drag on it drops and a focused
     * rename input loses its caret.
     *
     * The trigger is an UNRELATED tab moving, because that is what swaps the
     * whole `placedTabs` array and so re-keys every item in it.
     */
    it('hands back the same object for a held tab when an unrelated tab moves', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view } = mountView()
          const bridge = getCRDTBridge()!
          bridge.enqueue(newBatch([
            setNodeKind(ctxFromBridge(bridge), harness.rootTileId, NodeKind.SPLIT),
            setNodeKind(ctxFromBridge(bridge), 'tile-b', NodeKind.LEAF),
            setNodeParentId(ctxFromBridge(bridge), 'tile-b', harness.rootTileId),
            setNodePosition(ctxFromBridge(bridge), 'tile-b', 'N'),
            setNodeKind(ctxFromBridge(bridge), 'tile-c', NodeKind.LEAF),
            setNodeParentId(ctxFromBridge(bridge), 'tile-c', harness.rootTileId),
            setNodePosition(ctxFromBridge(bridge), 'tile-c', 'V'),
          ]))
          emitAddTab({ type: TabType.AGENT, id: 'held', tileId: 'tile-b', position: 'a' })
          emitAddTab({ type: TabType.AGENT, id: 'mover', tileId: 'tile-b', position: 'b' })
          return { d, view }
        })
        await flush()

        // Point `held` at a tile nobody has a record of: the genuinely
        // unorderable case, so the join holds it at its last placement.
        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'held', 'not-yet')]))
        await flush()
        const before = h.view.getById(TabType.AGENT, 'held')
        expect(before, 'the tab is held, not dropped').toBeDefined()
        expect(before?.tileId).toBe('tile-b')

        // An idle tick must not disturb it either.
        bridge.enqueue(newBatch([]))
        await flush()
        expect(h.view.getById(TabType.AGENT, 'held'), 'idle tick').toBe(before)

        // Now move the OTHER tab, which swaps the whole placement array.
        bridge.enqueue(newBatch([setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'mover', 'tile-c')]))
        await flush()
        expect(h.view.getById(TabType.AGENT, 'mover')?.tileId, 'the move really landed').toBe('tile-c')
        expect(h.view.getById(TabType.AGENT, 'held'), 'the held tab is the same object').toBe(before)
        h.d()
      })
    })

    // Metadata is the other half of the join, and it is a separate store --
    // consumers subscribe to it through `assemble`, not through `placedTabs`.
    // The equality guard must not swallow a title arriving from a worker.
    it('propagates a metadata-only change', async () => {
      await withTestBridge(async (harness) => {
        const h = createRoot((d) => {
          const { view, metadata } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })

          let seen: string | undefined
          createEffect(() => {
            seen = view.forWorkspace(harness.workspaceId)[0]?.title
          })
          return { d, metadata, seen: () => seen }
        })
        await flush()
        expect(h.seen()).toBeUndefined()

        h.metadata.patch('a1', { title: 'Renamed' })
        await flush()

        expect(h.seen()).toBe('Renamed')
        h.d()
      })
    })
  })

  /**
   * The projection collapses a SPLIT that has one live child, rendering the
   * child under the SPLIT's node id. The child's own NodeRecord is untouched,
   * so a tab still names the child in its `tile_id` -- and used to render on no
   * tile at all: alive, untombstoned, invisible. Closing a grid next to an
   * agent made the agent vanish until the tree was split again.
   */
  describe('single-child SPLIT collapse', () => {
    function twoTiles(layoutStore: ReturnType<typeof createTestLayoutStore>) {
      const right = layoutStore.splitTile('root', 'horizontal')!
      const left = layoutStore.getAllTileIds().find(id => id !== right)!
      return { left, right }
    }

    it('keeps a tab rendered when its sibling GRID is removed', () => {
      createRoot((dispose) => {
        installTestBridge({ workspaceId: 'ws', rootTileId: 'root' })
        const { view, layoutStore } = createTestTabStores('ws')
        const { left, right } = twoTiles(layoutStore)
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: left, position: 'a', workerId: 'w' })

        const { gridId } = layoutStore.makeGrid(right, 2, 2)
        layoutStore.removeGrid(gridId)

        expect(view.getById(TabType.AGENT, 'a1'), 'the tab is still alive').toBeDefined()
        const rendered = layoutStore.getAllTileIds().flatMap(id => view.forTile(id).map(t => t.id))
        expect(rendered).toContain('a1')
        dispose()
      })
    })

    it('keeps a tab rendered when its sibling tile is closed', () => {
      createRoot((dispose) => {
        installTestBridge({ workspaceId: 'ws', rootTileId: 'root' })
        const { view, layoutStore } = createTestTabStores('ws')
        const { left, right } = twoTiles(layoutStore)
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: left, position: 'a', workerId: 'w' })

        layoutStore.closeTile(right)

        const tiles = layoutStore.getAllTileIds()
        expect(tiles).toHaveLength(1)
        expect(view.forTile(tiles[0]).map(t => t.id)).toEqual(['a1'])
        dispose()
      })
    })

    it('keeps a tab rendered through NESTED collapses', () => {
      // Two levels of single-child SPLIT: the alias chain has to resolve
      // transitively or the tab lands on an intermediate id nothing renders.
      createRoot((dispose) => {
        installTestBridge({ workspaceId: 'ws', rootTileId: 'root' })
        const { view, layoutStore } = createTestTabStores('ws')
        const outerRight = layoutStore.splitTile('root', 'horizontal')!
        const outerLeft = layoutStore.getAllTileIds().find(id => id !== outerRight)!
        const innerRight = layoutStore.splitTile(outerLeft, 'vertical')!
        const innerLeft = layoutStore.getAllTileIds().find(id => id !== outerRight && id !== innerRight)!
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: innerLeft, position: 'a', workerId: 'w' })

        layoutStore.closeTile(innerRight)
        layoutStore.closeTile(outerRight)

        expect(view.getById(TabType.AGENT, 'a1'), 'the tab is still alive').toBeDefined()
        const rendered = layoutStore.getAllTileIds().flatMap(id => view.forTile(id).map(t => t.id))
        expect(rendered).toContain('a1')
        dispose()
      })
    })
  })

  /**
   * A tab whose tile chain is momentarily unresolvable -- mid-batch during a
   * close/undo-split, or between a Batch frame and the EntityMaterialized frame
   * that creates its new tile -- drops out of the projection's `ownedTabs` while
   * remaining perfectly alive in the CRDT. Anything that sweeps on "not in
   * ownedTabs" therefore deletes live state: the tab comes back seconds later
   * with its title, git badges and terminal scrollback gone for good.
   *
   * `state.tabs` is the authoritative "does this tab exist" signal, and it does
   * not depend on the tile resolving.
   */
  /**
   * The hold-in-place compensates for ONE thing: a tile id this client has no
   * record of yet, because the frame that would explain it has not arrived.
   * Everything else the projection leaves out of `renderedTabs`, it left out on
   * purpose — a live SPLIT tile is not a leaf, so its tabs are owned but not
   * rendered. Holding those would override the projection with a stale
   * placement and re-assert a tab on a tile the projection says it is not on.
   */
  describe('deliberate projection drops are not held', () => {
    it('does not re-render a tab whose tile a peer tombstoned', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          expect(view.all().map(t => t.id), 'resolves first, so it has a held placement').toEqual(['a1'])

          // Tombstoning the tile leaves its node RECORD in place (a tombstone
          // replaces the record rather than removing the key), so this client
          // knows exactly what happened -- the chain is dead. The projection
          // drops the tab deliberately, and the hold must not override it.
          const bridge = getCRDTBridge()!
          bridge.enqueue(newBatch([tombstoneNode(ctxFromBridge(bridge), harness.rootTileId)]))

          expect(view.all().map(t => t.id), 'the projection dropped it on purpose')
            .toEqual([])
          dispose()
        })
      })
    })
  })

  /**
   * `assemble` reads a tab's metadata row plus ~20 tracked fields. Running it for
   * every tab in the account inside ONE computation is what made a single
   * `metadata.patch` cost 0.750ms at 300 tabs; one computation per tab is what
   * bounds it. Counting the metadata reads is how that is observable at all --
   * object identity was already preserved by the cache this replaced, so an
   * identity assertion cannot tell the two apart.
   */
  describe('per-tab scoping', () => {
    const flush = () => new Promise<void>(queueMicrotask)

    /** Wrap `metadata` so every `get(tabId)` is recorded. */
    function countingMetadata() {
      const inner = createTabMetadataStore()
      const reads: string[] = []
      return {
        reads,
        store: {
          ...inner,
          get(tabId: string) {
            reads.push(tabId)
            return inner.get(tabId)
          },
        } as TabMetadataStore,
      }
    }

    it('re-assembles only the patched tab', async () => {
      await withTestBridge(async (harness) => {
        const counting = countingMetadata()
        const h = createRoot((d) => {
          const { state, projection } = projectionMemo()
          const view = createTabView({ projection, state, metadata: counting.store })
          for (const id of ['a1', 'a2', 'a3'])
            emitAddTab({ type: TabType.AGENT, id, tileId: harness.rootTileId, position: id })
          createEffect(() => {
            void view.forWorkspace(harness.workspaceId).map(t => t.title).join()
          })
          return { d, view }
        })
        await flush()
        counting.reads.length = 0

        counting.store.patch('a2', { title: 'Renamed' })
        await flush()

        expect(h.view.getById(TabType.AGENT, 'a2')?.title, 'the patch landed').toBe('Renamed')
        expect([...new Set(counting.reads)], 'only a2 is re-read').toEqual(['a2'])
        h.d()
      })
    })

    it('re-assembles nothing on a CRDT tick that moves no tab', async () => {
      await withTestBridge(async (harness) => {
        const counting = countingMetadata()
        const h = createRoot((d) => {
          const { state, projection } = projectionMemo()
          const view = createTabView({ projection, state, metadata: counting.store })
          for (const id of ['a1', 'a2', 'a3'])
            emitAddTab({ type: TabType.AGENT, id, tileId: harness.rootTileId, position: id })
          createEffect(() => {
            void view.forWorkspace(harness.workspaceId).map(t => t.title).join()
          })
          return { d, view }
        })
        await flush()
        counting.reads.length = 0

        // What a geometry-only drag frame plus its commit echo look like from
        // here: the version signal bumps, the projection does not move.
        const bridge = getCRDTBridge()!
        for (let i = 0; i < 20; i++)
          bridge.enqueue(newBatch([]))
        await flush()

        expect(counting.reads, 'the join must not even look at metadata').toEqual([])
        h.d()
      })
    })

    it('re-assembles only the moved tab when one tab moves', async () => {
      await withTestBridge(async (harness) => {
        seedWorkspace(harness, 'ws-other', 'other-root')
        const counting = countingMetadata()
        const h = createRoot((d) => {
          const { state, projection } = projectionMemo()
          const view = createTabView({ projection, state, metadata: counting.store })
          for (const id of ['a1', 'a2', 'a3'])
            emitAddTab({ type: TabType.AGENT, id, tileId: harness.rootTileId, position: id })
          createEffect(() => {
            void view.all().length
          })
          return { d, view }
        })
        await flush()
        const before = h.view.getById(TabType.AGENT, 'a1')
        counting.reads.length = 0

        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([
          setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a3', 'other-root'),
        ]))
        await flush()

        expect(h.view.getById(TabType.AGENT, 'a3')?.workspaceId).toBe('ws-other')
        expect([...new Set(counting.reads)], 'only the moved tab is re-assembled').toEqual(['a3'])
        expect(h.view.getById(TabType.AGENT, 'a1'), 'the others keep their objects').toBe(before)
        h.d()
      })
    })

    it('releases a departed tab instead of leaking its assembled object', async () => {
      await withTestBridge(async (harness) => {
        const counting = countingMetadata()
        const h = createRoot((d) => {
          const { state, projection } = projectionMemo()
          const view = createTabView({ projection, state, metadata: counting.store })
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'b' })
          createEffect(() => {
            void view.all().length
          })
          return { d, view }
        })
        await flush()

        const bridge = getCRDTBridge()!
        bridge.enqueue(newBatch([tombstoneTab(ctxFromBridge(bridge), TabType.AGENT, 'a1')]))
        await flush()
        expect(h.view.all().map(t => t.id)).toEqual(['a2'])
        counting.reads.length = 0

        // A patch for the departed tab reaches no computation: `mapArray` disposed
        // it, which is what replaced the hand-rolled eviction sweep.
        counting.store.patch('a1', { title: 'ghost' })
        await flush()
        expect(counting.reads).toEqual([])
        h.d()
      })
    })
  })

  /**
   * `placedTabs` runs on every CRDT tick by construction, and its walk is
   * O(all tabs in the account). The short-circuit is what makes the steady state
   * O(1) -- and what its two gates cost is the subject here, because getting
   * either one wrong loses a tab from the UI rather than just some time.
   */
  describe('placement walk short-circuit', () => {
    it('skips the walk while the projection rows have not moved', () => {
      createRoot((dispose) => {
        const rows = [{ userId: 'u', workspaceId: 'w', tabType: TabType.AGENT, tabId: 'a1', workerId: '', tileId: 'tile', position: 'a' }]
        // A hand-built pair rather than the bridge: the walk's cost is only
        // observable by counting reads of `state.tabs`, and the real manager
        // replaces the whole state object on `recomputeSpeculative`, which would
        // take the counter with it.
        const proj = { userId: 'u', workspaces: new Map(), ownedTabs: rows, renderedTabs: rows } as unknown as Projection
        let tabsReads = 0
        const tabs = { a1: { tabId: 'a1', tileId: { value: 'tile' } } }
        const state = {
          userId: 'u',
          nodes: { tile: {} },
          get tabs() {
            tabsReads++
            return tabs
          },
        } as unknown as UserCrdtState
        const [version, bump] = createSignal(0)
        const view = createTabView({
          projection: () => proj,
          // Exactly the shell's contract: notifies on every tick.
          state: () => {
            version()
            return state
          },
          metadata: createTabMetadataStore(),
        })

        expect(view.all()).toHaveLength(1)
        const afterFirstWalk = tabsReads
        expect(afterFirstWalk, 'the first run must walk').toBeGreaterThan(0)

        for (let i = 0; i < 10; i++)
          bump(v => v + 1)
        expect(view.all()).toHaveLength(1)
        expect(tabsReads, 'ten ticks, no walk').toBe(afterFirstWalk)
        dispose()
      })
    })

    /**
     * The reason the gate counts hold CANDIDATES rather than whether anything was
     * actually held.
     *
     * A tab dropped from `renderedTabs` because its tile chain died is a candidate
     * but not yet held -- the tile's record still exists. It starts being held the
     * moment that record is DELETED, and that deletion need not touch
     * `renderedTabs` at all, because the tab had already left it. Gating on "did
     * we hold anything last time" would short-circuit straight past it and lose
     * the tab from the UI until something unrelated moved.
     */
    it('starts holding a tab when its tile record is deleted, though the rows did not move', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          const bridge = getCRDTBridge()!
          const tile = 'child-tile'
          // Split the root by hand so the tab sits on a NON-root leaf: a
          // workspace root cannot be tombstoned by an ordinary batch.
          const ctx = ctxFromBridge(bridge)
          bridge.enqueue(newBatch([
            setNodeKind(ctx, harness.rootTileId, NodeKind.SPLIT),
            setNodeKind(ctx, tile, NodeKind.LEAF),
            setNodeParentId(ctx, tile, harness.rootTileId),
            setNodePosition(ctx, tile, 'N'),
            setNodeKind(ctx, 'sibling-tile', NodeKind.LEAF),
            setNodeParentId(ctx, 'sibling-tile', harness.rootTileId),
            setNodePosition(ctx, 'sibling-tile', 'V'),
          ]))
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: tile, position: 'M' })
          expect(view.getById(TabType.AGENT, 'a1')?.tileId).toBe(tile)

          // A peer tombstones the tile: the chain is dead, so the projection drops
          // the tab on purpose and it must NOT be held.
          bridge.enqueue(newBatch([tombstoneNode(ctxFromBridge(bridge), tile)]))
          expect(view.getById(TabType.AGENT, 'a1'), 'dropped on purpose').toBeUndefined()

          // Now the hub redacts the node out of this subscriber's set entirely.
          // Driven through the real `consumeEntityRemoved` rather than by deleting
          // the map slot: it also drops the pending ops naming that node, and
          // without that the queued tombstone simply re-creates the record on the
          // next `recomputeSpeculative`. `renderedTabs` does not change either
          // way -- the tab was already gone from it.
          harness.pending.consumeEntityRemoved(create(EntityRemovedSchema, {
            entity: { case: 'nodeId', value: tile },
          }))

          const held = view.getById(TabType.AGENT, 'a1')
          expect(held, 'an unknown tile is the case the hold exists for').toBeDefined()
          expect(held?.tileId).toBe(tile)
          dispose()
        })
      })
    })

    /**
     * The hold must never resurrect a placement the tab has demonstrably LEFT.
     *
     * A tab moves from one tile to another, and the new tile turns out to be a
     * live SPLIT rather than a leaf -- so the projection drops the tab on
     * purpose and it is a candidate, not held. That tick is where the remembered
     * placement stops being merely unused and becomes wrong: the projection has
     * said where the tab went. If the entry survives, deleting the NEW tile's
     * record later makes the tab pop back onto the OLD tile -- a placement
     * several ticks stale, and in the general case a different workspace.
     */
    it('forgets the remembered placement once the tab moves to a tile it can see', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          const bridge = getCRDTBridge()!
          const home = 'home-tile'
          const moved = 'moved-tile'
          bridge.enqueue(newBatch([
            setNodeKind(ctxFromBridge(bridge), harness.rootTileId, NodeKind.SPLIT),
            setNodeKind(ctxFromBridge(bridge), home, NodeKind.LEAF),
            setNodeParentId(ctxFromBridge(bridge), home, harness.rootTileId),
            setNodePosition(ctxFromBridge(bridge), home, 'N'),
            setNodeKind(ctxFromBridge(bridge), moved, NodeKind.LEAF),
            setNodeParentId(ctxFromBridge(bridge), moved, harness.rootTileId),
            setNodePosition(ctxFromBridge(bridge), moved, 'V'),
          ]))
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: home, position: 'M' })
          expect(view.getById(TabType.AGENT, 'a1')?.tileId, 'resolved on its home tile').toBe(home)

          // The tab moves to `moved`, and `moved` becomes a SPLIT in the same
          // batch -- a live node that does not own tabs, so the projection drops
          // the tab deliberately.
          bridge.enqueue(newBatch([
            setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', moved),
            setNodeKind(ctxFromBridge(bridge), moved, NodeKind.SPLIT),
            setNodeKind(ctxFromBridge(bridge), 'grandchild', NodeKind.LEAF),
            setNodeParentId(ctxFromBridge(bridge), 'grandchild', moved),
            setNodePosition(ctxFromBridge(bridge), 'grandchild', 'N'),
          ]))
          expect(view.getById(TabType.AGENT, 'a1'), 'a live non-leaf owns no tabs').toBeUndefined()

          // Now the hub redacts `moved` entirely. Its tile id names nothing this
          // client knows -- the shape that normally triggers the hold.
          harness.pending.consumeEntityRemoved(create(EntityRemovedSchema, {
            entity: { case: 'nodeId', value: moved },
          }))

          expect(
            view.getById(TabType.AGENT, 'a1'),
            'must not reappear on the tile it left; the stale memory was dropped when the move was seen',
          ).toBeUndefined()
          dispose()
        })
      })
    })

    it('keeps applying register writes to a tab that is still held', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          const bridge = getCRDTBridge()!
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M', workerId: 'wkr-1' })
          bridge.enqueue(newBatch([setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'not-yet')]))
          expect(view.getById(TabType.AGENT, 'a1')?.workerId).toBe('wkr-1')

          // The short-circuit must stay OFF while a hold is in flight, or this
          // write never reaches the view.
          bridge.enqueue(newBatch([setTabWorkerId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'wkr-2')]))
          expect(view.getById(TabType.AGENT, 'a1')?.workerId).toBe('wkr-2')
          dispose()
        })
      })
    })

    it('forgets a held tab whose record is redacted out of the subscriber set', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          const bridge = getCRDTBridge()!
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
          emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'V' })

          // a1 stops resolving and is held; `renderedTabs` now holds a2 alone and
          // stays that way, so every later tick runs the scoped candidate walk.
          bridge.enqueue(newBatch([setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'not-yet')]))
          expect(view.getById(TabType.AGENT, 'a1'), 'held').toBeDefined()

          harness.pending.consumeEntityRemoved(create(EntityRemovedSchema, {
            entity: { case: 'tab', value: { tabType: TabType.AGENT, tabId: 'a1' } },
          }))
          expect(view.getById(TabType.AGENT, 'a1'), 'gone from the CRDT, gone from the view').toBeUndefined()
          expect(view.all().map(t => t.id), 'and the rest are untouched').toEqual(['a2'])
          dispose()
        })
      })
    })

    /**
     * A hold must not cost an account-wide walk per tick for as long as it lasts.
     *
     * A held tab keeps `renderedTabs` still -- it left that array when it stopped
     * resolving -- so the "rows are the same" fast path is available, but the tab
     * genuinely has to be re-checked every tick: its tile record may arrive, its
     * `worker_id` may be written, its own record may be redacted. Re-checking the
     * REMEMBERED candidates is what makes that cost O(held) instead of O(every
     * tab in the account), and a hold can last indefinitely -- `EntityRemoved` on
     * the tile deletes the record for good.
     *
     * `Object.values(state.tabs)` is the account-wide walk and is the only thing
     * here that ENUMERATES the tab map; a scoped re-check reads `state.tabs[id]`.
     * So an `ownKeys` trap counts exactly the full walks.
     */
    it('re-checks only the held tab on later ticks, not every tab in the account', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const metadata = createTabMetadataStore()
          const { state, projection } = projectionMemo()
          const scans = { count: 0 }
          const countingState = () => {
            const s = state()
            if (!s)
              return s
            const tabs = new Proxy(s.tabs, {
              ownKeys(t) {
                scans.count++
                return Reflect.ownKeys(t)
              },
            })
            return { ...s, tabs } as UserCrdtState
          }
          const view = createTabView({ projection, state: countingState, metadata })
          const bridge = getCRDTBridge()!
          for (const id of ['a1', 'a2', 'a3'])
            emitAddTab({ type: TabType.AGENT, id, tileId: harness.rootTileId, position: id })

          // Enter the hold. This tick legitimately rescans: the rows moved.
          bridge.enqueue(newBatch([setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'not-yet')]))
          expect(view.getById(TabType.AGENT, 'a1'), 'held').toBeDefined()
          scans.count = 0

          // Twenty geometry-only frames with the hold still in flight.
          for (let i = 0; i < 20; i++) {
            bridge.enqueue(newBatch([]))
            void view.all()
          }
          expect(scans.count, 'no tick rescans the account').toBe(0)
          expect(view.getById(TabType.AGENT, 'a1')?.tileId, 'and the tab is still held').toBe(harness.rootTileId)

          // The scoped walk still has to SEE writes to the held tab.
          bridge.enqueue(newBatch([setTabWorkerId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'wkr-9')]))
          expect(view.getById(TabType.AGENT, 'a1')?.workerId).toBe('wkr-9')
          dispose()
        })
      })
    })
  })

  describe('transiently unresolvable tabs', () => {
    it('holds an unresolvable tab at its last known placement, and the sweep spares it', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view, metadata } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          metadata.patch('a1', { title: 'Mine', screen: new Uint8Array([1, 2, 3]) })
          expect(view.all().map(t => t.id)).toEqual(['a1'])

          // Point the tab at a tile that does not exist: unresolvable, but the
          // TabRecord is untombstoned and the tab is still the user's.
          const bridge = getCRDTBridge()!
          bridge.enqueue(newBatch([
            setTabTileId(ctxFromBridge(bridge), TabType.AGENT, 'a1', 'not-a-real-tile'),
          ]))

          // The JOIN is what is under test: the tab must still be visible, held
          // at the placement it last resolved to, rather than vanishing for the
          // tick its tile is missing.
          const held = view.getById(TabType.AGENT, 'a1')
          expect(held, 'the tab is still rendered').toBeDefined()
          expect(held?.tileId, 'held at its last known tile').toBe(harness.rootTileId)
          expect(view.forWorkspace(harness.workspaceId).map(t => t.id)).toEqual(['a1'])

          // And the metadata sweep, keyed on the raw record set, spares it: the
          // tab is still LIVE, so nothing retires its row.
          const state = harness.pending.state.speculativeState
          expect(hlcIsZero(state.tabs.a1.tombstoneAt), 'not tombstoned').toBe(true)
          expect(liveTabIds(state).has('a1'), 'an unresolvable tab is still live').toBe(true)
          // The sweep's real question, transcribed: retire every row the CRDT
          // has no live record for. Passing an EMPTY set instead would assert
          // nothing -- `dropTabs` returns early on one, so the expectations
          // below would hold for any implementation, including one that
          // retired this tab.
          const live = liveTabIds(state)
          metadata.dropTabs(new Set(
            Object.keys(metadata.state.byTabId).filter(id => !live.has(id)),
          ))
          expect(metadata.get('a1')?.title, 'title survives the sweep').toBe('Mine')
          expect(metadata.get('a1')?.screen).toEqual(new Uint8Array([1, 2, 3]))
          dispose()
        })
      })
    })
  })

  /**
   * `lastOffset` is written once per PTY read -- the worker emits one
   * TerminalEvent_Data per `ptmx.Read` with no coalescing -- so if the join
   * carried it, a busy terminal would re-run the O(all-tabs) assemble and
   * re-sort every workspace on every output chunk. The field lives in
   * `tabMetadata` and is read straight from there by its two consumers.
   */
  it('does not re-run the join when only lastOffset changes', async () => {
    const flush = () => new Promise<void>(queueMicrotask)
    await withTestBridge(async (harness) => {
      const ctx = createRoot((d) => {
        const { view, metadata } = mountView()
        emitAddTab({ type: TabType.TERMINAL, id: 't1', tileId: harness.rootTileId, position: 'a' })
        metadata.patch('t1', { title: 'zsh' })
        let runs = 0
        createEffect(() => {
          void view.forWorkspace(harness.workspaceId).length
          runs++
        })
        return { d, metadata, runs: () => runs }
      })
      await flush()
      const before = ctx.runs()

      ctx.metadata.patch('t1', { lastOffset: 4096 })
      await flush()
      expect(ctx.runs(), 'a PTY-frequency write must not invalidate the join').toBe(before)

      // Control: a field the join DOES read still propagates.
      ctx.metadata.patch('t1', { title: 'bash' })
      await flush()
      expect(ctx.runs(), 'a joined field still propagates').toBeGreaterThan(before)
      ctx.d()
    })
  })

  it('orders tabs within a tile by position', () => {
    withTestBridge((harness) => {
      createRoot((dispose) => {
        const { view } = mountView()
        emitAddTab({ type: TabType.AGENT, id: 'c', tileId: harness.rootTileId, position: 'z' })
        emitAddTab({ type: TabType.AGENT, id: 'a', tileId: harness.rootTileId, position: 'a' })
        emitAddTab({ type: TabType.AGENT, id: 'b', tileId: harness.rootTileId, position: 'm' })

        expect(view.forTile(harness.rootTileId).map(t => t.id)).toEqual(['a', 'b', 'c'])
        dispose()
      })
    })
  })

  /**
   * Both sort keys are ordered by CODE POINT everywhere else in the system —
   * `project()` sorts by `cmpStr`, and `ops.ts`'s `liveTabsOnTile` promises in
   * its own doc that "the visible order is identical before and after". Intl
   * collation breaks that promise, so the join must not use `localeCompare`.
   */
  describe('ordering agrees with the crdt layer, not with Intl collation', () => {
    it('breaks a position tie by code point, so mixed-case ids do not reorder', () => {
      // nanoid ids are mixed-case (`A-Za-z0-9`). Case-insensitive collation
      // puts 'a…' before 'Z…' in EVERY locale, while code-point order puts
      // uppercase first — so this pair distinguishes the two comparators
      // regardless of which locale the test runner is under.
      expect('Zx7QpLm'.localeCompare('ax7QpLm'), 'precondition: collation disagrees').toBe(1)
      expect('Zx7QpLm' < 'ax7QpLm', 'precondition: code point disagrees').toBe(true)

      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          // Identical positions force the id tie-break to decide the order.
          emitAddTab({ type: TabType.AGENT, id: 'ax7QpLm', tileId: harness.rootTileId, position: 'n' })
          emitAddTab({ type: TabType.AGENT, id: 'Zx7QpLm', tileId: harness.rootTileId, position: 'n' })

          expect(view.forTile(harness.rootTileId).map(t => t.id)).toEqual(['Zx7QpLm', 'ax7QpLm'])
          dispose()
        })
      })
    })

    // The `position` key has the same hazard — Lithuanian and Latvian
    // collation hoist `y` ahead of `j`..`x`, and LexoRank does emit `y` — but
    // it is deliberately NOT asserted here: no pair of a-z LexoRank strings
    // orders differently under this runner's en-US default, so such a test
    // would pass against either comparator and prove nothing. The tie-break
    // case above discriminates in every locale and guards the same line.
  })

  /**
   * The narrowed lookups exist so callers that only make sense for one tab kind
   * get the narrow type instead of casting at each site. Each must reject an id
   * belonging to a DIFFERENT kind rather than hand back a mistyped tab: the id
   * space is per-kind, so the type tag is the only thing that makes the answer
   * safe. TileRenderer's file pane resolves its viewer through `getFileTab` on
   * every reactive read, so a wrong answer there renders another kind's tab.
   */
  describe('narrowed lookups', () => {
    it('resolves each kind and rejects an id of another kind', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          const { view } = mountView()
          emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'a' })
          emitAddTab({ type: TabType.TERMINAL, id: 't1', tileId: harness.rootTileId, position: 'b' })
          emitAddTab({ type: TabType.FILE, id: 'f1', tileId: harness.rootTileId, position: 'c' })

          expect(view.getAgentTab('a1')?.type).toBe(TabType.AGENT)
          expect(view.getTerminalTab('t1')?.type).toBe(TabType.TERMINAL)
          expect(view.getFileTab('f1')?.type).toBe(TabType.FILE)

          expect(view.getFileTab('a1'), 'an agent id is not a file tab').toBeUndefined()
          expect(view.getAgentTab('f1'), 'a file id is not an agent tab').toBeUndefined()
          expect(view.getTerminalTab('f1'), 'a file id is not a terminal tab').toBeUndefined()
          dispose()
        })
      })
    })

    it('returns undefined for an id nothing has ever used', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const { view } = mountView()
          expect(view.getFileTab('ghost')).toBeUndefined()
          dispose()
        })
      })
    })
  })
})
