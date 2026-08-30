/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { sessionStorageSet } from '~/lib/browserStorage'
import { getCRDTBridge, setCRDTBridge } from '~/lib/crdt'
import { emitSplitTile } from '~/stores/layoutOps'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { createTabSelectionRestorer } from './restoreTabSelection'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from './tabPersistenceKeys'

const WS = 'ws-test'
const key = (id: string) => `${TabType.AGENT}:${id}`

beforeEach(() => sessionStorage.clear())
afterEach(() => {
  sessionStorage.clear()
  setCRDTBridge(null)
})

/**
 * The read half of the reload-continuity pair.
 *
 * `useTabPersistence` writes these keys on every change; nothing read them back
 * for a while, which made the writer pure overhead and sent every refresh to
 * whichever tab the MRU fallback picked. These tests pin the round trip.
 */
function setup() {
  const harness = installTestBridge({ workspaceId: WS })
  const stores = createTestTabStores(WS)
  let seq = 0
  return {
    ...stores,
    harness,
    rootTileId: harness.rootTileId,
    add(id: string, tileId = harness.rootTileId) {
      seq += 1
      emitAddTab({ type: TabType.AGENT, id, tileId, position: `p${seq}`, workerId: 'w' })
    },
    restore: createTabSelectionRestorer({
      selection: stores.selection,
      layoutStore: stores.layoutStore,
      view: stores.view,
      // Same expression AppShell passes, off the same projection memo.
      hasWorkspace: wsId => Boolean(stores.projection()?.workspaces.has(wsId)),
    }),
  }
}

describe('createTabSelectionRestorer', () => {
  it('restores the workspace active tab', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      s.add('a2')
      sessionStorageSet(activeTabKey(WS), key('a2'))

      s.restore(WS)

      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a2'))
      dispose()
    })
  })

  // The sidebar's "click a tab in a non-active workspace" path writes ONLY the
  // workspace-level key and then switches; the tab bar reads the per-TILE
  // pointer. Restoring one without the other lands the user on whichever tab
  // the MRU fallback picks, which is the tab they didn't click.
  it('points the tab own tile at it when restoring the workspace active tab', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      s.add('a2')
      sessionStorageSet(activeTabKey(WS), key('a2'))

      s.restore(WS)

      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a2'))
      expect(s.selection.activeKeyForTile(s.rootTileId)).toBe(key('a2'))
      dispose()
    })
  })

  it('leaves the tile pointer alone when the stored tab no longer exists', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(activeTabKey(WS), key('closed-elsewhere'))

      expect(() => s.restore(WS)).not.toThrow()
      // Falls through to the healing fallback rather than pinning a dead key.
      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a1'))
      dispose()
    })
  })

  it('restores per-tile active tabs', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(tileActiveTabsKey(WS), JSON.stringify({ [s.rootTileId]: key('a1') }))

      s.restore(WS)

      expect(s.selection.activeKeyForTile(s.rootTileId)).toBe(key('a1'))
      dispose()
    })
  })

  it('restores the focused tile', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(focusedTileKey(WS), s.rootTileId)

      s.restore(WS)

      expect(s.layoutStore.focusedTileId()).toBe(s.rootTileId)
      dispose()
    })
  })

  // A second call must not undo selections the user has made since — the
  // stored value is a snapshot of the last session, not a standing preference.
  it('restores a workspace only once', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      s.add('a2')
      sessionStorageSet(activeTabKey(WS), key('a1'))

      s.restore(WS)
      s.selection.setActiveById(TabType.AGENT, 'a2')
      s.restore(WS)

      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a2'))
      dispose()
    })
  })

  // The sidebar's click-into-another-workspace path selects the tab and THEN
  // switches, so the restore effect fires with a choice already made. Restoring
  // over it would send the user to whichever tab was active there last time --
  // the exact bug the sessionStorage handoff used to paper over.
  it('does not override a selection made before the workspace became active', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      s.add('a2')
      sessionStorageSet(activeTabKey(WS), key('a1'))

      s.selection.setActiveById(TabType.AGENT, 'a2')
      s.restore(WS)

      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a2'))
      dispose()
    })
  })

  it('does nothing when nothing was stored', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')

      s.restore(WS)

      // The healing fallback answers instead — not the stored value, because
      // there isn't one.
      expect(s.selection.state.activeByWorkspace[WS]).toBeUndefined()
      dispose()
    })
  })

  // A hand-edited or half-written entry must not throw during startup: a
  // crash here would take the whole shell down before it rendered.
  it('survives a malformed tileActiveTabs payload', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(tileActiveTabsKey(WS), '{not json')
      sessionStorageSet(activeTabKey(WS), key('a1'))

      expect(() => s.restore(WS)).not.toThrow()
      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a1'))
      dispose()
    })
  })

  it('ignores a tileActiveTabs payload that is not an object', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(tileActiveTabsKey(WS), JSON.stringify(['nope']))

      expect(() => s.restore(WS)).not.toThrow()
      dispose()
    })
  })

  it('keeps workspaces independent', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(activeTabKey(WS), key('a1'))
      sessionStorageSet(activeTabKey('other-ws'), key('elsewhere'))

      s.restore(WS)

      expect(s.selection.state.activeByWorkspace['other-ws']).toBeUndefined()
      dispose()
    })
  })

  // The stored id can name a tile that a peer closed while this tab was away.
  // `layoutStore`'s own focus invariant is what rejects it; restoring must not
  // assume the id is still live.
  it('does not crash on a focused tile that no longer exists', () => {
    createRoot((dispose) => {
      const s = setup()
      s.add('a1')
      sessionStorageSet(focusedTileKey(WS), 'tile-that-is-gone')

      expect(() => s.restore(WS)).not.toThrow()
      dispose()
    })
  })

  /**
   * The restore runs off `activeWorkspaceId`, which comes from the
   * `listWorkspaces` HTTP response; the CRDT bootstrap arrives separately over
   * the userevents socket and normally lands later. Consuming the one-shot flag
   * against an empty projection burned the only attempt and left the user on
   * whatever the fallback picked.
   */
  describe('waits for the projection', () => {
    it('does not consume the attempt before the bootstrap delivers the workspace', () => {
      createRoot((dispose) => {
        const s = setup()
        const LATE = 'ws-late'
        sessionStorageSet(activeTabKey(LATE), key('a1'))

        // `activeWorkspaceId` comes from the `listWorkspaces` HTTP response,
        // which can name a workspace the CRDT bootstrap has not delivered yet.
        s.restore(LATE)
        expect(s.selection.state.activeByWorkspace[LATE]).toBeUndefined()

        // The bootstrap lands; the next pass must still restore.
        seedWorkspace(s.harness, LATE, 'late-tile')
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: 'late-tile', position: 'p1', workerId: 'w' })
        s.restore(LATE)
        expect(s.selection.state.activeByWorkspace[LATE]).toBe(key('a1'))
        dispose()
      })
    })

    // The gate asks whether the WORKSPACE arrived, not whether it has tabs.
    // Keying it on the tab count made a workspace whose tabs were all closed
    // last session permanently un-restorable: the early return fired on every
    // tick, the one-shot never ran, and the tile the user deliberately focused
    // before reloading was never restored. `useTabPersistence` deliberately
    // keeps writing `focusedTile` for an empty workspace precisely because it
    // "persists a user CHOICE that outlives the tab set", so the read side has
    // to honour it.
    it('restores the focused tile of a workspace that has no tabs left', () => {
      createRoot((dispose) => {
        const s = setup()
        const split = emitSplitTile(getCRDTBridge()!, s.rootTileId, 'vertical')!
        sessionStorageSet(focusedTileKey(WS), split.childB)

        s.restore(WS)

        expect(s.layoutStore.focusedTileId()).toBe(split.childB)
        dispose()
      })
    })

    // A workspace with nothing stored has nothing to wait for; it must not
    // retry forever.
    it('settles immediately when there is nothing stored', () => {
      createRoot((dispose) => {
        const s = setup()
        s.restore(WS)
        // A later call is a no-op even once tabs exist, because the workspace
        // was already marked restored.
        s.add('a1')
        sessionStorageSet(activeTabKey(WS), key('a1'))
        s.restore(WS)
        expect(s.selection.state.activeByWorkspace[WS]).toBeUndefined()
        dispose()
      })
    })
  })
  /**
   * The stored tab can be projected in a LATER frame than its workspace's first
   * tab. The emptiness gate is satisfied by whichever tab lands first, so the
   * one-shot is consumed while `selection.restore`'s own-tile back-fill silently
   * no-ops — and the `restored.has` early return then exits before the reactive
   * `forWorkspace` read, so nothing ever runs it again.
   */
  it('back-fills the tab own tile when the stored tab arrives after the first pass', () => {
    createRoot((dispose) => {
      const s = setup()
      const split = emitSplitTile(getCRDTBridge()!, s.rootTileId, 'horizontal')!
      s.add('a1')
      s.add('a3', split.childB)
      sessionStorageSet(activeTabKey(WS), key('a2'))

      // a2 is not projected yet; a1/a3 are, so the emptiness gate passes.
      s.restore(WS)
      expect(s.selection.activeKeyForTile(split.childB), 'falls through to MRU').toBe(key('a3'))

      s.add('a2', split.childB)
      s.restore(WS)

      expect(s.selection.activeKeyForTile(split.childB)).toBe(key('a2'))
      expect(s.selection.activeKeyForWorkspace(WS)).toBe(key('a2'))
      dispose()
    })
  })

  it('abandons the back-fill once the user has chosen something else', () => {
    createRoot((dispose) => {
      const s = setup()
      const split = emitSplitTile(getCRDTBridge()!, s.rootTileId, 'horizontal')!
      s.add('a1')
      s.add('a3', split.childB)
      sessionStorageSet(activeTabKey(WS), key('a2'))
      s.restore(WS)

      // The user clicks a3 while the stored tab is still absent.
      s.selection.setActiveById(TabType.AGENT, 'a3')
      s.add('a2', split.childB)
      s.restore(WS)

      expect(s.selection.activeKeyForWorkspace(WS), 'the live choice wins').toBe(key('a3'))
      expect(s.selection.activeKeyForTile(split.childB)).toBe(key('a3'))
      dispose()
    })
  })
})
