/// <reference types="vitest/globals" />
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabHydrationStatus } from '~/generated/proto/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { isFileTab, isImageTab } from '~/stores/tab.types'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { useTabHydrators } from './useTabHydrators'

const mockListAgents = vi.fn()
const mockListTerminals = vi.fn()
const mockGetTabPayload = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  listAgents: (...a: unknown[]) => mockListAgents(...a),
  listTerminals: (...a: unknown[]) => mockListTerminals(...a),
  getTabPayload: (...a: unknown[]) => mockGetTabPayload(...a),
}))

beforeEach(() => {
  mockListAgents.mockReset()
  mockListAgents.mockResolvedValue({ agents: [], verdicts: [] })
  mockListTerminals.mockReset()
  mockListTerminals.mockResolvedValue({ terminals: [], verdicts: [] })
  mockGetTabPayload.mockReset()
  mockGetTabPayload.mockResolvedValue({
    payload: { workingDir: '/repo', kind: { case: 'file', value: { filePath: '/repo/nested/x.ts' } } },
  })
})

afterEach(() => setCRDTBridge(null))

const flush = () => new Promise<void>(queueMicrotask)

function agentInfo(id: string, over: Record<string, unknown> = {}) {
  return {
    id,
    workerId: 'w1',
    title: 'Agent Olivia',
    workingDir: '/repo',
    agentProvider: 1,
    status: AgentStatus.ACTIVE,
    agentSessionId: '',
    optionGroups: [],
    createdAt: '',
    startupError: '',
    startupMessage: '',
    gitStatus: undefined,
    ...over,
  }
}

function terminalInfo(id: string, over: Record<string, unknown> = {}) {
  return {
    terminalId: id,
    title: '',
    workingDir: '/repo',
    shellStartDir: '/repo',
    screen: new Uint8Array(),
    screenEndOffset: 0n,
    cols: 80,
    rows: 24,
    status: TerminalStatus.READY,
    exited: false,
    gitBranch: '',
    gitOriginUrl: '',
    gitToplevel: '',
    gitIsWorktree: false,
    startupError: '',
    startupMessage: '',
    ...over,
  }
}

/**
 * The single hydration path.
 *
 * Everything a tab needs beyond `tile_id` / `position` / `worker_id` comes from
 * the worker — titles, agent status, terminal dimensions, file paths — and the
 * hub strips all of it from the userevents stream because it lives behind E2EE.
 * A tab that arrived purely via the CRDT projection (opened by another browser,
 * another device, or the CLI) is a bare row until this fetches the rest.
 *
 * There used to be a second, per-workspace path alongside it. That one had no
 * in-flight dedupe and no backoff, so a worker still handshaking got re-asked on
 * every reactive tick until it closed the channel. This module already had both
 * guards, so it is now the only path — and it had no tests, which is how a
 * predicate that could never go false shipped in it.
 */
function setup(workspaceId = 'ws-test') {
  const harness = installTestBridge({ workspaceId })
  const stores = createTestTabStores(workspaceId)
  const repoGitStore = createRepoGitStore()
  let seq = 0
  return {
    ...stores,
    repoGitStore,
    harness,
    mount: (
      onlineWorkerIds?: () => ReadonlySet<string>,
      settingsPendingAxes?: (agentId: string) => ReadonlySet<string>,
    ) =>
      useTabHydrators({
        view: stores.view,
        metadata: stores.metadata,
        repoGitStore,
        onlineWorkerIds,
        settingsPendingAxes,
      }),
    add(type: TabType, id: string, workerId = 'w1', tileId = harness.rootTileId) {
      seq += 1
      emitAddTab({ type, id, tileId, position: `p${seq}`, workerId })
    },
  }
}

describe('useTabHydrators', () => {
  describe('agent', () => {
    it('fetches the agent record for a tab that arrived with no metadata', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents).toHaveBeenCalledTimes(1)
      expect(mockListAgents.mock.calls[0][0]).toBe('w1')
      expect(mockListAgents.mock.calls[0][1]).toEqual({ tabIds: ['a1'] })
      d()
    })

    it('writes the fetched fields onto the tab', async () => {
      mockListAgents.mockResolvedValue({ agents: [agentInfo('a1')], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(s.view.getAgentTab('a1')?.title).toBe('Agent Olivia')
      expect(s.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
      d()
    })

    // The reason this module exists at all: a tab in a workspace the user has
    // never opened is just as bare as one in the active workspace, and the
    // sidebar renders it either way.
    it('hydrates tabs in a workspace that is not the active one', async () => {
      mockListAgents.mockResolvedValue({ agents: [agentInfo('elsewhere')], verdicts: [] })
      const s = setup('ws-active')
      const d = createRoot((dispose) => {
        seedWorkspace(s.harness, 'ws-other', 'tile-other')
        emitAddTab({ type: TabType.AGENT, id: 'elsewhere', tileId: 'tile-other', position: 'a', workerId: 'w1' })
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents).toHaveBeenCalledTimes(1)
      expect(s.view.getAgentTab('elsewhere')?.title).toBe('Agent Olivia')
      d()
    })

    it('batches same-worker agents into one call', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        s.add(TabType.AGENT, 'a2')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents).toHaveBeenCalledTimes(1)
      expect(mockListAgents.mock.calls[0][1].tabIds.sort()).toEqual(['a1', 'a2'])
      d()
    })

    it('splits calls per worker', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1', 'w1')
        s.add(TabType.AGENT, 'a2', 'w2')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents.mock.calls.map(c => c[0]).sort()).toEqual(['w1', 'w2'])
      d()
    })

    // Once hydrated the predicate goes false, so a later unrelated tab change
    // must not re-ask. Without that the hydrator re-fetches forever.
    it('does not re-fetch a tab that already has its record', async () => {
      mockListAgents.mockResolvedValue({ agents: [agentInfo('a1')], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()
      expect(mockListAgents).toHaveBeenCalledTimes(1)

      s.metadata.patch('a1', { hasNotification: true })
      await flush()
      await flush()

      expect(mockListAgents).toHaveBeenCalledTimes(1)
      d()
    })

    it('skips a tab with no worker to ask', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1', '')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents).not.toHaveBeenCalled()
      d()
    })
  })

  describe('terminal', () => {
    // The predicate is the `hydrated` flag, not `!title`. A shell that emits no
    // OSC title leaves `title` empty for its whole life, so a title-keyed check
    // never goes false and the hydrator re-asks the worker on every tick until
    // the channel closes.
    it('stops asking once the worker has answered, even with no title', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: '' })], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()
      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      expect(s.view.getTerminalTab('t1')?.title, 'the shell reported no title').toBeUndefined()

      // Any later change re-evaluates the candidate set. It must come up empty.
      s.metadata.patch('t1', { hasNotification: true })
      await flush()
      await flush()

      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      d()
    })

    it('writes the fetched terminal fields onto the tab', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: 'zsh' })], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(s.view.getTerminalTab('t1')?.title).toBe('zsh')
      expect(s.view.getTerminalTab('t1')?.cols).toBe(80)
      d()
    })
  })

  describe('file', () => {
    it('fetches the path and working dir for a file tab that has none', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.FILE, 'f1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockGetTabPayload).toHaveBeenCalledTimes(1)
      const f1 = s.view.getById(TabType.FILE, 'f1')
      expect(f1 && isFileTab(f1) && f1.filePath).toBe('/repo/nested/x.ts')
      // The working dir comes back with the path because it is what puts the
      // tab in a branch group: a hydrated tab without one matches git status
      // by the file's own path instead, which is a different repo whenever the
      // file was opened from another checkout.
      expect(f1?.workingDir).toBe('/repo')
      d()
    })

    it('fetches the message reference for an image tab, so it can resolve at all', async () => {
      // Without the payload an IMAGE tab knows no agent, no seq and no index,
      // so it can show nothing whatsoever -- it needs this at least as much as
      // a FILE tab does.
      mockGetTabPayload.mockResolvedValueOnce({
        payload: {
          workingDir: '/repo',
          kind: { case: 'image', value: { agentId: 'a1', seq: 42n, imageIndex: 1, title: 'Read' } },
        },
      })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.IMAGE, 'i1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockGetTabPayload).toHaveBeenCalledTimes(1)
      const i1 = s.view.getById(TabType.IMAGE, 'i1')
      expect(i1 && isImageTab(i1) && i1.imageAgentId).toBe('a1')
      expect(i1 && isImageTab(i1) && i1.imageSeq).toBe(42n)
      expect(i1 && isImageTab(i1) && i1.imageIndex).toBe(1)
      expect(i1?.title).toBe('Read')
      expect(i1?.workingDir).toBe('/repo')
      d()
    })

    /**
     * A tab the private-event stream already answered for is not fetched at
     * all. `TabPayloadRegistered` carries the same `(file_path, working_dir)`
     * this RPC returns, so the stream marks the tab `hydrated` and the
     * predicate goes false.
     *
     * That flag is deliberately the ONLY record of "the worker has answered".
     * A companion path cache used to hold it, and being keyed by tab id but
     * swept on a different schedule than the metadata row, it could outlive the
     * row -- leaving the hydrator permanently convinced it had already asked
     * about a tab whose path had been discarded.
     */
    it('skips a tab the private-event stream already answered for', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.FILE, 'f1')
        // What `onTabPayloadRegistered` writes.
        s.metadata.patch('f1', { filePath: '/repo/nested/x.ts', workingDir: '/repo', hydrated: true })
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockGetTabPayload).not.toHaveBeenCalled()
      d()
    })

    it('does not re-fetch once the path is known', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.FILE, 'f1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()
      expect(mockGetTabPayload).toHaveBeenCalledTimes(1)

      s.metadata.patch('f1', { hasNotification: true })
      await flush()
      await flush()

      expect(mockGetTabPayload).toHaveBeenCalledTimes(1)
      d()
    })
  })

  // A failure must not spin. The per-worker backoff owns the retry; the
  // in-flight set stops a second request going out while one is outstanding.
  it('does not pile on requests while one is in flight', async () => {
    let resolveIt: (v: unknown) => void = () => {}
    mockListAgents.mockReturnValue(new Promise((r) => {
      resolveIt = r
    }))
    const s = setup()
    const d = createRoot((dispose) => {
      s.add(TabType.AGENT, 'a1')
      s.mount()
      return dispose
    })
    await flush()

    // A second tab on the same worker arrives mid-flight.
    s.add(TabType.AGENT, 'a2')
    await flush()
    await flush()

    // a1 is in flight, so only a2 may go out — never a1 again. Assert the
    // second batch POSITIVELY: `slice(1)` is empty when no second call was
    // made, so a regression that drops the mid-flight tab entirely would
    // satisfy a "not.toContain" loop by never running its body.
    expect(mockListAgents.mock.calls.length, 'a2 must have gone out').toBe(2)
    expect(mockListAgents.mock.calls[1][1].tabIds).toEqual(['a2'])

    resolveIt({ agents: [], verdicts: [] })
    d()
  })

  /**
   * "Hydrated" is a flag with exactly one writer, not a sniffed payload field.
   *
   * Both earlier predicates were forgeable. `!title` never went false for a
   * shell that emits no OSC title; `cols === undefined` goes false the moment
   * `handleTerminalResize` patches dimensions locally — within a frame of
   * mount — which retires the tab from the candidate set before the worker has
   * answered, permanently, because the candidate-set hash then stops changing.
   */
  describe('hydration is tracked explicitly', () => {
    it('does not re-fetch a tab the worker has already answered for', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1')], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()
      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      expect(s.metadata.get('t1')?.hydrated).toBe(true)

      // Another tab appears, changing the candidate set and re-running the
      // dispatch effect. The hydrated tab must not be re-requested.
      s.add(TabType.TERMINAL, 't2')
      await flush()
      await flush()

      for (const call of mockListTerminals.mock.calls.slice(1))
        expect(call[1].tabIds).not.toContain('t1')
      d()
    })

    /**
     * `hydrated` is write-once, so on its own it strands a terminal the
     * worker-offline sweep marked DISCONNECTED: the reconnect's
     * `statusChange{READY}` is deliberately ignored for a DISCONNECTED tab
     * ("Preserve DISCONNECTED / EXITED"), and this hook is the only production
     * caller of `listTerminals`. Without the second predicate clause the tab
     * stays read-only — input dropped, restart gated on EXITED — until a full
     * page reload.
     */
    it('re-asks for a terminal the worker marked DISCONNECTED', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: 'zsh' })], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()
      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      expect(s.metadata.get('t1')?.hydrated).toBe(true)

      // The worker goes offline: the sweep in `useWorkspaceConnection` marks
      // every READY terminal on it DISCONNECTED.
      s.metadata.patch('t1', { terminalStatus: TerminalStatus.DISCONNECTED })
      await flush()
      await flush()

      expect(mockListTerminals, 'the disconnected tab is asked about again').toHaveBeenCalledTimes(2)
      expect(mockListTerminals.mock.calls[1][1].tabIds).toEqual(['t1'])
      d()
    })

    it('goes quiet again once the reply resolves the disconnected terminal', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: 'zsh' })], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      s.metadata.patch('t1', { terminalStatus: TerminalStatus.DISCONNECTED })
      await flush()
      await flush()
      expect(mockListTerminals).toHaveBeenCalledTimes(2)
      // The reply carries the worker's real status, which clears the trigger.
      expect(s.view.getTerminalTab('t1')?.status).not.toBe(TerminalStatus.DISCONNECTED)

      // An unrelated change re-runs the dispatch effect; nothing more goes out.
      s.metadata.patch('t1', { hasNotification: true })
      await flush()
      await flush()
      expect(mockListTerminals).toHaveBeenCalledTimes(2)
      d()
    })

    // The local open paths hold the worker's own OpenAgent/OpenTerminal
    // response, so they record the hydration themselves. Without that a tab
    // this client just opened is re-asked immediately, and the reply is applied
    // with none of the live handlers' guards.
    it('does not re-ask for a tab the local open path already recorded', async () => {
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        // What `useAgentOperations` writes on an OpenAgent response.
        s.metadata.patch('a1', { agentStatus: AgentStatus.INACTIVE, hydrated: true })
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListAgents, 'the open response already answered').not.toHaveBeenCalled()
      d()
    })

    // The regression: a local resize writes `cols`, so a `cols`-keyed predicate
    // would drop this tab and it would never receive its title or status.
    it('still hydrates a terminal whose dimensions were set locally first', async () => {
      mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: 'zsh' })], verdicts: [] })
      const s = setup()
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        // xterm's fit observer fires on mount, before any worker reply.
        s.metadata.patch('t1', { cols: 120, rows: 40 })
        s.mount()
        return dispose
      })
      await flush()
      await flush()

      expect(mockListTerminals).toHaveBeenCalledTimes(1)
      expect(s.metadata.get('t1')?.title, 'the worker reply still lands').toBe('zsh')
      d()
    })

    // A batch RPC can succeed while omitting tabs: the reply reflects the
    // channel's accessible set at REQUEST time, which the hub grows
    // asynchronously. Treating a fulfilled promise as "all hydrated" cleared
    // the backoff and stranded those tabs.
    it('retries a tab the worker omitted from an otherwise successful reply', async () => {
      vi.useFakeTimers()
      try {
        // t1 answered for, t2 silently missing.
        mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1')], verdicts: [] })
        const s = setup()
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.add(TabType.TERMINAL, 't2')
          s.mount()
          return dispose
        })
        await vi.advanceTimersByTimeAsync(0)
        expect(mockListTerminals).toHaveBeenCalledTimes(1)
        expect(s.metadata.get('t1')?.hydrated).toBe(true)
        expect(s.metadata.get('t2')?.hydrated, 'omitted tab is not marked').toBeUndefined()

        // The backoff must still be armed for the omitted tab.
        await vi.advanceTimersByTimeAsync(1000)
        const retried = mockListTerminals.mock.calls.slice(1)
        expect(retried.length, 'a retry went out').toBeGreaterThan(0)
        expect(retried[0][1].tabIds).toEqual(['t2'])
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })
  })
  /**
   * A reply that omits a tab used to be ambiguous, so the client had to assume
   * transient and retry forever -- one 10s timer per unanswerable tab, for the
   * life of the page. The worker now says WHY, and only "not accessible yet"
   * (the hub grows a channel's accessible set asynchronously) is worth asking
   * about again.
   */
  describe('per-tab hydration verdicts', () => {
    it('stops retrying a tab the worker has no record for', async () => {
      vi.useFakeTimers()
      try {
        mockListTerminals.mockResolvedValue({
          terminals: [terminalInfo('t1')],
          verdicts: [
            { tabId: 't1', status: TabHydrationStatus.FOUND },
            { tabId: 't2', status: TabHydrationStatus.ABSENT },
          ],
        })
        const s = setup()
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.add(TabType.TERMINAL, 't2')
          s.mount()
          return dispose
        })
        await vi.advanceTimersByTimeAsync(0)
        expect(mockListTerminals).toHaveBeenCalledTimes(1)

        // Well past the 10s ceiling: nothing more goes out.
        await vi.advanceTimersByTimeAsync(60_000)
        expect(mockListTerminals, 'ABSENT is permanent -- no retry').toHaveBeenCalledTimes(1)
        expect(s.metadata.get('t2')?.hydrated, 'and it is not marked hydrated').toBeUndefined()
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })

    /**
     * The attempt cap is per EPISODE, not per page.
     *
     * `maxAttempts` stops a permanently-unreachable worker holding a timer
     * forever, and the reset that ends an episode used to live only inside the
     * retry callback — which `schedule` stops arming the moment the budget is
     * spent, so it could never run. A worker that was down long enough to
     * exhaust its budget was then stranded: opening another tab on it fired one
     * more request, and when that failed nothing re-armed. Every tab on that
     * worker showed "Agent not found." for the life of the page, with no
     * spinner and no toast (`isExhausted` has no production consumer).
     */
    /**
     * A worker coming back is the other way an episode ends.
     *
     * Nothing about a reconnect touches the candidate set -- the same tabs are
     * still waiting on the same worker -- so the membership-gated effect cannot
     * see it. Without a liveness signal the tabs stay bare until the user
     * happens to open another one on that worker, which is not a thing a user
     * would think to do.
     */
    it('re-arms an exhausted worker when it comes back online', async () => {
      vi.useFakeTimers()
      try {
        mockListTerminals.mockRejectedValue(new Error('worker unreachable'))
        const s = setup()
        const [online, setOnline] = createSignal<ReadonlySet<string>>(new Set())
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.mount(() => online())
          return dispose
        })
        await vi.advanceTimersByTimeAsync(120_000)
        const exhausted = mockListTerminals.mock.calls.length
        await vi.advanceTimersByTimeAsync(120_000)
        expect(mockListTerminals, 'the budget really is spent').toHaveBeenCalledTimes(exhausted)

        // The hub reports it back. The tab set has not changed.
        mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1', { title: 'zsh' })], verdicts: [] })
        setOnline(new Set(['w1']))
        await vi.advanceTimersByTimeAsync(0)

        expect(mockListTerminals.mock.calls.length, 'coming back is worth one more ask')
          .toBeGreaterThan(exhausted)
        expect(s.view.getTerminalTab('t1')?.title, 'and the tab finally hydrates').toBe('zsh')
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })

    // A worker that was online through every WORKERS_CHANGED push has nothing
    // to retry; only the OFF -> ON edge does.
    it('does not re-dispatch for a worker that was already online', async () => {
      const s = setup()
      const [online, setOnline] = createSignal<ReadonlySet<string>>(new Set(['w1']))
      const d = createRoot((dispose) => {
        s.add(TabType.TERMINAL, 't1')
        s.mount(() => online())
        return dispose
      })
      await flush()
      await flush()
      const initial = mockListTerminals.mock.calls.length

      // A fresh Set with the same membership, as `listWorkers` produces.
      setOnline(new Set(['w1']))
      await flush()
      await flush()

      expect(mockListTerminals).toHaveBeenCalledTimes(initial)
      d()
    })

    /**
     * An agent tab has no re-arm of its own. A terminal re-arms on DISCONNECTED,
     * which is why a terminal repaired itself after an outage and an agent did
     * not -- its title, status and option catalogs froze at whatever was true
     * before the link dropped, for the life of the page.
     */
    it('re-asks for an already hydrated agent when its worker comes back', async () => {
      const s = setup()
      const [online, setOnline] = createSignal<ReadonlySet<string>>(new Set(['w1']))
      mockListAgents.mockResolvedValue({ agents: [agentInfo('a1', { title: 'Before' })], verdicts: [] })
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        s.mount(() => online())
        return dispose
      })
      await flush()
      await flush()
      expect(s.view.getAgentTab('a1')?.title).toBe('Before')
      const hydrated = mockListAgents.mock.calls.length

      // The link drops and returns. The tab set never changed, and the tab is
      // still `hydrated`.
      setOnline(new Set<string>())
      await flush()
      mockListAgents.mockResolvedValue({ agents: [agentInfo('a1', { title: 'After' })], verdicts: [] })
      setOnline(new Set(['w1']))
      await flush()
      await flush()

      expect(mockListAgents.mock.calls.length, 'coming back is worth one more ask')
        .toBeGreaterThan(hydrated)
      expect(s.view.getAgentTab('a1')?.title, 'the tab catches up on what it missed').toBe('After')
      d()
    })

    /**
     * The re-ask is what makes this reachable: the batch can now run for a tab
     * that has an in-flight settings edit. The live status handler already
     * routes every catalog through `resolveSettingsTabFields`, and this batch
     * must too, or the worker's older answer lands over what the user just
     * chose.
     */
    it('keeps an in-flight settings edit when a re-ask reply lands', async () => {
      const s = setup()
      const [online, setOnline] = createSignal<ReadonlySet<string>>(new Set(['w1']))
      // `currentValue` is the field the mapper reads. With any other name the
      // reply carries no `optionValues` at all, and the test cannot fail.
      const groups = [{
        id: 'model',
        label: 'Model',
        currentValue: 'sonnet',
        options: [{ id: 'sonnet', label: 'Sonnet' }, { id: 'opus', label: 'Opus' }],
      }]
      mockListAgents.mockResolvedValue({
        agents: [agentInfo('a1', { optionGroups: groups })],
        verdicts: [],
      })
      const d = createRoot((dispose) => {
        s.add(TabType.AGENT, 'a1')
        // The user is changing the model right now.
        s.mount(() => online(), () => new Set(['model']))
        return dispose
      })
      await flush()
      await flush()

      // The optimistic edit the user made while the reply was on its way.
      s.metadata.patch('a1', { optionValues: { model: 'opus' } })
      setOnline(new Set<string>())
      await flush()
      setOnline(new Set(['w1']))
      await flush()
      await flush()

      expect(
        s.view.getAgentTab('a1')?.optionValues?.model,
        'the worker\'s older answer must not land over the pending axis',
      ).toBe('opus')
      d()
    })

    it('restores the attempt budget when a worker gets another tab', async () => {
      vi.useFakeTimers()
      try {
        mockListTerminals.mockRejectedValue(new Error('worker unreachable'))
        const s = setup()
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.mount()
          return dispose
        })
        // Burn the whole budget: 500+1000+2000+4000+8000+10000x3 ≈ 45.5s.
        await vi.advanceTimersByTimeAsync(120_000)
        // One dispatch from the candidate-set effect, then `maxAttempts` retries.
        const exhausted = mockListTerminals.mock.calls.length
        expect(exhausted, 'the cap really is a cap').toBeLessThanOrEqual(9)

        await vi.advanceTimersByTimeAsync(120_000)
        expect(mockListTerminals, 'and nothing re-arms on its own').toHaveBeenCalledTimes(exhausted)

        // The worker's candidate set changes — a peer or the CLI opened one
        // more tab on it. That is new information, so the episode restarts.
        s.add(TabType.TERMINAL, 't2')
        await vi.advanceTimersByTimeAsync(0)
        expect(mockListTerminals, 'the change dispatches immediately').toHaveBeenCalledTimes(exhausted + 1)

        // The point of the reset: the retry after that dispatch must re-arm.
        await vi.advanceTimersByTimeAsync(120_000)
        expect(
          mockListTerminals.mock.calls.length,
          'a restored budget keeps asking, so a worker that comes back is picked up',
        ).toBeGreaterThan(exhausted + 1)
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('keeps retrying a tab the worker answered for with no verdict', async () => {
      vi.useFakeTimers()
      try {
        mockListTerminals.mockResolvedValue({
          terminals: [terminalInfo('t1')],
          verdicts: [
            { tabId: 't1', status: TabHydrationStatus.FOUND },
            { tabId: 't2', status: TabHydrationStatus.UNSPECIFIED },
          ],
        })
        const s = setup()
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.add(TabType.TERMINAL, 't2')
          s.mount()
          return dispose
        })
        await vi.advanceTimersByTimeAsync(0)
        expect(mockListTerminals).toHaveBeenCalledTimes(1)

        await vi.advanceTimersByTimeAsync(1_000)
        const retried = mockListTerminals.mock.calls.slice(1)
        expect(retried.length, 'transient omissions must be re-asked').toBeGreaterThan(0)
        expect(retried[0][1].tabIds, 'and only the unresolved one').toEqual(['t2'])
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('treats an unrecognised verdict as retryable', async () => {
      // An older worker sends no verdicts at all. Degrading to the pre-verdict
      // behaviour is the safe default: never silently retire a live tab.
      vi.useFakeTimers()
      try {
        mockListTerminals.mockResolvedValue({ terminals: [terminalInfo('t1')], verdicts: [] })
        const s = setup()
        const d = createRoot((dispose) => {
          s.add(TabType.TERMINAL, 't1')
          s.add(TabType.TERMINAL, 't2')
          s.mount()
          return dispose
        })
        await vi.advanceTimersByTimeAsync(0)
        await vi.advanceTimersByTimeAsync(1_000)
        expect(mockListTerminals.mock.calls.length).toBeGreaterThan(1)
        d()
      }
      finally {
        vi.useRealTimers()
      }
    })
  })
})
