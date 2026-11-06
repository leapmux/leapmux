import type { AgentInputProcessRecovery, AgentInputRecoveryTarget } from '~/generated/proto/leapmux/v1/agent_pb'
import type { InputRecoveryTarget } from '~/models/agentInputRecovery'
import type { AgentTab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInputProcessRecoverySchema, AgentInputQueueSnapshotSchema, AgentInputRecoveryTargetSchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { deferred } from '~/test-support/async'
import { createAgentInputRecoveryOperations } from './agentInputRecoveryOperations'

const rpc = vi.hoisted(() => ({
  listAgentInputRecoveries: vi.fn(),
  recoverAgentInputProcess: vi.fn(),
}))
vi.mock('~/api/workerRpc', () => ({ workerAgentInput: rpc }))

function recovery(generation = 1n): AgentInputProcessRecovery {
  return create(AgentInputProcessRecoverySchema, {
    outputGeneration: generation,
    agentSessionId: `session-${generation}`,
    agentProvider: AgentProvider.CODEX,
    processInstanceId: `process-${generation}`,
  })
}

function tab(id: string, workerId: string): AgentTab {
  return {
    id,
    workerId,
    type: TabType.AGENT,
    workspaceId: 'workspace',
    title: id,
    agentProvider: AgentProvider.CODEX,
  }
}

function fixture(initial = [
  tab('current', 'worker-a'),
  tab('other', 'worker-b'),
]) {
  const tabs = [...initial]
  const apply = vi.fn()
  const actions = createAgentInputRecoveryOperations({
    view: {
      all: () => tabs,
      getAgentTab: id => tabs.find(candidate => candidate.id === id),
    },
    store: { apply },
    workerName: id => `Host ${id}`,
  })
  return { tabs, actions, apply }
}

function candidate(agentId: string, workerId = 'worker-a', generation = 1n): InputRecoveryTarget {
  return {
    agentId,
    workerId,
    workerName: `Host ${workerId}`,
    title: agentId,
    recovery: recovery(generation),
  }
}

beforeEach(() => {
  rpc.listAgentInputRecoveries.mockReset()
  rpc.recoverAgentInputProcess.mockReset()
})

describe('createAgentInputRecoveryOperations', () => {
  it('captures the worker and process before later tab and input changes', async () => {
    const { tabs, actions } = fixture()
    const original = recovery(9_007_199_254_740_993n)
    const target = actions.captureTarget('current', original)
    original.agentSessionId = 'changed-session'
    original.processInstanceId = 'changed-process'
    tabs[0]!.workerId = 'different-worker'
    rpc.recoverAgentInputProcess.mockResolvedValue({
      snapshot: create(AgentInputQueueSnapshotSchema, { revision: 2n }),
    })
    await actions.recoverTargets([target])
    expect(rpc.recoverAgentInputProcess).toHaveBeenCalledExactlyOnceWith('worker-a', {
      agentId: 'current',
      recovery: expect.objectContaining({
        outputGeneration: 9_007_199_254_740_993n,
        agentSessionId: 'session-9007199254740993',
        processInstanceId: 'process-9007199254740993',
      }),
    })
  })

  it('discovers all known workers without retargeting the original process', async () => {
    const { actions } = fixture()
    const current = actions.captureTarget('current', recovery())
    const otherRecovery = recovery(2n)
    const otherRecord = create(AgentInputRecoveryTargetSchema, { agentId: 'other', title: 'Other tab', recovery: otherRecovery })
    rpc.listAgentInputRecoveries.mockImplementation(async (workerId: string) => ({
      targets: workerId === 'worker-a'
        ? [
            create(AgentInputRecoveryTargetSchema, {
              agentId: 'current',
              title: 'New current process',
              recovery: recovery(8n),
            }),
          ]
        : [
            otherRecord,
            create(AgentInputRecoveryTargetSchema, {
              agentId: 'not-in-the-user-view',
              recovery: recovery(),
            }),
          ],
    }))
    const found = await actions.discoverTargets(current)
    otherRecord.recovery!.agentSessionId = 'mutated-after-discovery'
    otherRecord.recovery!.processInstanceId = 'mutated-process-after-discovery'
    expect(rpc.listAgentInputRecoveries).toHaveBeenCalledWith('worker-a', { agentProvider: AgentProvider.CODEX })
    expect(rpc.listAgentInputRecoveries).toHaveBeenCalledWith('worker-b', { agentProvider: AgentProvider.CODEX })
    expect(found.failures).toEqual([])
    expect(found.targets.map(target => [target.workerId, target.agentId])).toEqual([
      ['worker-a', 'current'],
      ['worker-b', 'other'],
    ])
    expect(found.current.recovery.outputGeneration).toBe(1n)
    expect(found.current.recovery.processInstanceId).toBe('process-1')
    expect(found.targets[1]?.recovery.agentSessionId).toBe('session-2')
    expect(found.targets[1]?.recovery.processInstanceId).toBe('process-2')
  })

  it('excludes tabs that appear after the discovery snapshot', async () => {
    const { tabs, actions } = fixture([
      tab('current', 'worker-a'),
    ])
    const response = deferred<{ targets: AgentInputRecoveryTarget[] }>()
    rpc.listAgentInputRecoveries.mockReturnValue(response.promise)
    const pending = actions.discoverTargets(actions.captureTarget('current', recovery()))
    tabs.push(tab('late', 'worker-a'))
    response.resolve({
      targets: [
        create(AgentInputRecoveryTargetSchema, {
          agentId: 'late',
          recovery: recovery(2n),
        }),
      ],
    })
    const found = await pending
    expect(found.targets.map(target => target.agentId)).toEqual(['current'])
  })

  it('reports failed and invalid worker discovery without losing the current target', async () => {
    const { actions } = fixture()
    rpc.listAgentInputRecoveries.mockImplementation(async (workerId: string) => {
      if (workerId === 'worker-b')
        throw new Error('Worker disconnected')
      return {
        targets: [
          create(AgentInputRecoveryTargetSchema, { agentId: 'current' }),
        ],
      }
    })
    const found = await actions.discoverTargets(actions.captureTarget('current', recovery()))
    expect(found.targets).toHaveLength(1)
    expect(found.failures.map(failure => failure.workerId)).toEqual(['worker-a', 'worker-b'])
    expect(found.failures[0]?.message).toContain('invalid recovery target')
    expect(found.failures[1]?.message).toBe('Worker disconnected')
    expect(rpc.recoverAgentInputProcess).not.toHaveBeenCalled()
  })

  it('keeps independent results and applies snapshots only after a confirmed response', async () => {
    const { actions, apply } = fixture()
    const snapshot = create(AgentInputQueueSnapshotSchema, { revision: 7n })
    rpc.recoverAgentInputProcess.mockImplementation(async (workerId: string) => {
      if (workerId === 'worker-b')
        throw new Error('The process changed')
      return { snapshot }
    })
    const targets = [
      candidate('first'),
      candidate('second', 'worker-b'),
    ]
    const results = await actions.recoverTargets(targets)
    expect(results.map(result => result.status)).toEqual(['recovered', 'unconfirmed'])
    expect(results[1]).toMatchObject({
      target: { workerId: 'worker-b', agentId: 'second' },
      message: 'The process changed',
    })
    expect(apply).toHaveBeenCalledExactlyOnceWith('first', snapshot)
    expect(rpc.recoverAgentInputProcess).toHaveBeenCalledTimes(2)
  })

  it('does not claim recovery when the queue response is missing or still reports an issue', async () => {
    const { actions, apply } = fixture()
    const stillRequired = create(AgentInputQueueSnapshotSchema, {
      revision: 3n,
      processRecovery: recovery(3n),
    })
    rpc.recoverAgentInputProcess.mockResolvedValueOnce({}).mockResolvedValueOnce({ snapshot: stillRequired })
    const results = await actions.recoverTargets([
      candidate('first'),
      candidate('second'),
    ])
    expect(results.map(result => result.status)).toEqual(['unconfirmed', 'unconfirmed'])
    expect(apply).toHaveBeenCalledExactlyOnceWith('second', stillRequired)
    expect(rpc.recoverAgentInputProcess).toHaveBeenCalledTimes(2)
  })

  it('limits concurrent recovery requests to four and preserves result order', async () => {
    const { actions } = fixture()
    const release = deferred<void>()
    let active = 0
    let maximum = 0
    rpc.recoverAgentInputProcess.mockImplementation(async () => {
      active++
      maximum = Math.max(maximum, active)
      try {
        await release.promise
        return {
          snapshot: create(AgentInputQueueSnapshotSchema),
        }
      }
      finally {
        active--
      }
    })
    const targets = Array.from({ length: 21 }, (_, index) => candidate(`agent-${index}`))
    const pending = actions.recoverTargets(targets)
    expect(rpc.recoverAgentInputProcess).toHaveBeenCalledTimes(4)
    release.resolve()
    const results = await pending
    expect(maximum).toBe(4)
    expect(results.map(result => result.target.agentId)).toEqual(targets.map(target => target.agentId))
    expect(results.every(result => result.status === 'recovered')).toBe(true)
  })

  it('accepts an empty batch and rejects duplicate approvals before making a request', async () => {
    const { actions } = fixture()
    await expect(actions.recoverTargets([])).resolves.toEqual([])
    await expect(actions.recoverTargets([
      candidate('same'),
      candidate('same', 'worker-a', 2n),
    ])).rejects.toThrow('only once')
    expect(rpc.recoverAgentInputProcess).not.toHaveBeenCalled()
  })

  it('refuses an unavailable worker and an incomplete process target', () => {
    const { actions } = fixture()
    expect(() => actions.captureTarget('absent', recovery())).toThrow('worker')
    expect(() => actions.captureTarget('current', recovery(0n))).toThrow('incomplete')
    const missingInstance = recovery()
    missingInstance.processInstanceId = ''
    expect(() => actions.captureTarget('current', missingInstance)).toThrow('incomplete')
    expect(() => actions.captureTarget('current', create(AgentInputProcessRecoverySchema))).toThrow('incomplete')
  })
})
