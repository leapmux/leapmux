import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { createEffect, onCleanup } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { floatingWindowsToProto } from '~/stores/floatingWindow.store'
import { toProto } from '~/stores/layout.store'

interface UseTabPersistenceOpts {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: FloatingWindowStoreType
  registry: WorkspaceStoreRegistryType
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  activeWorkspace: () => Workspace | null
  workspaceLoading: () => boolean
}

export function useTabPersistence(opts: UseTabPersistenceOpts) {
  const {
    tabStore,
    layoutStore,
    getActiveWorkspaceId,
    getOrgId,
    activeWorkspace,
    workspaceLoading,
  } = opts

  // Debounced layout + tab persistence
  let layoutSaveTimer: ReturnType<typeof setTimeout> | null = null
  let layoutSaveDirty = false

  function doLayoutSave() {
    layoutSaveDirty = false
    const ws = activeWorkspace()
    if (!ws || workspaceLoading())
      return

    const tabs = tabStore.state.tabs
      .filter(t => t.type !== TabType.FILE)
      .map(t => ({
        tabType: t.type,
        tabId: t.id,
        position: t.position ?? '',
        tileId: t.tileId ?? '',
        workerId: t.workerId ?? '',
      }))

    workspaceClient.saveLayout({
      orgId: getOrgId(),
      workspaceId: ws.id,
      layout: layoutStore.toProto(),
      tabs,
      floatingWindows: opts.floatingWindowStore?.toProto() ?? [],
    }).then(() => {
      // Dispatch a custom event so E2E tests can detect layout save completion.
      window.dispatchEvent(new CustomEvent('leapmux:layout-saved'))
    }).catch(() => {})
  }

  const persistLayout = () => {
    layoutSaveDirty = true
    if (layoutSaveTimer)
      clearTimeout(layoutSaveTimer)
    layoutSaveTimer = setTimeout(doLayoutSave, 500)
  }

  // Persist active tab to sessionStorage
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    const wsId = getActiveWorkspaceId()
    if (wsId && activeKey && !workspaceLoading()) {
      sessionStorage.setItem(`leapmux:activeTab:${wsId}`, activeKey)
    }
  })

  // Persist per-tile active tabs to sessionStorage
  createEffect(() => {
    const tileActiveTabKeys = tabStore.state.tileActiveTabKeys
    const wsId = getActiveWorkspaceId()
    if (wsId && !workspaceLoading()) {
      const entries = Object.entries(tileActiveTabKeys).filter(([, v]) => v != null)
      if (entries.length > 0) {
        sessionStorage.setItem(`leapmux:tileActiveTabs:${wsId}`, JSON.stringify(Object.fromEntries(entries)))
      }
      else {
        sessionStorage.removeItem(`leapmux:tileActiveTabs:${wsId}`)
      }
    }
  })

  // Persist focused tile to sessionStorage
  createEffect(() => {
    const focusedTileId = layoutStore.focusedTileId()
    const wsId = getActiveWorkspaceId()
    if (wsId && focusedTileId && !workspaceLoading()) {
      sessionStorage.setItem(`leapmux:focusedTile:${wsId}`, focusedTileId)
    }
  })

  // Persist ephemeral (local) tabs to sessionStorage
  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    const tabs = tabStore.state.tabs
    if (!wsId || workspaceLoading())
      return
    const localTabs = tabs
      .filter(t => t.type === TabType.FILE)
      .map(t => ({
        type: t.type,
        id: t.id,
        filePath: t.filePath,
        workerId: t.workerId,
        position: t.position,
        tileId: t.tileId,
        title: t.title,
        displayMode: t.displayMode,
        fileViewMode: t.fileViewMode,
        fileDiffBase: t.fileDiffBase,
      }))
    if (localTabs.length > 0) {
      sessionStorage.setItem(`leapmux:localTabs:${wsId}`, JSON.stringify(localTabs))
    }
    else {
      sessionStorage.removeItem(`leapmux:localTabs:${wsId}`)
    }
  })

  // Persist active workspace to sessionStorage
  createEffect(() => {
    const wsId = getActiveWorkspaceId()
    if (wsId && !workspaceLoading()) {
      sessionStorage.setItem('leapmux:activeWorkspace', wsId)
    }
  })

  // Multi-workspace layout persistence: saves layout + tabs for all
  // workspaces that have cached snapshots in the registry, plus the
  // active workspace.
  let multiSaveTimer: ReturnType<typeof setTimeout> | null = null
  let multiSaveDirty = false

  function doMultiSave() {
    multiSaveDirty = false
    const orgId = getOrgId()
    if (!orgId || workspaceLoading())
      return

    const { registry } = opts
    const entries: Array<{
      workspaceId: string
      layout: ReturnType<typeof layoutStore.toProto>
      tabs: Array<{ tabType: number, tabId: string, position: string, tileId: string, workerId: string }>
      floatingWindows?: ReturnType<NonNullable<typeof opts.floatingWindowStore>['toProto']>
    }> = []

    // Active workspace: use live stores
    const ws = activeWorkspace()
    if (ws) {
      entries.push({
        workspaceId: ws.id,
        layout: layoutStore.toProto(),
        tabs: tabStore.state.tabs
          .filter(t => t.type !== TabType.FILE)
          .map(t => ({
            tabType: t.type,
            tabId: t.id,
            position: t.position ?? '',
            tileId: t.tileId ?? '',
            workerId: t.workerId ?? '',
          })),
        floatingWindows: opts.floatingWindowStore?.toProto() ?? [],
      })
    }

    // Other cached workspaces from registry (include both restored and
    // tabsLoaded snapshots — cross-workspace moves add tabs to non-restored
    // snapshots that still need to be persisted to the hub).
    for (const snap of registry.all()) {
      if (ws && snap.workspaceId === ws.id)
        continue
      if (!snap.restored && !snap.tabsLoaded)
        continue
      const snapLayout = snap.layout
      // Convert floating windows from snapshot to proto
      const snapFloatingWindows = snap.floatingWindows
        ? floatingWindowsToProto(snap.floatingWindows.windows)
        : []
      entries.push({
        workspaceId: snap.workspaceId,
        layout: toProto(snapLayout.root),
        tabs: snap.tabs.tabs
          .filter(t => t.type !== TabType.FILE)
          .map(t => ({
            tabType: t.type,
            tabId: t.id,
            position: t.position ?? '',
            tileId: t.tileId ?? '',
            workerId: t.workerId ?? '',
          })),
        floatingWindows: snapFloatingWindows,
      })
    }

    if (entries.length === 0)
      return

    workspaceClient.saveMultiLayout({
      orgId,
      entries,
    }).then(() => {
      window.dispatchEvent(new CustomEvent('leapmux:layout-saved'))
    }).catch(() => {})
  }

  const persistMultiLayout = (immediate?: boolean) => {
    if (immediate) {
      if (multiSaveTimer) {
        clearTimeout(multiSaveTimer)
        multiSaveTimer = null
      }
      multiSaveDirty = false
      doMultiSave()
      return
    }
    multiSaveDirty = true
    if (multiSaveTimer)
      clearTimeout(multiSaveTimer)
    multiSaveTimer = setTimeout(doMultiSave, 500)
  }

  // Flush any pending saves on page unload so cross-workspace moves
  // aren't lost if the user refreshes before the debounce fires.
  const flushOnUnload = () => {
    if (multiSaveDirty) {
      if (multiSaveTimer) {
        clearTimeout(multiSaveTimer)
        multiSaveTimer = null
      }
      doMultiSave()
    }
    if (layoutSaveDirty) {
      if (layoutSaveTimer) {
        clearTimeout(layoutSaveTimer)
        layoutSaveTimer = null
      }
      doLayoutSave()
    }
  }
  window.addEventListener('beforeunload', flushOnUnload)
  onCleanup(() => window.removeEventListener('beforeunload', flushOnUnload))

  return { persistLayout, persistMultiLayout }
}
