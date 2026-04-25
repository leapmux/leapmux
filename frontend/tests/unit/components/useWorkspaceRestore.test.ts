import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceRestore } from '~/components/shell/useWorkspaceRestore'
import { EXPANDED_WORKSPACES_KEY } from '~/components/workspace/expandedWorkspaces'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'

// Both network calls return unresolved promises: the test only inspects
// the synchronous fan-out that happens inside the restore effect.
const mockListTabsForWorkspace = vi.fn(() => new Promise<never>(() => {}))
const mockGetLayout = vi.fn(() => new Promise<never>(() => {}))
const mockListTerminals = vi.fn<(workerId: string, req: { tabIds: string[] }) => Promise<{ terminals: unknown[] }>>(() => new Promise<never>(() => {}))

vi.mock('~/api/listTabsBatcher', () => ({
  listTabsForWorkspace: (...args: unknown[]) => mockListTabsForWorkspace(...args as []),
}))

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    getLayout: (...args: unknown[]) => mockGetLayout(...args as []),
  },
}))

vi.mock('~/api/workerRpc', () => ({
  listAgents: vi.fn(() => new Promise<never>(() => {})),
  listTerminals: (workerId: string, req: { tabIds: string[] }) => mockListTerminals(workerId, req),
}))

function makeBaseOpts() {
  const registry = createWorkspaceStoreRegistry()
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()
  const agentStore = createAgentStore()
  const chatStore = createChatStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const setWorkspaceLoading = vi.fn()
  return {
    registry,
    tabStore,
    layoutStore,
    agentStore,
    chatStore,
    controlStore,
    agentSessionStore,
    setWorkspaceLoading,
  }
}

beforeEach(() => {
  sessionStorage.clear()
  mockListTabsForWorkspace.mockClear()
  mockGetLayout.mockClear()
  mockListTerminals.mockClear()
  mockListTerminals.mockImplementation(() => new Promise<never>(() => {}))
})

afterEach(() => {
  sessionStorage.clear()
})

// Yields a microtask so Solid flushes queued createEffect invocations.
async function flushEffects(): Promise<void> {
  await Promise.resolve()
}

describe('useWorkspaceRestore sibling pre-fetch', () => {
  it('fires onExpandWorkspace for every expanded sibling in the same tick as the active ListTabs', async () => {
    sessionStorage.setItem(
      EXPANDED_WORKSPACES_KEY,
      JSON.stringify(['active-ws', 'sib-1', 'sib-2']),
    )
    const onExpandWorkspace = vi.fn()
    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      useWorkspaceRestore({
        ...makeBaseOpts(),
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
        onExpandWorkspace,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    // The active workspace's ListTabs must fire.
    expect(mockListTabsForWorkspace).toHaveBeenCalledTimes(1)
    expect(mockListTabsForWorkspace).toHaveBeenCalledWith('org-1', 'active-ws')

    // onExpandWorkspace fires for every sibling, not for the active workspace.
    const calls = onExpandWorkspace.mock.calls.map(c => c[0]).sort()
    expect(calls).toEqual(['sib-1', 'sib-2'])
    expect(onExpandWorkspace).not.toHaveBeenCalledWith('active-ws')

    dispose()
  })

  it('does not fire onExpandWorkspace when the active workspace is restored from cache', async () => {
    sessionStorage.setItem(EXPANDED_WORKSPACES_KEY, JSON.stringify(['active-ws', 'sib-1']))
    const onExpandWorkspace = vi.fn()
    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      const opts = makeBaseOpts()
      // Seed the registry so the active workspace takes the cached path.
      opts.registry.set('active-ws', {
        workspaceId: 'active-ws',
        tabs: [],
        activeTabKey: null,
        layout: { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
        agents: [],
        restored: true,
        tabsLoaded: true,
      })

      useWorkspaceRestore({
        ...opts,
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
        onExpandWorkspace,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    expect(mockListTabsForWorkspace).not.toHaveBeenCalled()
    expect(onExpandWorkspace).not.toHaveBeenCalled()

    dispose()
  })

  it('tolerates an empty expanded-workspaces list and still fires the active ListTabs', async () => {
    // No sessionStorage entry — readExpandedWorkspaceIds returns an empty set.
    const onExpandWorkspace = vi.fn()
    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      useWorkspaceRestore({
        ...makeBaseOpts(),
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
        onExpandWorkspace,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    expect(mockListTabsForWorkspace).toHaveBeenCalledTimes(1)
    expect(onExpandWorkspace).not.toHaveBeenCalled()

    dispose()
  })
})

describe('useWorkspaceRestore terminal hydration retry', () => {
  // Regression: after a full hub+worker restart, the WatchEvents catch-up
  // can deliver a TerminalStatusChange (READY) before ListTerminals
  // succeeds, leaving tab.status set but the worker-side payload (cols,
  // title, screen, etc.) still missing. The retry must keep firing until
  // worker-side fields land — skipping on status alone strands the tab
  // without dimensions or screen forever.
  it('re-hydrates a terminal tab whose status is set but worker payload is missing', async () => {
    const opts = makeBaseOpts()
    // Seed an existing workspace snapshot so the effect skips the
    // network restore path and goes straight into the retry pass.
    opts.registry.set('active-ws', {
      workspaceId: 'active-ws',
      tabs: [{
        type: TabType.TERMINAL,
        id: 'term-1',
        workerId: 'worker-1',
        // Worker-side StatusChange event has set status but cols is
        // still undefined because ListTerminals hasn't returned yet.
        status: TerminalStatus.READY,
      }],
      activeTabKey: null,
      layout: { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
      agents: [],
      restored: true,
      tabsLoaded: true,
    })

    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      useWorkspaceRestore({
        ...opts,
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    expect(mockListTerminals).toHaveBeenCalledTimes(1)
    expect(mockListTerminals).toHaveBeenCalledWith('worker-1', { tabIds: ['term-1'] })

    dispose()
  })

  // Counterpart: once the worker payload has populated cols, the tab is
  // fully hydrated and the retry must stay quiet — no thundering herd of
  // ListTerminals calls. `title` is intentionally left undefined to
  // confirm the discriminator is `cols`, not `title` (shells that don't
  // emit OSC titles would otherwise loop forever).
  it('does not re-hydrate a terminal tab that already has worker payload', async () => {
    const opts = makeBaseOpts()
    opts.registry.set('active-ws', {
      workspaceId: 'active-ws',
      tabs: [{
        type: TabType.TERMINAL,
        id: 'term-1',
        workerId: 'worker-1',
        status: TerminalStatus.READY,
        cols: 80,
        rows: 24,
      }],
      activeTabKey: null,
      layout: { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
      agents: [],
      restored: true,
      tabsLoaded: true,
    })

    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      useWorkspaceRestore({
        ...opts,
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    expect(mockListTerminals).not.toHaveBeenCalled()

    dispose()
  })

  // Mixed fixture: two tabs on the same worker, one fully hydrated and
  // one missing its payload. Proves the filter is per-tab (not
  // per-workspace) and that the RPC carries only the un-hydrated id.
  it('passes only un-hydrated tab ids to listTerminals', async () => {
    const opts = makeBaseOpts()
    opts.registry.set('active-ws', {
      workspaceId: 'active-ws',
      tabs: [
        {
          type: TabType.TERMINAL,
          id: 'term-hydrated',
          workerId: 'worker-1',
          status: TerminalStatus.READY,
          cols: 80,
          rows: 24,
        },
        {
          type: TabType.TERMINAL,
          id: 'term-missing',
          workerId: 'worker-1',
          status: TerminalStatus.READY,
        },
      ],
      activeTabKey: null,
      layout: { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
      agents: [],
      restored: true,
      tabsLoaded: true,
    })

    const [activeId, setActiveId] = createSignal<string | null>(null)
    const [orgId, setOrgId] = createSignal<string | undefined>(undefined)

    const dispose = createRoot((dispose) => {
      useWorkspaceRestore({
        ...opts,
        getActiveWorkspaceId: activeId,
        getOrgId: orgId,
      })
      setActiveId('active-ws')
      setOrgId('org-1')
      return dispose
    })

    await flushEffects()

    expect(mockListTerminals).toHaveBeenCalledTimes(1)
    expect(mockListTerminals).toHaveBeenCalledWith('worker-1', { tabIds: ['term-missing'] })

    dispose()
  })
})
