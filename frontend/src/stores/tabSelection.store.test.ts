/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { flush } from '~/test-support/async'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { tabKey } from './tab.helpers'
import { emitAddTab, emitMoveTabToTile, emitRemoveTab } from './tabOps'
import { useSelectionSweep } from './tabSelection.store'

afterEach(() => setCRDTBridge(null))

const WS = 'ws-test'

function withStores<T>(
  body: (ctx: ReturnType<typeof createTestTabStores> & {
    rootTileId: string
    harness: ReturnType<typeof installTestBridge>
  }) => T,
): T {
  return createRoot((dispose) => {
    const harness = installTestBridge({ workspaceId: WS })
    try {
      return body({ ...createTestTabStores(WS), rootTileId: harness.rootTileId, harness })
    }
    finally {
      dispose()
    }
  })
}

let seq = 0
function add(id: string, tileId: string) {
  seq += 1
  emitAddTab({ type: TabType.AGENT, id, tileId, position: `p${seq}`, workerId: 'w-1' })
}

const key = (id: string) => tabKey({ type: TabType.AGENT, id })

describe('tabSelection', () => {
  describe('setActive', () => {
    it('reads the workspace and tile off the tab itself', () => {
      withStores(({ selection, view, rootTileId }) => {
        add('a', rootTileId)
        selection.setActive(view.getById(TabType.AGENT, 'a')!)
        expect(selection.activeKeyForWorkspace(WS)).toBe(key('a'))
        expect(selection.activeKeyForTile(rootTileId)).toBe(key('a'))
      })
    })

    it('ignores an id that resolves to no tab', () => {
      withStores(({ selection }) => {
        selection.setActiveById(TabType.AGENT, 'ghost')
        expect(selection.activeKeyForWorkspace(WS)).toBeNull()
      })
    })
  })

  /**
   * The pointers name a tab by key, and a tab can vanish underneath them at any
   * time — closed here, closed on another device, or tombstoned by a peer's op.
   * Nothing writes to `tabSelection` on those paths (a tombstone is a CRDT op,
   * not a store call), so a stored key that no longer resolves has to heal on
   * read or the shell renders an empty pane with tabs still in the bar.
   */
  describe('dangling pointers', () => {
    it('falls back to the MRU survivor when the active tab is closed', () => {
      withStores(({ selection, metadata, view, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)
        metadata.touchMru('b')
        selection.setActiveById(TabType.AGENT, 'a')

        emitRemoveTab(TabType.AGENT, 'a')

        expect(view.getById(TabType.AGENT, 'a'), 'the tab really is gone').toBeUndefined()
        expect(selection.activeKeyForWorkspace(WS)).toBe(key('b'))
        expect(selection.activeTabForWorkspace(WS)?.id).toBe('b')
      })
    })

    it('picks the highest-MRU survivor, not the first', () => {
      withStores(({ selection, metadata, view, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)
        add('c', rootTileId)
        metadata.touchMru('b')
        metadata.touchMru('c')
        metadata.touchMru('b')
        selection.setActiveById(TabType.AGENT, 'a')

        emitRemoveTab(TabType.AGENT, 'a')

        expect(selection.activeTabForWorkspace(WS)?.id).toBe('b')
        expect(view.forWorkspace(WS).map(t => t.id)).toEqual(['b', 'c'])
      })
    })

    it('reports null when the last tab in the workspace is closed', () => {
      withStores(({ selection, rootTileId }) => {
        add('only', rootTileId)
        selection.setActiveById(TabType.AGENT, 'only')

        emitRemoveTab(TabType.AGENT, 'only')

        expect(selection.activeKeyForWorkspace(WS)).toBeNull()
        expect(selection.activeTabForWorkspace(WS)).toBeUndefined()
      })
    })

    it('heals the per-tile pointer independently of the workspace pointer', () => {
      withStores(({ selection, layoutStore, view, rootTileId }) => {
        const otherTile = layoutStore.splitTile(rootTileId, 'horizontal')!
        const homeTile = layoutStore.getAllTileIds().find(id => id !== otherTile)!
        add('a', homeTile)
        add('b', homeTile)
        add('elsewhere', otherTile)
        selection.setActiveById(TabType.AGENT, 'a')
        selection.setActiveById(TabType.AGENT, 'elsewhere')

        emitRemoveTab(TabType.AGENT, 'a')

        // The home tile's pointer named the closed tab; it promotes its own
        // survivor rather than borrowing the other tile's.
        expect(selection.activeKeyForTile(homeTile)).toBe(key('b'))
        expect(selection.activeKeyForTile(otherTile)).toBe(key('elsewhere'))
        expect(view.forTile(homeTile).map(t => t.id)).toEqual(['b'])
      })
    })

    // A move within one workspace keeps the WORKSPACE pointer valid — the tab
    // is still in that workspace — but the SOURCE TILE's pointer must stop
    // claiming it, or the tile renders a tab that is not on it (no pane
    // matches, so it goes blank with its tab bar still populated).
    it('releases the source tile pointer when its tab moves away, but keeps the workspace one', () => {
      withStores(({ selection, layoutStore, rootTileId }) => {
        const otherTile = layoutStore.splitTile(rootTileId, 'horizontal')!
        const homeTile = layoutStore.getAllTileIds().find(id => id !== otherTile)!
        add('a', homeTile)
        add('b', homeTile)
        selection.setActiveById(TabType.AGENT, 'a')
        expect(selection.activeKeyForTile(homeTile)).toBe(key('a'))

        emitMoveTabToTile(TabType.AGENT, 'a', otherTile)

        // Same workspace, so this pointer is still correct.
        expect(selection.activeKeyForWorkspace(WS)).toBe(key('a'))
        // The source tile promotes its own survivor instead of naming a tab
        // that now lives on `otherTile`.
        expect(selection.activeKeyForTile(homeTile)).toBe(key('b'))
        expect(selection.activeTabForTile(homeTile)?.tileId).toBe(homeTile)
      })
    })

    // The last tab leaving a tile leaves it with nothing to promote — the
    // pointer must resolve to null rather than to the departed tab.
    it('resolves to null when the moved tab was the tile last tab', () => {
      withStores(({ selection, layoutStore, rootTileId }) => {
        const otherTile = layoutStore.splitTile(rootTileId, 'horizontal')!
        const homeTile = layoutStore.getAllTileIds().find(id => id !== otherTile)!
        add('a', homeTile)
        selection.setActiveById(TabType.AGENT, 'a')

        emitMoveTabToTile(TabType.AGENT, 'a', otherTile)

        expect(selection.activeKeyForTile(homeTile)).toBeNull()
        expect(selection.activeKeyForTile(otherTile)).toBe(key('a'))
      })
    })

    it('does not let one workspace heal into another workspace tabs', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')
        emitRemoveTab(TabType.AGENT, 'a')

        // `ws-other` has no tabs at all; the healed lookup must not reach
        // across into this workspace's survivors.
        expect(selection.activeKeyForWorkspace('ws-other')).toBeNull()
      })
    })
  })

  /**
   * `restore` reads the stored key through `view.get`, which spans EVERY
   * workspace. A key persisted for workspace A can therefore name a tab that
   * has since moved to B — a cross-workspace move here, or one from another
   * device, outliving the reload in sessionStorage. Writing that tab's tile
   * pointer would make B's tile jump to it while restoring A.
   */
  describe('restore does not reach across workspaces', () => {
    it('ignores the tile of a stored tab that now lives in another workspace', () => {
      withStores(({ selection, harness }) => {
        seedWorkspace(harness, 'ws-other', 'other-root')
        seq += 1
        emitAddTab({ type: TabType.AGENT, id: 'moved', tileId: 'other-root', position: `p${seq}`, workerId: 'w-1' })

        selection.restore(WS, key('moved'), {})

        expect(
          selection.state.activeByTile['other-root'],
          'the other workspace\'s tile must not be repointed',
        ).toBeUndefined()
        // The workspace pointer is still written; `resolve` heals it on read
        // because the tab fails the `belongs` check for this workspace.
        expect(selection.state.activeByWorkspace[WS]).toBe(key('moved'))
        expect(selection.activeKeyForWorkspace(WS), 'healed to null - no tabs here').toBeNull()
      })
    })

    it('still back-fills the tile when the stored tab is in this workspace', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)

        selection.restore(WS, key('a'), {})

        expect(selection.state.activeByTile[rootTileId]).toBe(key('a'))
      })
    })
  })

  /**
   * Tile ids and workspace ids are never reused, so a deleted workspace's
   * pointers would sit here for the life of the page. Harmless per entry --
   * `resolve` heals them to null -- but this is the same unbounded-growth class
   * the join's `assembled`/`lastResolved` caches went out of their way to bound.
   */
  describe('cleanupWorkspaces', () => {
    it('drops the workspace pointer and the tiles it names', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')
        expect(selection.state.activeByWorkspace[WS]).toBeDefined()
        expect(selection.state.activeByTile[rootTileId]).toBeDefined()

        // The workspace and its tile have left the projection.
        selection.retainOnly(new Set(), new Set())

        expect(selection.state.activeByWorkspace[WS]).toBeUndefined()
        expect(selection.state.activeByTile[rootTileId]).toBeUndefined()
      })
    })

    it('leaves other workspaces alone', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')

        // This workspace and tile are still live; only some OTHER workspace
        // disappeared, which the sweep expresses by simply not listing it.
        selection.retainOnly(new Set([WS]), new Set([rootTileId]))

        expect(selection.state.activeByWorkspace[WS], 'unrelated pointer survives').toBeDefined()
        expect(selection.state.activeByTile[rootTileId]).toBeDefined()
      })
    })
  })

  /**
   * Reclamation is a sweep off the projection, not a duty of whichever handler
   * happened to close something. The version this replaced ran from exactly one
   * call site — the LOCAL workspace delete — so a workspace or tile closed on
   * another device arrived as a pure CRDT tombstone with no local handler and
   * leaked its pointers for the life of the page.
   */
  describe('useSelectionSweep', () => {
    it('reclaims pointers for a workspace deleted on another device', async () => {
      await createRoot(async (dispose) => {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        seedWorkspace(harness, 'ws-remote', 'remote-tile')
        useSelectionSweep(stores.projection, stores.selection, {
          tileOrderFor: wsId => stores.layoutStore.tileOrderFor(wsId),
          getAllTileIdsFor: wsId => stores.floatingWindowStore.getAllTileIdsFor(wsId),
        })

        emitAddTab({ type: TabType.AGENT, id: 'r1', tileId: 'remote-tile', position: 'a' })
        stores.selection.setActiveById(TabType.AGENT, 'r1')
        await flush()
        expect(stores.selection.state.activeByWorkspace['ws-remote']).toBeDefined()
        expect(stores.selection.state.activeByTile['remote-tile']).toBeDefined()

        // A peer deletes it. Nothing local runs; the tombstone just lands.
        delete harness.pending.state.confirmedState.workspaces['ws-remote']
        harness.pending.recomputeSpeculative()
        // Any op re-derives the projection, as a live client would see.
        emitAddTab({ type: TabType.AGENT, id: 'local', tileId: harness.rootTileId, position: 'a' })
        await flush()

        expect(stores.selection.state.activeByWorkspace['ws-remote'], 'workspace pointer reclaimed')
          .toBeUndefined()
        expect(stores.selection.state.activeByTile['remote-tile'], 'and its tile pointer')
          .toBeUndefined()
        dispose()
      })
    })

    /**
     * Floating tiles are real `activeByTile` keys, and they reach the sweep by a
     * different accessor than main-tree tiles do. The live set is composed from
     * the two stores that already index them rather than re-walked here, so
     * dropping either half silently reclaims pointers for tiles that are very
     * much alive -- and the user's selection inside a floating window vanishes.
     */
    it('keeps the pointer for a live FLOATING tile', async () => {
      await createRoot(async (dispose) => {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        useSelectionSweep(stores.projection, stores.selection, {
          tileOrderFor: wsId => stores.layoutStore.tileOrderFor(wsId),
          getAllTileIdsFor: wsId => stores.floatingWindowStore.getAllTileIdsFor(wsId),
        })

        const created = stores.floatingWindowStore.addWindow({ x: 0.1, y: 0.1, width: 0.3, height: 0.3 })!
        emitAddTab({ type: TabType.AGENT, id: 'fw', tileId: created.tileId, position: 'a' })
        stores.selection.setActiveById(TabType.AGENT, 'fw')
        await flush()

        // Churn the projection so the sweep actually runs.
        emitAddTab({ type: TabType.AGENT, id: 'main', tileId: harness.rootTileId, position: 'b' })
        await flush()

        expect(
          stores.selection.state.activeByTile[created.tileId],
          'a floating tile is not a dead tile',
        ).toBeDefined()
        dispose()
      })
    })

    it('does not sweep before the bootstrap lands', async () => {
      await createRoot(async (dispose) => {
        setCRDTBridge(null)
        const stores = createTestTabStores(WS)
        useSelectionSweep(stores.projection, stores.selection, {
          tileOrderFor: wsId => stores.layoutStore.tileOrderFor(wsId),
          getAllTileIdsFor: wsId => stores.floatingWindowStore.getAllTileIdsFor(wsId),
        })
        // A restore ran from sessionStorage before any projection existed.
        stores.selection.restore(WS, key('a'), { 'some-tile': key('a') })
        await flush()

        // Projection is null, so the sweep must stay its hand -- wiping here
        // would destroy a restore that cannot run twice.
        expect(stores.selection.state.activeByWorkspace[WS], 'restored pointer survives').toBeDefined()
        expect(stores.selection.state.activeByTile['some-tile']).toBeDefined()
        dispose()
      })
    })
  })

  describe('retainOnly', () => {
    it('drops entries for tiles that no longer exist', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')
        selection.restore(WS, null, { 'dead-tile': key('a') })

        selection.retainOnly(new Set([WS]), new Set([rootTileId]))

        expect(selection.state.activeByTile['dead-tile']).toBeUndefined()
        expect(selection.activeKeyForTile(rootTileId)).toBe(key('a'))
      })
    })
  })

  /**
   * Activating a tab is three writes, not one. The old store's activation paths
   * each bumped MRU and cleared the notification dot alongside the pointer
   * writes; splitting the store dropped both, leaving `mru` permanently
   * undefined (so every MRU consumer silently fell back to position order) and
   * no writer anywhere for `hasNotification: false`.
   */
  describe('setActive side effects', () => {
    it('stamps MRU so the most recently activated tab wins', () => {
      withStores(({ selection, metadata, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)

        selection.setActiveById(TabType.AGENT, 'a')
        selection.setActiveById(TabType.AGENT, 'b')

        const mruA = metadata.get('a')?.mru ?? 0
        const mruB = metadata.get('b')?.mru ?? 0
        expect(mruB).toBeGreaterThan(mruA)
      })
    })

    // Without the MRU stamp `mruHead` degenerates to `tabs[0]` — the leftmost
    // tab — so closing the active tab promotes the wrong one.
    it('promotes the most recently used survivor, not the leftmost', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)
        add('c', rootTileId)
        selection.setActiveById(TabType.AGENT, 'b')
        selection.setActiveById(TabType.AGENT, 'c')

        emitRemoveTab(TabType.AGENT, 'c')

        expect(selection.activeKeyForTile(rootTileId)).toBe(key('b'))
      })
    })

    it('clears the notification dot on the tab it activates', () => {
      withStores(({ selection, metadata, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)
        metadata.patch('a', { hasNotification: true })
        metadata.patch('b', { hasNotification: true })

        selection.setActiveById(TabType.AGENT, 'a')

        expect(metadata.get('a')?.hasNotification).toBe(false)
        expect(metadata.get('b')?.hasNotification, 'other tabs keep their badge').toBe(true)
      })
    })
  })

  /**
   * The VALIDATED-but-never-synthesised half of the pointer API, and the one
   * anything persisting must use.
   *
   * `activeKeyForWorkspace` heals all the way to `mruHead`, which is right for
   * rendering -- the UI always needs something to highlight -- and destructive to
   * persist, because `mru` is never stored: after a reload every tab scores zero
   * and the invented answer is always the FIRST tab. These accessors keep the
   * three cases apart so a writer can tell "nobody has chosen yet" (leave the
   * stored key for the restore) from "the choice is dead" (reclaim it).
   */
  describe('chosen pointers', () => {
    it('answers undefined when nothing has been chosen, even with tabs present', () => {
      withStores(({ selection, rootTileId }) => {
        add('first', rootTileId)
        add('second', rootTileId)

        expect(selection.chosenKeyForWorkspace(WS)).toBeUndefined()
        // The contrast that makes it worth having: the rendering accessor
        // invents an answer from the same state.
        expect(selection.activeKeyForWorkspace(WS)).toBe(key('first'))
      })
    })

    it('answers the key while the choice is live', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')

        expect(selection.chosenKeyForWorkspace(WS)).toBe(key('a'))
      })
    })

    it('answers null once the chosen tab is closed', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)
        add('b', rootTileId)
        selection.setActiveById(TabType.AGENT, 'a')
        emitRemoveTab(TabType.AGENT, 'a')

        // Nothing rewrites the raw pointer on a close -- a tombstone is a CRDT
        // op, not a store call -- so only the validation tells them apart.
        expect(selection.state.activeByWorkspace[WS], 'the raw pointer outlives its tab').toBe(key('a'))
        expect(selection.chosenKeyForWorkspace(WS)).toBeNull()
      })
    })

    it('answers null when the chosen tab moved to another workspace', () => {
      withStores(({ selection, view, harness, rootTileId }) => {
        const elsewhere = seedWorkspace(harness, 'ws-other', 'tile-other')
        add('moves', rootTileId)
        selection.setActiveById(TabType.AGENT, 'moves')
        emitMoveTabToTile(TabType.AGENT, 'moves', elsewhere)

        // Still LIVE globally -- a liveness check alone would keep satisfying
        // this workspace's pointer. `belongs` is what makes it a real check.
        expect(view.get(key('moves'))?.workspaceId).toBe('ws-other')
        expect(selection.chosenKeyForWorkspace(WS)).toBeNull()
      })
    })

    it('omits a tile nobody has chosen on, rather than reporting null', () => {
      withStores(({ selection, rootTileId }) => {
        add('a', rootTileId)

        // Absent, NOT null: "no tile has been chosen on" is every reload before
        // the restore runs, and a caller that saw null there would clear the
        // snapshot it was about to read.
        expect(selection.chosenTileActivesFor([rootTileId])).toEqual({})
      })
    })

    it('reports a live per-tile choice and nulls a dead one', () => {
      withStores(({ selection, layoutStore, rootTileId }) => {
        const otherTile = layoutStore.splitTile(rootTileId, 'horizontal')!
        const homeTile = layoutStore.getAllTileIds().find(id => id !== otherTile)!
        add('stays', homeTile)
        add('goes', otherTile)
        selection.setActiveById(TabType.AGENT, 'stays')
        selection.setActiveById(TabType.AGENT, 'goes')
        emitRemoveTab(TabType.AGENT, 'goes')

        expect(selection.chosenTileActivesFor([homeTile, otherTile])).toEqual({
          [homeTile]: key('stays'),
          [otherTile]: null,
        })
      })
    })

    it('nulls a per-tile choice whose tab moved to another tile', () => {
      withStores(({ selection, layoutStore, rootTileId }) => {
        const otherTile = layoutStore.splitTile(rootTileId, 'horizontal')!
        const homeTile = layoutStore.getAllTileIds().find(id => id !== otherTile)!
        add('moves', homeTile)
        selection.setActiveById(TabType.AGENT, 'moves')
        emitMoveTabToTile(TabType.AGENT, 'moves', otherTile)

        // The source tile's pointer is dead even though the tab is alive and in
        // the same workspace -- without this the tile persists, and then
        // renders, a tab it no longer holds.
        expect(selection.chosenTileActivesFor([homeTile])).toEqual({ [homeTile]: null })
      })
    })
  })
})

/**
 * The "only if unclaimed" rule the close flows share. It used to be copied into
 * three callbacks in `TileRenderer`, each reaching into `state.activeByTile`
 * directly to check it.
 */
describe('claimTileIfUnclaimed', () => {
  it('claims an unclaimed tile for the tab', () => {
    withStores(({ selection, view, layoutStore, rootTileId }) => {
      const heir = layoutStore.splitTile(rootTileId, 'horizontal')!
      // The close flow MOVES the tabs onto the heir and then claims, so the tab
      // is already there by the time the pointer is written -- `activeKeyForTile`
      // heals against the tile's real occupants and would drop a pointer naming
      // a tab that is somewhere else.
      add('a', heir)

      expect(selection.claimTileIfUnclaimed(view.getById(TabType.AGENT, 'a')!, heir)).toBe(true)
      expect(selection.activeKeyForTile(heir)).toBe(key('a'))
    })
  })

  it('leaves a tile that already has a selection alone', () => {
    withStores(({ selection, view, layoutStore, rootTileId }) => {
      const heir = layoutStore.splitTile(rootTileId, 'horizontal')!
      const source = layoutStore.getAllTileIds().find(id => id !== heir)!
      add('held', heir)
      add('incoming', source)
      selection.setActiveInTile(view.getById(TabType.AGENT, 'held')!, heir)

      expect(selection.claimTileIfUnclaimed(view.getById(TabType.AGENT, 'incoming')!, heir)).toBe(false)
      expect(
        selection.activeKeyForTile(heir),
        'a merge must not overwrite a choice the user made',
      ).toBe(key('held'))
    })
  })

  it('never claims the WORKSPACE pointer', () => {
    withStores(({ selection, view, layoutStore, rootTileId }) => {
      const heir = layoutStore.splitTile(rootTileId, 'horizontal')!
      const source = layoutStore.getAllTileIds().find(id => id !== heir)!
      add('reading', source)
      add('arriving', source)
      selection.setActive(view.getById(TabType.AGENT, 'reading')!)

      selection.claimTileIfUnclaimed(view.getById(TabType.AGENT, 'arriving')!, heir)

      expect(
        selection.activeKeyForWorkspace(WS),
        'badging what the user is reading is the whole hazard',
      ).toBe(key('reading'))
    })
  })

  it('is a no-op for a missing tab', () => {
    withStores(({ selection, layoutStore, rootTileId }) => {
      const heir = layoutStore.splitTile(rootTileId, 'horizontal')!
      expect(selection.claimTileIfUnclaimed(undefined, heir)).toBe(false)
      expect(selection.activeKeyForTile(heir)).toBeNull()
    })
  })
})
