import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import { createEffect } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

interface UseTabPersistenceOpts {
  tabStore: ReturnType<typeof createTabStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  activeWorkspace: () => Workspace | null
  workspaceLoading: () => boolean
}

export function useTabPersistence(opts: UseTabPersistenceOpts) {
  const {
    tabStore,
    terminalStore,
    layoutStore,
    getActiveWorkspaceId,
    getOrgId,
    activeWorkspace,
    workspaceLoading,
  } = opts

  // Debounced layout + tab persistence
  let layoutSaveTimer: ReturnType<typeof setTimeout> | null = null
  const persistLayout = () => {
    const ws = activeWorkspace()
    if (!ws || workspaceLoading())
      return
    if (layoutSaveTimer)
      clearTimeout(layoutSaveTimer)
    layoutSaveTimer = setTimeout(() => {
      const tileIds = layoutStore.getAllTileIds()
      const activeTabs = tileIds.map((tileId) => {
        const activeKey = tabStore.getActiveTabKeyForTile(tileId)
        if (!activeKey)
          return null
        const parts = activeKey.split(':')
        const tabType = Number(parts[0]) as TabType
        if (tabType === TabType.FILE)
          return null
        return { tileId, tabType, tabId: parts[1] }
      }).filter(Boolean) as Array<{ tileId: string, tabType: TabType, tabId: string }>

      const tabs = tabStore.state.tabs
        .filter(t => t.type !== TabType.FILE)
        .map(t => ({
          tabType: t.type,
          tabId: t.id,
          position: t.position ?? '',
          tileId: t.tileId ?? '',
          workingDir: t.workingDir ?? '',
          shellStartDir: t.type === TabType.TERMINAL
            ? (terminalStore.state.terminals.find(term => term.id === t.id)?.shellStartDir ?? '')
            : '',
        }))

      workspaceClient.saveLayout({
        orgId: getOrgId(),
        workspaceId: ws.id,
        layout: layoutStore.toProto(),
        activeTabs,
        tabs,
      }).catch(() => {})
    }, 500)
  }

  // Persist active tab to sessionStorage
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    const wsId = getActiveWorkspaceId()
    if (wsId && activeKey && !workspaceLoading()) {
      sessionStorage.setItem(`leapmux:activeTab:${wsId}`, activeKey)
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

  return { persistLayout }
}
