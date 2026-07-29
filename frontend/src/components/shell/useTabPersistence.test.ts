/// <reference types="vitest/globals" />
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from '~/components/shell/tabPersistenceKeys'
import { useTabPersistence } from '~/components/shell/useTabPersistence'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { setCRDTBridge } from '~/lib/crdt'
import { emitAddTab } from '~/stores/tabOps'
import { withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  sessionStorage.clear()
  setCRDTBridge(null)
})

const WS = 'ws-persist'

/**
 * Mount the persistence writers over a real bridge, run `body`, then flush a
 * microtask so the effects have written before the assertions run.
 */
async function withPersistence(
  body: (ctx: ReturnType<typeof createTestTabStores> & { rootTileId: string }) => void,
  opts: { loading?: boolean } = {},
): Promise<void> {
  await withTestBridge(async (harness) => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        const stores = createTestTabStores(WS)
        // `loading` here means "the bootstrap has not delivered this workspace
        // yet", which is exactly what the writers now gate on.
        const [bootstrapped] = createSignal(!(opts.loading ?? false))
        useTabPersistence({
          selection: stores.selection,
          layoutStore: stores.layoutStore,
          floatingWindowStore: stores.floatingWindowStore,
          getActiveWorkspaceId: () => WS,
          hasWorkspace: () => bootstrapped(),
        })
        try {
          body({ ...stores, rootTileId: harness.rootTileId })
        }
        catch (err) {
          dispose()
          reject(err)
          return
        }
        // Let the persistence effects run before we read sessionStorage.
        queueMicrotask(() => {
          dispose()
          resolve()
        })
      })
    })
  }, { workspaceId: WS })
}

/**
 * These three pointers are per-client and never enter the CRDT, so
 * sessionStorage is the only thing carrying them across a page RELOAD. Within a
 * session `tabSelection` holds them and they now survive a workspace switch on
 * their own -- they used to ride along in the registry snapshot.
 *
 * Which workspace is active is NOT persisted here; that moved to
 * `createWorkspaceSwitcher`, which writes it to localStorage keyed by user id.
 */
describe('useTabPersistence', () => {
  it('persists the workspace active tab key', async () => {
    await withPersistence(({ selection, view, rootTileId }) => {
      emitAddTab({ type: TabType.AGENT, id: 'first', tileId: rootTileId, position: 'a' })
      emitAddTab({ type: TabType.AGENT, id: 'second', tileId: rootTileId, position: 'b' })
      selection.setActiveById(TabType.AGENT, 'second')
      expect(view.forWorkspace(WS)).toHaveLength(2)
    })
    expect(sessionStorageGet<string>(activeTabKey(WS))).toBe(`${TabType.AGENT}:second`)
  })

  it('persists per-tile active tabs', async () => {
    await withPersistence(({ selection, rootTileId }) => {
      emitAddTab({ type: TabType.AGENT, id: 'a', tileId: rootTileId, position: 'a' })
      selection.setActiveById(TabType.AGENT, 'a')
    })
    const raw = sessionStorageGet<string>(tileActiveTabsKey(WS))
    expect(raw, 'tileActiveTabs key present').not.toBeUndefined()
    expect(Object.values(JSON.parse(raw!))).toContain(`${TabType.AGENT}:a`)
  })

  it('persists the focused tile', async () => {
    let rootTileId = ''
    await withPersistence((ctx) => {
      rootTileId = ctx.rootTileId
      ctx.layoutStore.setFocusedTile(ctx.rootTileId)
    })
    expect(sessionStorageGet<string>(focusedTileKey(WS))).toBe(rootTileId)
  })

  /**
   * `focusedTileId()` synthesises `firstLeafId(projectedRoot())` when nothing
   * has been focused; `focusedTileIdFor` deliberately does not, because — in
   * its own words — "a synthesised answer would be a wrong yes". Persisting the
   * synthesised one records a choice the user never made, and restores it next
   * load as though they had.
   */
  it('writes nothing when the user has not focused a tile', async () => {
    await withPersistence(({ selection, rootTileId }) => {
      emitAddTab({ type: TabType.AGENT, id: 'a', tileId: rootTileId, position: 'a' })
      selection.setActiveById(TabType.AGENT, 'a')
      // Deliberately no setFocusedTile: `focusedTileId()` would still answer
      // with the first leaf, which is exactly what must not be persisted.
    })
    expect(sessionStorageGet(activeTabKey(WS)), 'the active tab IS a real choice').not.toBeUndefined()
    expect(sessionStorageGet(focusedTileKey(WS)), 'focus was never chosen').toBeUndefined()
  })

  // The restore path is mid-clear while a workspace loads; writing then would
  // stomp the very keys it is about to read back.
  it('does not write before the bootstrap delivers the workspace', async () => {
    await withPersistence(({ selection, layoutStore, rootTileId }) => {
      emitAddTab({ type: TabType.AGENT, id: 'a', tileId: rootTileId, position: 'a' })
      selection.setActiveById(TabType.AGENT, 'a')
      layoutStore.setFocusedTile(rootTileId)
    }, { loading: true })
    expect(sessionStorageGet(activeTabKey(WS))).toBeUndefined()
    expect(sessionStorageGet(focusedTileKey(WS))).toBeUndefined()
    expect(sessionStorageGet(tileActiveTabsKey(WS))).toBeUndefined()
  })

  // Regression: the writers used to share `workspaceLoading` with the paint
  // gate, which a watchdog clears on a timer when the bootstrap is slow or never
  // arrives. That timer then authorised these writes against an EMPTY
  // projection, and both derived effects fall through to `clearIfPresent` --
  // deleting last session's keys while `restoreTabSelection`, gated on the
  // projection, had not yet spent its one attempt to read them. Gating on the
  // same predicate as the restore is what makes the writer unable to outrun it.
  it('does not delete a previous session\'s keys when the bootstrap has not landed', async () => {
    sessionStorageSet(activeTabKey(WS), `${TabType.AGENT}:from-last-session`)
    sessionStorageSet(tileActiveTabsKey(WS), JSON.stringify({ 'tile-1': `${TabType.AGENT}:from-last-session` }))
    // No tabs and no projection for this workspace -- exactly the state the
    // watchdog used to open the writers against.
    await withPersistence(({ view }) => {
      expect(view.forWorkspace(WS), 'the projection really is empty').toHaveLength(0)
    }, { loading: true })
    expect(sessionStorageGet(activeTabKey(WS))).toBe(`${TabType.AGENT}:from-last-session`)
    expect(sessionStorageGet(tileActiveTabsKey(WS))).not.toBeUndefined()
  })

  // Closing the last tab must ERASE the pointer, not leave it naming a dead tab:
  // the next load's `restoreTabSelection` reads `hasStoredState` off it and
  // spends its one restore attempt applying a previous session's selection.
  it('clears the workspace active-tab key once the workspace has no tabs', async () => {
    sessionStorageSet(activeTabKey(WS), `${TabType.AGENT}:gone`)
    await withPersistence(({ view }) => {
      expect(view.forWorkspace(WS), 'the workspace really is empty').toHaveLength(0)
    })
    expect(sessionStorageGet(activeTabKey(WS))).toBeUndefined()
  })

  // The focus pointer is a user CHOICE that outlives the tab set -- tiles do not
  // vanish when their tabs do -- so "nothing focused this session" must not
  // delete what the previous session recorded. Pins the asymmetry above so a
  // later "make them all symmetric" pass cannot silently break it.
  it('does NOT clear the focused-tile key just because nothing is focused yet', async () => {
    sessionStorageSet(focusedTileKey(WS), 'tile-from-last-session')
    await withPersistence(() => {})
    expect(sessionStorageGet(focusedTileKey(WS))).toBe('tile-from-last-session')
  })

  // `activeByTile` is keyed by globally-unique tile id and therefore now spans
  // every workspace at once. Persisting it unnarrowed would write other
  // workspaces' tiles under this workspace's key, and the reader would restore
  // them into a tree that has no such tile.
  it('persists only the tiles belonging to this workspace', async () => {
    await withPersistence(({ selection, rootTileId }) => {
      emitAddTab({ type: TabType.AGENT, id: 'a', tileId: rootTileId, position: 'a' })
      selection.setActiveById(TabType.AGENT, 'a')
      // A tile id from some other workspace's tree.
      selection.restore('other-ws', null, { 'tile-elsewhere': `${TabType.AGENT}:elsewhere` })
    })
    const parsed = JSON.parse(sessionStorageGet<string>(tileActiveTabsKey(WS))!)
    expect(parsed['tile-elsewhere']).toBeUndefined()
  })

  // Floating windows own tiles too, and they belong to THIS workspace.
  // Narrowing to `layoutStore.getAllTileIds()` alone (the main tree) dropped
  // them, so a window reopened on its first tab after a reload.
  it('persists a floating window tile active tab', async () => {
    let windowTileId = ''
    await withPersistence(({ selection, floatingWindowStore, rootTileId }) => {
      const win = floatingWindowStore.addWindow()
      expect(win, 'the test bridge must mint a floating window').toBeTruthy()
      windowTileId = win!.tileId
      emitAddTab({ type: TabType.AGENT, id: 'main', tileId: rootTileId, position: 'a' })
      emitAddTab({ type: TabType.AGENT, id: 'floating', tileId: windowTileId, position: 'b' })
      selection.setActiveById(TabType.AGENT, 'main')
      selection.setActiveById(TabType.AGENT, 'floating')
    })
    const parsed = JSON.parse(sessionStorageGet<string>(tileActiveTabsKey(WS))!)
    expect(parsed[windowTileId]).toBe(`${TabType.AGENT}:floating`)
  })
})
