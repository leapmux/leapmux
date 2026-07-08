import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from '~/components/shell/tabPersistenceKeys'
import { useTabPersistence } from '~/components/shell/useTabPersistence'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { KEY_ACTIVE_WORKSPACE, sessionStorageGet } from '~/lib/browserStorage'
import { setCRDTBridge } from '~/lib/crdt'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { installTestBridge } from '~/test-support/crdtBridge'

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  sessionStorage.clear()
  setCRDTBridge(null)
})

describe('useTabPersistence', () => {
  // Regression: after the CRDT refactor, the writers that persist the
  // active tab / per-tile active tabs / focused tile to sessionStorage
  // were deleted. The reads in useWorkspaceRestore.ts survived, so on
  // every page refresh the keys are missing and the restore path falls
  // back to activating the first tab in the array.
  it('persists tabStore.activeTabKey to leapmux:activeTab on every change', async () => {
    const wsId = 'ws-persist'
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const [activeWorkspaceId] = createSignal<string | null>(wsId)
          const [workspaceLoading] = createSignal(false)

          useTabPersistence({
            tabStore,
            layoutStore,
            getActiveWorkspaceId: activeWorkspaceId,
            workspaceLoading,
          })

          tabStore.addTab({ type: TabType.AGENT, id: 'first', tileId: 'tile-1' }, { activate: true })
          tabStore.addTab({ type: TabType.AGENT, id: 'second', tileId: 'tile-1' }, { activate: false })
          tabStore.setActiveTab(TabType.AGENT, 'second')

          Promise.resolve().then(() => {
            try {
              expect(sessionStorageGet<string>(activeTabKey(wsId))).toBe(`${TabType.AGENT}:second`)
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  it('persists tabStore.state.tileActiveTabKeys to leapmux:tileActiveTabs', async () => {
    const wsId = 'ws-persist'
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const [activeWorkspaceId] = createSignal<string | null>(wsId)
          const [workspaceLoading] = createSignal(false)

          useTabPersistence({
            tabStore,
            layoutStore,
            getActiveWorkspaceId: activeWorkspaceId,
            workspaceLoading,
          })

          tabStore.addTab({ type: TabType.AGENT, id: 'a', tileId: 'tile-A' }, { activate: true })
          tabStore.addTab({ type: TabType.AGENT, id: 'b', tileId: 'tile-B' }, { activate: false })
          tabStore.setActiveTabForTile('tile-B', TabType.AGENT, 'b')

          Promise.resolve().then(() => {
            try {
              const raw = sessionStorageGet<string>(tileActiveTabsKey(wsId))
              expect(raw, 'tileActiveTabs key present').not.toBeUndefined()
              const parsed = JSON.parse(raw!)
              expect(parsed['tile-A']).toBe(`${TabType.AGENT}:a`)
              expect(parsed['tile-B']).toBe(`${TabType.AGENT}:b`)
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  it('persists layoutStore.focusedTileId to leapmux:focusedTile', async () => {
    const wsId = 'ws-focus'
    const tileId = 'tile-focus'
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          // Real bridge seeds a single LEAF node at `tileId`, so the
          // layout-store's focus invariant accepts setFocusedTile(tileId)
          // instead of snapping back to the fallback leaf.
          installTestBridge({ workspaceId: wsId, rootTileId: tileId })

          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const [activeWorkspaceId] = createSignal<string | null>(wsId)
          const [workspaceLoading] = createSignal(false)

          useTabPersistence({
            tabStore,
            layoutStore,
            getActiveWorkspaceId: activeWorkspaceId,
            workspaceLoading,
          })

          layoutStore.setFocusedTile(tileId)

          Promise.resolve().then(() => {
            try {
              expect(sessionStorageGet<string>(focusedTileKey(wsId))).toBe(tileId)
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  it('persists active workspace id to leapmux:activeWorkspace', async () => {
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const [activeWorkspaceId, setActiveWorkspaceId] = createSignal<string | null>('ws-A')
          const [workspaceLoading] = createSignal(false)

          useTabPersistence({
            tabStore,
            layoutStore,
            getActiveWorkspaceId: activeWorkspaceId,
            workspaceLoading,
          })

          Promise.resolve().then(() => {
            expect(sessionStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBe('ws-A')
            setActiveWorkspaceId('ws-B')
            return Promise.resolve()
          }).then(() => {
            try {
              expect(sessionStorageGet<string>(KEY_ACTIVE_WORKSPACE)).toBe('ws-B')
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })

  // Guards: don't persist while a workspace is still loading (the
  // restore path is mid-clear) and don't persist when there's no
  // active workspace yet.
  it('does not write while workspaceLoading is true', async () => {
    const wsId = 'ws-persist'
    await new Promise<void>((resolve, reject) => {
      createRoot((dispose) => {
        try {
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const [activeWorkspaceId] = createSignal<string | null>(wsId)
          const [workspaceLoading] = createSignal(true)

          useTabPersistence({
            tabStore,
            layoutStore,
            getActiveWorkspaceId: activeWorkspaceId,
            workspaceLoading,
          })

          tabStore.addTab({ type: TabType.AGENT, id: 'a', tileId: 'tile-1' }, { activate: true })
          layoutStore.setFocusedTile('tile-1')

          Promise.resolve().then(() => {
            try {
              expect(sessionStorageGet(activeTabKey(wsId))).toBeUndefined()
              expect(sessionStorageGet(focusedTileKey(wsId))).toBeUndefined()
              expect(sessionStorageGet(tileActiveTabsKey(wsId))).toBeUndefined()
              dispose()
              resolve()
            }
            catch (err) {
              dispose()
              reject(err)
            }
          })
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })
})
