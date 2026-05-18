import { create } from '@bufbuild/protobuf'
import { createEffect, createRoot, createSignal, untrack } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceRestore } from '~/components/shell/useWorkspaceRestore'
import { EXPANDED_WORKSPACES_KEY } from '~/components/workspace/expandedWorkspaces'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { HLCSchema, LWWStringSchema, TabRecordSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { getCRDTBridge, project, setCRDTBridge } from '~/lib/crdt'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import { installTestBridge } from '../helpers/crdtBridge'

// Both network calls return unresolved promises: the test only inspects
// the synchronous fan-out that happens inside the restore effect.
const mockListTabsForWorkspace = vi.fn<(orgId: string, wsId: string) => Promise<unknown>>(() => new Promise<never>(() => {}))
const mockListTerminals = vi.fn<(workerId: string, req: { tabIds: string[] }) => Promise<{ terminals: unknown[] }>>(() => new Promise<never>(() => {}))
const mockListAgents = vi.fn<(workerId: string, req: { tabIds: string[] }) => Promise<{ agents: unknown[] }>>(() => new Promise<never>(() => {}))

vi.mock('~/api/listTabsBatcher', () => ({
  listTabsForWorkspace: (...args: unknown[]) => mockListTabsForWorkspace(...args as [string, string]),
}))

vi.mock('~/api/workerRpc', () => ({
  listAgents: (workerId: string, req: { tabIds: string[] }) => mockListAgents(workerId, req),
  listTerminals: (workerId: string, req: { tabIds: string[] }) => mockListTerminals(workerId, req),
}))

function makeBaseOpts() {
  const registry = createWorkspaceStoreRegistry()
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()
  const chatStore = createChatStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const setWorkspaceLoading = vi.fn()
  return {
    registry,
    tabStore,
    layoutStore,
    chatStore,
    controlStore,
    agentSessionStore,
    setWorkspaceLoading,
  }
}

beforeEach(() => {
  sessionStorage.clear()
  mockListTabsForWorkspace.mockClear()
  mockListTabsForWorkspace.mockImplementation(() => new Promise<never>(() => {}))
  mockListTerminals.mockClear()
  mockListTerminals.mockImplementation(() => new Promise<never>(() => {}))
  mockListAgents.mockClear()
  mockListAgents.mockImplementation(() => new Promise<never>(() => {}))
})

afterEach(() => {
  sessionStorage.clear()
  setCRDTBridge(null)
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

  // Per-worker retry slot cleanup. When a worker disappears from the
  // missing-tabs map (its tabs were closed, it disconnected, or every
  // pending tab hydrated), the next failure on the same worker id must
  // restart the backoff at `initialMs` — not resume mid-sequence from
  // a stale `lastBaseDelays` entry the helper would otherwise keep
  // until unmount. Verified by pinning Math.random so the un-jittered
  // base equals the scheduled delay, then comparing arm-times across
  // a dropout cycle.
  it('resets per-worker retry state when a worker drops out of the candidate map', async () => {
    vi.useFakeTimers()
    // Pin jitter to its symmetric midpoint so scheduled delays match
    // the un-jittered base (500ms → 1000ms → …) exactly.
    vi.spyOn(Math, 'random').mockReturnValue(0.5)

    try {
      mockListTerminals.mockReset()
      // Every call rejects → every effect run schedules a retry.
      mockListTerminals.mockRejectedValue(new Error('worker unavailable'))

      const opts = makeBaseOpts()
      const workerId = 'flaky-worker'
      opts.registry.set('active-ws', {
        workspaceId: 'active-ws',
        tabs: [{
          type: TabType.TERMINAL,
          id: 'term-1',
          workerId,
          status: TerminalStatus.READY,
        }],
        activeTabKey: null,
        layout: { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
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

      // First effect tick: one listTerminals fire, fails, retry armed.
      await flushEffects()
      await vi.advanceTimersByTimeAsync(0)
      expect(mockListTerminals).toHaveBeenCalledTimes(1)

      // Advance just past the first retry delay (500ms). The retry
      // bumps the tick → effect runs again → second listTerminals →
      // rejects → next retry armed at 1000ms.
      await vi.advanceTimersByTimeAsync(500)
      expect(mockListTerminals).toHaveBeenCalledTimes(2)

      // Worker drops out: close the only tab that needs hydration.
      // `tabsNeedingHydration` now returns an empty map for this
      // worker, and the effect's keyset-diff must call `reset` for it.
      opts.tabStore.removeTab(TabType.TERMINAL, 'term-1')
      await flushEffects()

      // Re-introduce a tab on the same worker, still un-hydrated. The
      // effect fires immediately (candidate map changed) and the third
      // listTerminals call rejects.
      mockListTerminals.mockClear()
      opts.tabStore.addTab({
        type: TabType.TERMINAL,
        id: 'term-2',
        workerId,
        status: TerminalStatus.READY,
      })
      await flushEffects()
      await vi.advanceTimersByTimeAsync(0)
      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      mockListTerminals.mockClear()

      // The retry slot was reset, so the next retry must fire at
      // 500ms — NOT 2000ms (which would be the un-reset doubled value
      // after 500ms → 1000ms → 2000ms). Tick at 499ms: no fire yet.
      // Tick at 501ms: the retry must have fired, re-running the
      // effect → fourth listTerminals call.
      await vi.advanceTimersByTimeAsync(499)
      expect(mockListTerminals).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(2)
      expect(mockListTerminals).toHaveBeenCalledTimes(1)

      dispose()
    }
    finally {
      vi.useRealTimers()
      vi.restoreAllMocks()
    }
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

// Regression: when multiple AGENT tabs are restored in a single pass, the
// loop calls `tabStore.addTab` for each agent. `addTab` emits a CRDT op
// (SetTabRegister) through the bridge, which bumps the pendingVersion
// signal the AppShell-style projection reconciler subscribes to. Solid's
// `createEffect` flushes synchronously after the signal write, so the
// reconciler runs *between* the loop's iterations — after the first
// agent is in the local store but before the second. It then adds a
// bare tab (no title, no agentProvider) for the second agent because
// the projection still says that agent's tab exists. When the loop's
// next iteration calls `addTab` for that second agent with full
// metadata, the dedupe guard in `addTab` silently drops the metadata-
// carrying record in favour of the bare one. Net result: the first tab
// looks correct (Agent Etta) and every later tab in the same workspace
// renders as `<raw id>` + the generic bot icon — exactly the symptom
// reported after a fresh desktop-solo restart.
describe('useWorkspaceRestore agent hydration race against reconciler', () => {
  it('keeps full metadata on every agent tab even when the reconciler fires mid-loop', async () => {
    const orgId = 'org-race'
    const wsId = 'ws-race'
    const tileId = 'tile-race'
    const workerId = 'worker-race'

    await new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // Real bridge + PendingOpsManager so addTab's emit path drives
          // the same signal AppShell wires the reconciler to.
          const harness = installTestBridge({ orgId, workspaceId: wsId, rootTileId: tileId })
          const hlc = create(HLCSchema, { physical: 10n, logical: 0n, clientId: 'seed' })
          for (const id of ['etta', 'walker']) {
            harness.pending.state.confirmedState.tabs[id] = create(TabRecordSchema, {
              tabType: TabType.AGENT,
              tabId: id,
              tileId: create(LWWStringSchema, { value: tileId, hlc }),
              position: create(LWWStringSchema, { value: id === 'etta' ? 'n' : 'ne', hlc }),
              workerId: create(LWWStringSchema, { value: workerId, hlc }),
            })
          }
          harness.pending.recomputeSpeculative()

          const opts = makeBaseOpts()

          // Mirror AppShell.tsx's CRDT reconciler effect. The effect
          // subscribes to the bridge's speculativeState accessor — which
          // is the same signal `bumpVersion` (called from
          // PendingOpsManager.submit -> notify) bumps. On every fire,
          // pushes any projection-known tabs missing from tabStore in
          // as bare records.
          createEffect(() => {
            const bridge = getCRDTBridge()
            if (!bridge)
              return
            const state = bridge.speculativeState()
            if (!state)
              return
            untrack(() => {
              const proj = project(state)
              const rendered = proj.renderedTabs.filter(t => t.workspaceId === wsId)
              opts.tabStore.reconcileFromProjection({
                workspaceId: wsId,
                renderedTabs: rendered,
                crdtKnownTabIds: new Set(Object.keys(state.tabs)),
              })
            })
          })

          // Hub-side ListTabs returns both tabs, the worker-side
          // ListAgents returns both agents with full metadata.
          mockListTabsForWorkspace.mockImplementation(async () => ({
            tabs: [
              { tabType: TabType.AGENT, tabId: 'etta', tileId, position: 'n', workerId, workspaceId: wsId },
              { tabType: TabType.AGENT, tabId: 'walker', tileId, position: 'ne', workerId, workspaceId: wsId },
            ],
          }))
          mockListAgents.mockImplementation(async () => ({
            agents: [
              {
                id: 'etta',
                workspaceId: wsId,
                workerId,
                title: 'Agent Etta',
                agentProvider: AgentProvider.CLAUDE_CODE,
                workingDir: '/repo',
              },
              {
                id: 'walker',
                workspaceId: wsId,
                workerId,
                title: 'Agent Walker',
                agentProvider: AgentProvider.CLAUDE_CODE,
                workingDir: '/repo',
              },
            ],
          }))

          const [getActiveId, setActiveId] = createSignal<string | null>(null)
          const [getOrgId, setOrgId] = createSignal<string | undefined>(undefined)

          useWorkspaceRestore({
            ...opts,
            getActiveWorkspaceId: getActiveId,
            getOrgId,
          })
          setActiveId(wsId)
          setOrgId(orgId)

          // Drain microtasks until the restore .then() body has run.
          // The path awaits two promises (tabsLoaded + bootstrap) and
          // then fanOutTabsToWorkers, so several microtask turns are
          // needed.
          for (let i = 0; i < 20; i++) {
            await Promise.resolve()
          }

          const ettaTab = opts.tabStore.state.tabs.find(t => t.id === 'etta')
          const walkerTab = opts.tabStore.state.tabs.find(t => t.id === 'walker')
          expect(ettaTab, 'etta tab present').toBeDefined()
          expect(walkerTab, 'walker tab present').toBeDefined()
          expect(ettaTab?.title).toBe('Agent Etta')
          expect(ettaTab?.agentProvider).toBe(AgentProvider.CLAUDE_CODE)
          // The bug: walker comes back as a bare reconciler-added tab.
          expect(walkerTab?.title).toBe('Agent Walker')
          expect(walkerTab?.agentProvider).toBe(AgentProvider.CLAUDE_CODE)

          harness.dispose()
          dispose()
          resolve()
        }
        catch (err) {
          dispose()
          reject(err)
        }
      })
    })
  })
})
