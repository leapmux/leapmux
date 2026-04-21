import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceRestore } from '~/components/shell/useWorkspaceRestore'
import { EXPANDED_WORKSPACES_KEY } from '~/components/workspace/expandedWorkspaces'
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
  listTerminals: vi.fn(() => new Promise<never>(() => {})),
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
