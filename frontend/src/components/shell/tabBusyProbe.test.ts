import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { Tab } from '~/stores/tab.types'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTabBusyProbe } from '~/components/shell/tabBusyProbe'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createAgentActivityStore } from '~/stores/agentActivity.store'

const mockInspect = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  inspectTerminalProcesses: (...args: unknown[]) => mockInspect(...args),
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
  const activity = createAgentActivityStore()
  return { activity, probe: createTabBusyProbe({ activity, tasksFor: () => tasks }) }
}

describe('createTabBusyProbe', () => {
  beforeEach(() => {
    mockInspect.mockReset()
    mockInspect.mockResolvedValue({ terminals: [] })
  })

  describe('agent tabs', () => {
    it('reports a working agent, with its active tasks', async () => {
      const { activity, probe } = makeProbe([task(), task({ rowKey: 'r2', status: 'completed' })])
      activity.setBusy('a1', true)

      const reason = await probe.probe(agentTab('a1'))

      expect(reason).toEqual({ kind: 'agent-turn', activeTasks: [task()] })
      expect(mockInspect).not.toHaveBeenCalled()
    })

    it('reports an idle agent as not busy', async () => {
      const { probe } = makeProbe()

      expect(await probe.probe(agentTab('a1'))).toBeNull()
    })

    it('reports a working agent with no background tasks', async () => {
      const { activity, probe } = makeProbe()
      activity.setBusy('a1', true)

      expect(await probe.probe(agentTab('a1'))).toEqual({ kind: 'agent-turn', activeTasks: [] })
    })

    it('never reports a subagent tab, even a busy one', async () => {
      const { activity, probe } = makeProbe([task()])
      activity.setBusy('child-1', true)

      // A child tab closes in the UI only: the worker treats CloseAgent on a
      // child as tab-close-only, the transcript survives and the tab can be
      // revived. Nothing stops, so there is nothing to warn about.
      expect(await probe.probe(agentTab('child-1', { parentAgentId: 'a1' }))).toBeNull()
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
      const { activity, probe } = makeProbe()
      activity.setBusy('a1', true)

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

    it('makes no request when the set holds no terminals', async () => {
      const { probe } = makeProbe()

      await probe.probeMany([agentTab('a1')])

      expect(mockInspect).not.toHaveBeenCalled()
    })

    it('still reports the agents when a terminal worker fails', async () => {
      mockInspect.mockRejectedValue(new Error('down'))
      const { activity, probe } = makeProbe()
      activity.setBusy('a1', true)

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
