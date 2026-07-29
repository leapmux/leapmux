import type { createTestLayoutStore } from '~/test-support/tabStores'
/// <reference types="vitest/globals" />
import { createEffect, createMemo, createRoot } from 'solid-js'

import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { ctxFromBridge, getCRDTBridge, hlcIsZero, newBatch, project, setTabTileId, tombstoneNode, tombstoneTab } from '~/lib/crdt'
import { installTestBridge, seedWorkspace, withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
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
  const bridge = getCRDTBridge()!
  // Mirrors AppShell: one projection memo over speculativeState, shared by
  // every workspace's view.
  // `equals: false` is load-bearing: PendingOpsManager mutates
  // speculativeState IN PLACE, so its identity never changes and a default
  // memo would swallow every update after the first. AppShell has the same
  // requirement.
  const state = createMemo(() => bridge.speculativeState(), undefined, { equals: false })
  const projection = createMemo(() => {
    const s = state()
    return s ? project(s) : null
  })
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
        metadata.patch('a1', { title: 'My Agent', gitBranch: 'feature/x' })

        const tab = view.getById(TabType.AGENT, 'a1')!
        // Placement from the projection...
        expect(tab.tileId).toBe(harness.rootTileId)
        expect(tab.workerId).toBe('wkr-1')
        expect(tab.workspaceId).toBe(harness.workspaceId)
        // ...metadata from the store, joined on tab id.
        expect(tab.title).toBe('My Agent')
        expect(tab.gitBranch).toBe('feature/x')
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
   * join needs `placedTabs`' structural `equals` or every drag frame rebuilds
   * every `Tab` in every workspace and re-runs every consumer.
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

          // And the metadata sweep, keyed on the raw record set, spares it.
          const state = harness.pending.state.speculativeState
          expect(hlcIsZero(state.tabs.a1.tombstoneAt), 'not tombstoned').toBe(true)
          metadata.retainOnly(liveTabIds(state))
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
})
