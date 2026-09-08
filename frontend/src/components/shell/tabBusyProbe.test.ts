import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { Tab } from '~/stores/tab.types'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTabBusyProbe } from '~/components/shell/tabBusyProbe'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'

const mockInspect = vi.fn()
const mockListAgents = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  inspectTerminalProcesses: (...args: unknown[]) => mockInspect(...args),
  listAgents: (...args: unknown[]) => mockListAgents(...args),
}))

function agentTab(id: string, extra: Partial<Tab> = {}): Tab {
  return { type: TabType.AGENT, id, workspaceId: 'ws-1', workerId: 'w-1', ...extra } as Tab
}

function terminalTab(id: string, workerId = 'w-1'): Tab {
  return { type: TabType.TERMINAL, id, workspaceId: 'ws-1', workerId } as Tab
}

function task(overrides: Partial<BackgroundTaskItem> = {}): BackgroundTaskItem {
  return {
    rowKey: 'r1',
    kind: 'subagent',
    title: 'Research the API',
    activity: 'Researching',
    status: 'running',
    ...overrides,
  }
}

function makeProbe(tasks: BackgroundTaskItem[] = []) {
  const seeded = vi.fn()
  return { probe: createTabBusyProbe({ tasksFor: () => tasks, seedActivity: seeded }), seeded }
}

/**
 * What the worker reports for one agent, as ListAgents returns it.
 *
 * The two levels differ only inside the Worker's settle window, so they default
 * to one value and a case that needs the window states both.
 */
function agentIs(id: string, activityState: AgentActivityState, publishedActivityState = activityState) {
  mockListAgents.mockResolvedValue({ agents: [{ id, activityState, publishedActivityState }], verdicts: [] })
}

describe('createTabBusyProbe', () => {
  beforeEach(() => {
    mockInspect.mockReset()
    mockInspect.mockResolvedValue({ terminals: [] })
    mockListAgents.mockReset()
    mockListAgents.mockResolvedValue({ agents: [], verdicts: [] })
  })

  describe('agent tabs', () => {
    it('reports a working agent, with its active tasks', async () => {
      const { probe } = makeProbe([task(), task({ rowKey: 'r2', status: 'completed' })])
      agentIs('a1', AgentActivityState.WORKING)

      const reason = await probe.probe(agentTab('a1'))

      expect(reason).toEqual({ kind: 'agent-turn', activeTasks: [task()] })
      expect(mockListAgents).toHaveBeenCalledWith('w-1', { tabIds: ['a1'] })
      expect(mockInspect).not.toHaveBeenCalled()
    })

    it('asks the worker rather than reading the debounced level', async () => {
      // The Worker holds a settle for three seconds so the completion sound does
      // not ring for work that resumes, which means the level this client holds
      // still says WORKING after the work finished. A guard reading it would
      // raise a confirmation dialog over nothing, and would disagree with the
      // CLI guard.
      const { probe } = makeProbe([task()])
      agentIs('a1', AgentActivityState.IDLE)

      expect(await probe.probe(agentTab('a1'))).toBeNull()
    })

    it('fails open when the worker cannot answer', async () => {
      mockListAgents.mockRejectedValue(new Error('worker unreachable'))
      const { probe } = makeProbe([task()])

      // Refusing a close nobody can confirm strands a tab the user has no other
      // way to shut, which is the same rule the terminal branch keeps.
      expect(await probe.probe(agentTab('a1'))).toBeNull()
    })

    it('seeds the published level and answers the close from the exact one', async () => {
      // The two disagree only inside the settle window: the exact value already
      // carries the settled state while the transition announcing it waits the
      // window out. The close reads the exact one, so it never refuses a close
      // for work that finished. The display takes the published one, because a
      // level written from the exact value drops the spinner early -- and if the
      // work then resumes, the Worker derives what it already published and
      // broadcasts nothing, leaving that tab with no spinner for the rest of the
      // turn.
      const { probe, seeded } = makeProbe([task()])
      agentIs('a1', AgentActivityState.IDLE, AgentActivityState.WORKING)

      expect(await probe.probe(agentTab('a1')), 'the exact level answers the close').toBeNull()
      expect(seeded, 'the published level seeds the display').toHaveBeenCalledWith('a1', AgentActivityState.WORKING)
    })

    it('seeds an agent the close does not warn about', async () => {
      // The seed runs before the busy test, so an idle agent still repairs. A
      // WORKING left over from a link that dropped with no offline sweep is
      // exactly the level no later transition corrects, because the Worker has
      // nothing left to announce.
      const { probe, seeded } = makeProbe()
      agentIs('a1', AgentActivityState.IDLE)

      expect(await probe.probe(agentTab('a1'))).toBeNull()
      expect(seeded).toHaveBeenCalledWith('a1', AgentActivityState.IDLE)
    })

    it('seeds every agent in the batch, on every worker', async () => {
      // The seed sits before the busy test, so a BUSY agent takes one too --
      // the `continue` below it must never skip the repair.
      mockListAgents.mockImplementation((workerId: string) => Promise.resolve({
        agents: workerId === 'w-1'
          ? [{ id: 'a1', activityState: AgentActivityState.WORKING, publishedActivityState: AgentActivityState.WORKING }]
          : [{ id: 'a2', activityState: AgentActivityState.IDLE, publishedActivityState: AgentActivityState.IDLE }],
        verdicts: [],
      }))
      const { probe, seeded } = makeProbe()

      await probe.probeMany([agentTab('a1'), agentTab('a2', { workerId: 'w-2' })])

      expect(seeded).toHaveBeenCalledTimes(2)
      expect(seeded, 'a busy agent seeds too').toHaveBeenCalledWith('a1', AgentActivityState.WORKING)
      expect(seeded).toHaveBeenCalledWith('a2', AgentActivityState.IDLE)
    })

    it('seeds nothing when the worker cannot answer', async () => {
      mockListAgents.mockRejectedValue(new Error('worker unreachable'))
      const { probe, seeded } = makeProbe()

      expect(await probe.probe(agentTab('a1'))).toBeNull()
      expect(seeded, 'a reply that never arrived states no level').not.toHaveBeenCalled()
    })

    it('reports an idle agent as not busy', async () => {
      const { probe } = makeProbe()
      agentIs('a1', AgentActivityState.IDLE)

      expect(await probe.probe(agentTab('a1'))).toBeNull()
    })

    it('reports a working agent with no background tasks', async () => {
      const { probe } = makeProbe()
      agentIs('a1', AgentActivityState.WORKING)

      expect(await probe.probe(agentTab('a1'))).toEqual({ kind: 'agent-turn', activeTasks: [] })
    })

    it('warns about an agent waiting on a permission prompt', async () => {
      // The state the indicator deliberately does NOT spin for. Its turn is
      // still in flight, so this close kills it along with every background task
      // under it -- which is why the guard reads activityInterruptsWork.
      const { probe } = makeProbe([task()])
      agentIs('a1', AgentActivityState.WAITING_FOR_USER)

      expect(await probe.probe(agentTab('a1'))).toEqual({ kind: 'agent-turn', activeTasks: [task()] })
    })

    it('never reports a subagent tab, even a busy one', async () => {
      const { probe, seeded } = makeProbe([task()])
      agentIs('child-1', AgentActivityState.WORKING)

      // A child tab closes in the UI only: the worker treats CloseAgent on a
      // child as tab-close-only, the transcript survives and the tab can be
      // revived. Nothing stops, so there is nothing to warn about.
      expect(await probe.probe(agentTab('child-1', { parentAgentId: 'a1' }))).toBeNull()
      expect(mockListAgents, 'and it is not even asked about').not.toHaveBeenCalled()
      expect(seeded, 'so nothing seeds for it').not.toHaveBeenCalled()
    })
  })

  describe('terminal tabs', () => {
    it('reports the running processes', async () => {
      mockInspect.mockResolvedValue({
        terminals: [{
          terminalId: 't1',
          processes: [{ pid: 51234, name: 'node' }],
          totalCount: 1,
        }],
      })
      const { probe } = makeProbe()

      const reason = await probe.probe(terminalTab('t1'))

      expect(reason).toEqual({
        kind: 'terminal-processes',
        processes: [{ pid: 51234, name: 'node' }],
        totalCount: 1,
      })
      expect(mockInspect).toHaveBeenCalledWith('w-1', { terminalIds: ['t1'] })
    })

    it('reports an idle terminal as not busy', async () => {
      const { probe } = makeProbe()

      expect(await probe.probe(terminalTab('t1'))).toBeNull()
    })

    it('treats an empty process list as idle', async () => {
      // The worker omits an idle terminal, but a zero-length list means the same
      // thing and must not raise a prompt about nothing.
      mockInspect.mockResolvedValue({ terminals: [{ terminalId: 't1', processes: [], totalCount: 0 }] })
      const { probe } = makeProbe()

      expect(await probe.probe(terminalTab('t1'))).toBeNull()
    })

    it('fails open when the worker cannot answer', async () => {
      mockInspect.mockRejectedValue(new Error('worker unreachable'))
      const { probe } = makeProbe()

      // A probe that cannot answer must never block a close. Refusing one nobody
      // can confirm strands a tab the user has no other way to shut.
      expect(await probe.probe(terminalTab('t1'))).toBeNull()
    })
  })

  describe('probeMany', () => {
    it('asks each worker once, not once per tab', async () => {
      mockInspect.mockResolvedValue({ terminals: [] })
      const { probe } = makeProbe()

      await probe.probeMany([terminalTab('t1'), terminalTab('t2'), terminalTab('t3', 'w-2')])

      expect(mockInspect).toHaveBeenCalledTimes(2)
      expect(mockInspect).toHaveBeenCalledWith('w-1', { terminalIds: ['t1', 't2'] })
      expect(mockInspect).toHaveBeenCalledWith('w-2', { terminalIds: ['t3'] })
    })

    it('returns only the busy tabs, titled', async () => {
      mockInspect.mockResolvedValue({
        terminals: [{ terminalId: 't1', processes: [{ pid: 7, name: 'sleep' }], totalCount: 1 }],
      })
      const { probe } = makeProbe()
      agentIs('a1', AgentActivityState.WORKING)

      const busy = await probe.probeMany([
        agentTab('a1', { title: 'Refactor' }),
        agentTab('a2', { title: 'Idle one' }),
        terminalTab('t1'),
        terminalTab('t2'),
      ])

      expect(busy.map(b => b.tab.id)).toEqual(['a1', 't1'])
      expect(busy[0].title).toBe('Refactor')
      expect(busy[0].reason.kind).toBe('agent-turn')
      expect(busy[1].reason.kind).toBe('terminal-processes')
    })

    it('makes no request for a kind the set does not hold', async () => {
      const { probe } = makeProbe()

      await probe.probeMany([agentTab('a1')])

      expect(mockInspect, 'no terminals, so no terminal request').not.toHaveBeenCalled()
      expect(mockListAgents).toHaveBeenCalledWith('w-1', { tabIds: ['a1'] })
    })

    it('still reports the agents when a terminal worker fails', async () => {
      mockInspect.mockRejectedValue(new Error('down'))
      const { probe } = makeProbe()
      agentIs('a1', AgentActivityState.WORKING)

      const busy = await probe.probeMany([agentTab('a1'), terminalTab('t1')])

      // One unreachable worker must not blind the guard to the tabs it CAN
      // answer for.
      expect(busy.map(b => b.tab.id)).toEqual(['a1'])
    })

    it('ignores a tab kind that runs nothing', async () => {
      const { probe } = makeProbe()

      const busy = await probe.probeMany([
        { type: TabType.FILE, id: 'f1', workspaceId: 'ws-1', workerId: 'w-1' } as Tab,
      ])

      expect(busy).toEqual([])
      expect(mockInspect).not.toHaveBeenCalled()
    })
  })
})
