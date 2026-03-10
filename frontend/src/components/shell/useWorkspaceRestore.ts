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

      // Derive distinct worker IDs from tabs.
      const workerIds = new Set<string>()
      if (tabsResp?.tabs) {
        for (const t of tabsResp.tabs) {
          if (t.workerId)
            workerIds.add(t.workerId)
        }
      }
      log.warn(`[restore] listTabs for ${activeId}: ${tabsResp?.tabs.map(t => `${t.tabType}:${t.tabId}`).join(', ') ?? 'null'}`)

      // Fetch agents and terminals from each worker in parallel.
      const agentResults = await Promise.all(
        [...workerIds].map(async (workerId) => {
          try {
            const resp = await workerRpc.listAgents(workerId, { workspaceId: activeId })
            return resp.agents
          }
          catch {
            return []
          }
        }),
      )

      const terminalResults = await Promise.all(
        [...workerIds].map(async (workerId) => {
          try {
            const resp = await workerRpc.listTerminals(workerId, { orgId: currentOrgId, workspaceId: activeId })
            return { workerId, terminals: resp.terminals }
          }
          catch {
            return { workerId, terminals: [] as Awaited<ReturnType<typeof workerRpc.listTerminals>>['terminals'] }
          }
        }),
      )

      if (gen !== loadGeneration)
        return

      // Build persistedKeys from hub's listTabs — used to filter stale
      // agents that the worker hasn't moved yet.
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

      // Also include tabs from the registry snapshot (e.g. tabs moved
      // to this workspace via cross-workspace drag that the hub may not
      // know about yet). This prevents the agent filter from discarding
      // locally-moved tabs.
      if (cached) {
        for (const snapTab of cached.tabs.tabs) {
          const key = `${snapTab.type}:${snapTab.id}`
          persistedKeys.add(key)
        }
      }

      // Populate agent store.
      const allAgents = agentResults.flat()
      log.warn(`[restore] listAgents for ${activeId}: ${allAgents.map(a => a.id).join(', ')}`)
      // Filter agents to only those confirmed by hub OR cached snapshot.
      const filteredAgents = allAgents.filter(a => persistedKeys.has(`${TabType.AGENT}:${a.id}`))
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
      log.warn(`[restore] final agents for ${activeId}: ${filteredAgents.map(a => a.id).join(', ')}`)
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

      log.warn(`[restore] creating tabs for ${activeId}, agents: ${agentStore.state.agents.map(a => a.id).join(', ')}`)
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
      }

      for (const { workerId, terminals: terms } of terminalResults) {
        for (const term of terms) {
          const termId = term.terminalId
          if (!terminalStore.isExited(termId) || persistedKeys.has(`${TabType.TERMINAL}:${termId}`)) {
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
          }
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
