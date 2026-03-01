import type { createAgentStore } from '~/stores/agent.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import { createEffect, on } from 'solid-js'
import { agentClient, terminalClient, workspaceClient } from '~/api/clients'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.store'

interface UseWorkspaceRestoreOpts {
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  agentStore: ReturnType<typeof createAgentStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  setWorkspaceLoading: (v: boolean) => void
}

export function useWorkspaceRestore(opts: UseWorkspaceRestoreOpts) {
  const {
    getActiveWorkspaceId,
    getOrgId,
    agentStore,
    terminalStore,
    tabStore,
    layoutStore,
    setWorkspaceLoading,
  } = opts

  let loadGeneration = 0

  createEffect(on([getActiveWorkspaceId, getOrgId], ([activeId, currentOrgId]) => {
    if (!activeId || !currentOrgId)
      return

    const gen = ++loadGeneration

    setWorkspaceLoading(true)
    tabStore.clear()

    const agentsLoaded = agentClient.listAgents({ workspaceId: activeId })
      .then((resp) => {
        if (gen !== loadGeneration)
          return
        agentStore.setAgents(resp.agents)
      })
      .catch(() => {})

    const terminalsLoaded = terminalClient.listTerminals({ orgId: currentOrgId, workspaceId: activeId })
      .then((resp) => {
        if (gen !== loadGeneration)
          return
        terminalStore.setTerminals([])
        for (const t of resp.terminals) {
          terminalStore.addTerminal({
            id: t.terminalId,
            workspaceId: activeId,
            workerId: t.workerId,
            title: t.title || undefined,
            workingDir: t.workingDir || undefined,
            shellStartDir: t.shellStartDir || undefined,
            screen: t.screen.length > 0 ? t.screen : undefined,
            cols: t.cols || undefined,
            rows: t.rows || undefined,
          })
          if (t.exited) {
            terminalStore.markExited(t.terminalId)
          }
        }
      })
      .catch(() => {})

    const tabsLoaded = workspaceClient.listTabs({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    const layoutLoaded = workspaceClient.getLayout({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    Promise.all([agentsLoaded, terminalsLoaded, tabsLoaded, layoutLoaded]).then(([, , tabsResp, layoutResp]) => {
      if (gen !== loadGeneration)
        return

      tabStore.clear()

      if (layoutResp?.layout) {
        layoutStore.fromProto(layoutResp.layout)
      }
      else {
        layoutStore.initSingleTile()
      }

      const persistedKeys = new Set<string>()
      const tabTileMap = new Map<string, string>()
      if (tabsResp?.tabs) {
        for (const t of tabsResp.tabs) {
          const key = `${t.tabType}:${t.tabId}`
          persistedKeys.add(key)
          if (t.tileId) {
            tabTileMap.set(key, t.tileId)
          }
        }
      }

      const validTileIds = new Set(layoutStore.getAllTileIds())
      const defaultTileId = layoutStore.focusedTileId()

      for (const a of agentStore.state.agents) {
        if (a.status === AgentStatus.ACTIVE || persistedKeys.has(`${TabType.AGENT}:${a.id}`)) {
          const key = `${TabType.AGENT}:${a.id}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({ type: TabType.AGENT, id: a.id, title: a.title || undefined, tileId, workerId: a.workerId, workingDir: a.workingDir }, false)
        }
      }

      for (const t of terminalStore.state.terminals) {
        if (!terminalStore.isExited(t.id) || persistedKeys.has(`${TabType.TERMINAL}:${t.id}`)) {
          const key = `${TabType.TERMINAL}:${t.id}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({ type: TabType.TERMINAL, id: t.id, tileId, workerId: t.workerId, workingDir: t.workingDir }, false)
        }
      }

      if (tabsResp && tabsResp.tabs.length > 0) {
        const posMap = new Map<string, string>()
        for (const t of tabsResp.tabs) {
          posMap.set(`${t.tabType}:${t.tabId}`, t.position)
        }
        tabStore.sortByPositions(posMap)
      }

      try {
        const localTabsJson = sessionStorage.getItem(`leapmux:localTabs:${activeId}`)
        if (localTabsJson) {
          const localTabs = JSON.parse(localTabsJson) as Array<{
            type: number
            id: string
            filePath?: string
            workerId?: string
            position?: string
            tileId?: string
            title?: string
            displayMode?: string
          }>
          for (const lt of localTabs) {
            let tileId = lt.tileId ?? defaultTileId
            if (!validTileIds.has(tileId))
              tileId = defaultTileId
            tabStore.addTab({
              type: lt.type as TabType,
              id: lt.id,
              filePath: lt.filePath,
              workerId: lt.workerId,
              position: lt.position,
              tileId,
              title: lt.title,
              displayMode: lt.displayMode,
            }, false)
          }
        }
      }
      catch {
        // Ignore corrupt sessionStorage data
      }

      if (layoutResp?.activeTabs && layoutResp.activeTabs.length > 0) {
        for (const at of layoutResp.activeTabs) {
          tabStore.setActiveTabForTile(at.tileId, at.tabType, at.tabId)
        }
      }

      const savedKey = sessionStorage.getItem(`leapmux:activeTab:${activeId}`)
      if (savedKey && tabStore.state.tabs.some(t => tabKey(t) === savedKey)) {
        const parts = savedKey.split(':')
        const tabType = Number(parts[0]) as TabType
        const tabId = parts[1]
        tabStore.setActiveTab(tabType, tabId)
        const restoredTab = tabStore.state.tabs.find(t => tabKey(t) === savedKey)
        if (restoredTab?.tileId) {
          tabStore.setActiveTabForTile(restoredTab.tileId, tabType, tabId)
        }
        if (tabType === TabType.AGENT) {
          agentStore.setActiveAgent(tabId)
        }
        else if (tabType === TabType.TERMINAL) {
          terminalStore.setActiveTerminal(tabId)
        }
      }
      else if (tabStore.state.tabs.length > 0) {
        const firstTab = tabStore.state.tabs[0]
        tabStore.setActiveTab(firstTab.type, firstTab.id)
        if (firstTab.tileId) {
          tabStore.setActiveTabForTile(firstTab.tileId, firstTab.type, firstTab.id)
        }
        if (firstTab.type === TabType.AGENT) {
          agentStore.setActiveAgent(firstTab.id)
        }
      }

      setWorkspaceLoading(false)
    })
  }))
}
