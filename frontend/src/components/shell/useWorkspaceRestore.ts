import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { createEffect, on } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { tabKey } from '~/stores/tab.store'

const log = createLogger('restore')

interface UseWorkspaceRestoreOpts {
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  agentStore: ReturnType<typeof createAgentStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  chatStore: ReturnType<typeof createChatStore>
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  registry: WorkspaceStoreRegistryType
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
    registry,
    setWorkspaceLoading,
  } = opts

  let loadGeneration = 0
  let previousWorkspaceId: string | null = null

  createEffect(on([getActiveWorkspaceId, getOrgId], ([activeId, currentOrgId]) => {
    if (!activeId || !currentOrgId)
      return

    const gen = ++loadGeneration

    // Save current workspace state to registry before switching.
    if (previousWorkspaceId && previousWorkspaceId !== activeId) {
      registry.set(previousWorkspaceId, {
        workspaceId: previousWorkspaceId,
        tabs: tabStore.snapshot(),
        layout: layoutStore.snapshot(),
        agents: [...agentStore.state.agents],
        terminals: [...terminalStore.state.terminals],
        restored: true,
        tabsLoaded: true,
      })
    }
    previousWorkspaceId = activeId

    // Check if we have a cached snapshot for this workspace.
    const cached = registry.get(activeId)
    if (cached?.restored) {
      tabStore.restore(cached.tabs)
      layoutStore.restore(cached.layout)
      agentStore.setAgents(cached.agents)
      terminalStore.setTerminals(cached.terminals)

      // Activate the tab the user clicked in the sidebar (if any).
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

      setWorkspaceLoading(false)
      return
    }

    setWorkspaceLoading(true)
    tabStore.clear()

    // Fetch tabs and layout from hub (single call, no worker needed).
    const tabsLoaded = workspaceClient.listTabs({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    const layoutLoaded = workspaceClient.getLayout({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    const loadTimeout = new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error('Workspace load timed out after 30s')), 30_000),
    )

    Promise.race([
      Promise.all([tabsLoaded, layoutLoaded]),
      loadTimeout,
    ]).then(async ([tabsResp, layoutResp]) => {
      if (gen !== loadGeneration)
        return

      // Group agent/terminal tab IDs by worker from the hub's tab list.
      const agentIdsByWorker = new Map<string, string[]>()
      const terminalIdsByWorker = new Map<string, string[]>()
      const tabTileMap = new Map<string, string>()
      if (tabsResp?.tabs) {
        for (const t of tabsResp.tabs) {
          if (t.tileId) {
            tabTileMap.set(`${t.tabType}:${t.tabId}`, t.tileId)
          }
          if (!t.workerId)
            continue
          if (t.tabType === TabType.AGENT) {
            const ids = agentIdsByWorker.get(t.workerId) ?? []
            ids.push(t.tabId)
            agentIdsByWorker.set(t.workerId, ids)
          }
          else if (t.tabType === TabType.TERMINAL) {
            const ids = terminalIdsByWorker.get(t.workerId) ?? []
            ids.push(t.tabId)
            terminalIdsByWorker.set(t.workerId, ids)
          }
        }
      }

      // Fetch agents and terminals from each worker by tab IDs.
      const agentResults = await Promise.all(
        [...agentIdsByWorker.entries()].map(async ([workerId, tabIds]) => {
          try {
            const resp = await workerRpc.listAgents(workerId, { tabIds })
            return resp.agents
          }
          catch (err) {
            log.warn('failed to list agents from worker', { workerId, tabIds, err })
            return []
          }
        }),
      )

      const terminalResults = await Promise.all(
        [...terminalIdsByWorker.entries()].map(async ([workerId, tabIds]) => {
          try {
            const resp = await workerRpc.listTerminals(workerId, { tabIds })
            return { workerId, terminals: resp.terminals }
          }
          catch (err) {
            log.warn('failed to list terminals from worker', { workerId, tabIds, err })
            return { workerId, terminals: [] as Awaited<ReturnType<typeof workerRpc.listTerminals>>['terminals'] }
          }
        }),
      )

      if (gen !== loadGeneration)
        return

      // Populate agent store.
      const filteredAgents = agentResults.flat()
      // Merge locally-moved agents from the registry snapshot that the
      // worker hasn't returned yet (cross-workspace move may still be
      // in flight on the worker side).
      if (cached && cached.agents.length > 0) {
        const fetchedIds = new Set(filteredAgents.map(a => a.id))
        for (const snapAgent of cached.agents) {
          if (!fetchedIds.has(snapAgent.id)) {
            filteredAgents.push(snapAgent)
          }
        }
      }
      agentStore.setAgents(filteredAgents)

      // Populate terminal store.
      terminalStore.setTerminals([])
      for (const { workerId, terminals } of terminalResults) {
        for (const t of terminals) {
          terminalStore.addTerminal({
            id: t.terminalId,
            workspaceId: activeId,
            workerId,
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
      }

      tabStore.clear()

      if (layoutResp?.layout) {
        layoutStore.fromProto(layoutResp.layout)
      }
      else {
        layoutStore.initSingleTile()
      }

      const validTileIds = new Set(layoutStore.getAllTileIds())
      const defaultTileId = layoutStore.focusedTileId()

      const addedTabKeys = new Set<string>()

      for (const a of agentStore.state.agents) {
        const key = `${TabType.AGENT}:${a.id}`
        let tileId = tabTileMap.get(key) ?? defaultTileId
        if (!validTileIds.has(tileId))
          tileId = defaultTileId
        tabStore.addTab({
          type: TabType.AGENT,
          id: a.id,
          title: a.title || undefined,
          tileId,
          workerId: a.workerId,
          workingDir: a.workingDir,
          gitBranch: a.gitStatus?.branch || undefined,
          gitOriginUrl: a.gitStatus?.originUrl || undefined,
        }, false)
        addedTabKeys.add(key)
      }

      for (const { workerId, terminals: terms } of terminalResults) {
        for (const term of terms) {
          const termId = term.terminalId
          const key = `${TabType.TERMINAL}:${termId}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          const termData = terminalStore.state.terminals.find(t => t.id === termId)
          tabStore.addTab({
            type: TabType.TERMINAL,
            id: termId,
            title: term.title || undefined,
            tileId,
            workerId,
            workingDir: termData?.workingDir,
            gitBranch: term.gitBranch || undefined,
            gitOriginUrl: term.gitOriginUrl || undefined,
          }, false)
          addedTabKeys.add(key)
        }
      }

      // Add hub tabs the worker didn't return (e.g. worker offline or
      // agent inactive after restart) so they remain visible in the UI.
      if (tabsResp?.tabs) {
        for (const t of tabsResp.tabs) {
          const key = `${t.tabType}:${t.tabId}`
          if (addedTabKeys.has(key))
            continue
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({
            type: t.tabType as TabType,
            id: t.tabId,
            tileId,
            workerId: t.workerId,
          }, false)
          addedTabKeys.add(key)
        }
      }

      // Merge any locally-moved tabs from the registry snapshot that the
      // server didn't return yet (cross-workspace moves may still be in
      // flight when we fetch from the server).
      if (cached && cached.tabs.tabs.length > 0) {
        const existingKeys = new Set(tabStore.state.tabs.map(t => tabKey(t)))
        for (const snapTab of cached.tabs.tabs) {
          const key = tabKey(snapTab)
          if (!existingKeys.has(key)) {
            const tileId = defaultTileId
            tabStore.addTab({
              type: snapTab.type,
              id: snapTab.id,
              title: snapTab.title,
              tileId,
              workerId: snapTab.workerId,
              workingDir: snapTab.workingDir,
              gitBranch: snapTab.gitBranch,
              gitOriginUrl: snapTab.gitOriginUrl,
            }, false)
          }
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
            fileViewMode?: string
            fileDiffBase?: string
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
              fileViewMode: lt.fileViewMode as Tab['fileViewMode'],
              fileDiffBase: lt.fileDiffBase as Tab['fileDiffBase'],
            }, false)
          }
        }
      }
      catch {
        // Ignore corrupt sessionStorage data
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

      // Cache the restored state in the registry.
      registry.set(activeId, {
        workspaceId: activeId,
        tabs: tabStore.snapshot(),
        layout: layoutStore.snapshot(),
        agents: [...agentStore.state.agents],
        terminals: [...terminalStore.state.terminals],
        restored: true,
        tabsLoaded: true,
      })

      setWorkspaceLoading(false)
    }).catch((err) => {
      log.warn('Workspace restore failed, unblocking UI:', err)
      setWorkspaceLoading(false)
    })
  }))
}
