import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { batch, createEffect, createSignal, on, onCleanup } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { protoToTerminalTab, protoToTerminalTabFields, tabKey } from '~/stores/tab.store'

const log = createLogger('restore')

interface UseWorkspaceRestoreOpts {
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  agentStore: ReturnType<typeof createAgentStore>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: FloatingWindowStoreType
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
    tabStore,
    layoutStore,
    registry,
    setWorkspaceLoading,
  } = opts

  let loadGeneration = 0
  let previousWorkspaceId: string | null = null
  const [terminalHydrationTick, setTerminalHydrationTick] = createSignal(0)
  const terminalHydrationInflight = new Set<string>()
  const terminalHydrationRetryTimers = new Map<string, ReturnType<typeof setTimeout>>()
  const terminalHydrationRetryDelayMs = new Map<string, number>()

  const clearTerminalHydrationRetry = (workerId: string) => {
    const timer = terminalHydrationRetryTimers.get(workerId)
    if (timer) {
      clearTimeout(timer)
      terminalHydrationRetryTimers.delete(workerId)
    }
  }

  const scheduleTerminalHydrationRetry = (workerId: string) => {
    if (terminalHydrationRetryTimers.has(workerId))
      return
    const nextDelay = Math.min((terminalHydrationRetryDelayMs.get(workerId) ?? 500) * 2, 10_000)
    terminalHydrationRetryDelayMs.set(workerId, nextDelay)
    const timer = setTimeout(() => {
      terminalHydrationRetryTimers.delete(workerId)
      setTerminalHydrationTick(v => v + 1)
    }, nextDelay)
    terminalHydrationRetryTimers.set(workerId, timer)
  }

  const hydrateTerminalRecord = (workerId: string, term: Awaited<ReturnType<typeof workerRpc.listTerminals>>['terminals'][number]) => {
    // Hydration only refreshes worker-provided fields; layout fields
    // (tileId, position) on the existing tab are preserved.
    tabStore.updateTab(TabType.TERMINAL, term.terminalId, protoToTerminalTabFields(workerId, term))
  }

  onCleanup(() => {
    for (const workerId of terminalHydrationRetryTimers.keys()) {
      clearTerminalHydrationRetry(workerId)
    }
  })

  createEffect(on([getActiveWorkspaceId, getOrgId], ([activeId, currentOrgId]) => {
    if (!activeId || !currentOrgId)
      return

    const gen = ++loadGeneration

    // Save current workspace state to registry before switching.
    if (previousWorkspaceId && previousWorkspaceId !== activeId) {
      registry.set(previousWorkspaceId, {
        ...tabStore.snapshot(),
        workspaceId: previousWorkspaceId,
        layout: layoutStore.snapshot(),
        floatingWindows: opts.floatingWindowStore?.snapshot(),
        agents: [...agentStore.state.agents],
        restored: true,
        tabsLoaded: true,
      })
    }
    previousWorkspaceId = activeId

    // Check if we have a cached snapshot for this workspace.
    const cached = registry.get(activeId)
    if (cached?.restored) {
      tabStore.restore(cached)
      layoutStore.restore(cached.layout)
      if (cached.floatingWindows && opts.floatingWindowStore) {
        opts.floatingWindowStore.restore(cached.floatingWindows)
      }
      agentStore.setAgents(cached.agents)

      // Ensure every tile with tabs has an active tab (in case snapshot was
      // taken before per-tile active tabs were properly tracked).
      tabStore.initMissingTileActiveTabs()

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
        Array.from(agentIdsByWorker.entries(), async ([workerId, tabIds]) => {
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
        Array.from(terminalIdsByWorker.entries(), async ([workerId, tabIds]) => {
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

      tabStore.clear()

      if (layoutResp?.layout) {
        layoutStore.fromProto(layoutResp.layout)
      }
      else {
        layoutStore.initSingleTile()
      }

      // Restore floating windows from hub
      if (opts.floatingWindowStore && layoutResp?.floatingWindows && layoutResp.floatingWindows.length > 0) {
        opts.floatingWindowStore.fromProto(layoutResp.floatingWindows)
      }

      // Collect tile IDs from both main layout and floating windows
      const allFloatingTileIds = opts.floatingWindowStore?.getAllTileIds() ?? []
      const validTileIds = new Set([...layoutStore.getAllTileIds(), ...allFloatingTileIds])
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
          agentProvider: a.agentProvider,
          gitBranch: a.gitStatus?.branch || undefined,
          gitOriginUrl: a.gitStatus?.originUrl || undefined,
        }, { activate: false })
        addedTabKeys.add(key)
      }

      for (const { workerId, terminals: terms } of terminalResults) {
        for (const term of terms) {
          const key = `${TabType.TERMINAL}:${term.terminalId}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({ ...protoToTerminalTab(workerId, term), tileId }, { activate: false })
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
          const cachedTab = cached?.tabs.find(tab => tabKey(tab) === key)
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          // Preserve any cached tab fields (screen/cols/git info, etc.) and
          // overlay the hub's authoritative identity + worker.
          tabStore.addTab({
            ...cachedTab,
            type: t.tabType as TabType,
            id: t.tabId,
            tileId,
            workerId: t.workerId,
          }, { activate: false })
          addedTabKeys.add(key)
        }
      }

      // Merge any locally-moved tabs from the registry snapshot that the
      // server didn't return yet (cross-workspace moves may still be in
      // flight when we fetch from the server).
      if (cached && cached.tabs.length > 0) {
        const existingKeys = new Set(tabStore.state.tabs.map(t => tabKey(t)))
        for (const snapTab of cached.tabs) {
          if (!existingKeys.has(tabKey(snapTab))) {
            tabStore.addTab({ ...snapTab, tileId: defaultTileId }, { activate: false })
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
            }, { activate: false })
          }
        }
      }
      catch {
        // Ignore corrupt sessionStorage data
      }

      // Restore per-tile active tabs from sessionStorage
      try {
        const tileActiveJson = sessionStorage.getItem(`leapmux:tileActiveTabs:${activeId}`)
        if (tileActiveJson) {
          const tileActiveTabs = JSON.parse(tileActiveJson) as Record<string, string>
          for (const [tileId, key] of Object.entries(tileActiveTabs)) {
            if (tabStore.state.tabs.some(t => tabKey(t) === key && t.tileId === tileId)) {
              const parts = key.split(':')
              tabStore.setActiveTabForTile(tileId, Number(parts[0]) as TabType, parts[1])
            }
          }
        }
      }
      catch {
        // Ignore corrupt sessionStorage data
      }

      // Ensure every tile with tabs has an active tab
      tabStore.initMissingTileActiveTabs()

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

      // Restore focused tile from sessionStorage
      const savedFocusedTile = sessionStorage.getItem(`leapmux:focusedTile:${activeId}`)
      if (savedFocusedTile && validTileIds.has(savedFocusedTile)) {
        layoutStore.setFocusedTile(savedFocusedTile)
      }

      // Cache the restored state in the registry.
      registry.set(activeId, {
        ...tabStore.snapshot(),
        workspaceId: activeId,
        layout: layoutStore.snapshot(),
        floatingWindows: opts.floatingWindowStore?.snapshot(),
        agents: [...agentStore.state.agents],
        restored: true,
        tabsLoaded: true,
      })

      setWorkspaceLoading(false)
    }).catch((err) => {
      log.warn('Workspace restore failed, unblocking UI:', err)
      setWorkspaceLoading(false)
    })
  }))

  createEffect(() => {
    terminalHydrationTick()
    const activeId = getActiveWorkspaceId()
    if (!activeId)
      return

    // A tab needs hydration when its worker-side data hasn't been fetched
    // yet (status undefined) or when it was marked disconnected after a
    // temporary worker outage.
    const missingByWorker = new Map<string, string[]>()
    for (const tab of tabStore.state.tabs) {
      if (tab.type !== TabType.TERMINAL || !tab.workerId)
        continue
      if (tab.status !== undefined && tab.status !== 'disconnected')
        continue
      const ids = missingByWorker.get(tab.workerId) ?? []
      ids.push(tab.id)
      missingByWorker.set(tab.workerId, ids)
    }

    for (const [workerId, tabIds] of missingByWorker.entries()) {
      if (terminalHydrationInflight.has(workerId))
        continue

      terminalHydrationInflight.add(workerId)
      const targetWorkspaceId = activeId
      void workerRpc.listTerminals(workerId, { tabIds }).then((resp) => {
        if (getActiveWorkspaceId() !== targetWorkspaceId)
          return
        const resolvedIDs = new Set(resp.terminals.map(term => term.terminalId))
        batch(() => {
          for (const term of resp.terminals) {
            hydrateTerminalRecord(workerId, term)
          }
        })
        if (tabIds.some(id => !resolvedIDs.has(id))) {
          scheduleTerminalHydrationRetry(workerId)
        }
        else {
          clearTerminalHydrationRetry(workerId)
          terminalHydrationRetryDelayMs.delete(workerId)
        }
      }).catch((err) => {
        log.warn('failed to hydrate terminal metadata after restore', { workerId, tabIds, err })
        scheduleTerminalHydrationRetry(workerId)
      }).finally(() => {
        terminalHydrationInflight.delete(workerId)
      })
    }
  })
}
