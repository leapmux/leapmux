import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentGoalAction, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '../../../src/generated/proto/leapmux/v1/common_pb'
import { deepseekHarnessGoalOwner, withDeepseekHarnessGoalCleanup } from './goalCleanupRuntime'

const mocks = vi.hoisted(() => ({ channel: vi.fn(), callWorker: vi.fn(), nativeAgentById: vi.fn() }))
vi.mock('../helpers/api', () => ({ getTestChannel: mocks.channel }))
vi.mock('../helpers/nativeScenario', () => ({ currentNativeAgent: vi.fn(), nativeAgentById: mocks.nativeAgentById }))

beforeEach(() => {
  mocks.channel.mockReset().mockResolvedValue({ callWorker: mocks.callWorker })
  mocks.callWorker.mockReset().mockImplementation(async (_worker: string, method: string) => {
    if (method === 'UpdateAgentGoal')
      return {}
    if (method === 'ListAgentMessages')
      return { goalLoaded: true }
    throw new Error(`The cleanup test received an unexpected Worker method: ${method}.`)
  })
  mocks.nativeAgentById.mockReset()
})

function context() {
  return { leapmuxServer: { hubUrl: 'http://private-hub', adminToken: 'mock-admin', workerId: 'owned-worker' }, modelScript: { releaseGateIfHeld: vi.fn(async (_gate: string) => true) } }
}

describe('deepseekHarnessGoalOwner', () => {
  it('copies and freezes the actual root identity', () => {
    const agent = { id: 'owned-root', parentAgentId: '', rootAgentId: 'owned-root' }
    const owner = deepseekHarnessGoalOwner(agent, 'owned-worker')
    agent.id = 'selected-child'
    expect(owner).toEqual({ agentId: 'owned-root', workerId: 'owned-worker' })
    expect(Object.isFrozen(owner)).toBe(true)
  })

  it.each([
    { id: '', parentAgentId: '', rootAgentId: '' },
    { id: 'selected-child', parentAgentId: 'owned-root', rootAgentId: 'owned-root' },
    { id: 'selected-child', parentAgentId: '', rootAgentId: 'owned-root' },
  ])('rejects an absent or child owner: %j', (agent) => {
    expect(() => deepseekHarnessGoalOwner(agent, 'owned-worker')).toThrow('owned root and Worker')
  })
})

describe('withDeepseekHarnessGoalCleanup', () => {
  it('uses the captured root after the operation changes its caller object', async () => {
    const native = context()
    const owner = { agentId: 'owned-root', workerId: 'owned-worker' }
    await withDeepseekHarnessGoalCleanup(native, owner, ['first-round', 'second-round'], async () => {
      owner.agentId = 'selected-child'
    })
    expect(mocks.callWorker).toHaveBeenNthCalledWith(1, 'owned-worker', 'UpdateAgentGoal', expect.anything(), expect.anything(), { agentId: 'owned-root', action: AgentGoalAction.CLEAR })
    expect(mocks.callWorker).toHaveBeenNthCalledWith(2, 'owned-worker', 'ListAgentMessages', expect.anything(), expect.anything(), { agentId: 'owned-root', limit: 1 })
    expect(native.modelScript.releaseGateIfHeld.mock.calls).toEqual([['first-round'], ['second-round']])
  })

  it('releases every model reply after one release fails', async () => {
    const native = context()
    const failure = new Error('The first model release failed.')
    native.modelScript.releaseGateIfHeld.mockRejectedValueOnce(failure)
    await expect(withDeepseekHarnessGoalCleanup(native, { agentId: 'owned-root', workerId: 'owned-worker' }, ['first-round', 'second-round'], async () => {})).rejects.toMatchObject({ errors: [failure] })
    expect(native.modelScript.releaseGateIfHeld.mock.calls).toEqual([['first-round'], ['second-round']])
  })

  it('refuses another Worker before the operation can start', async () => {
    const native = context()
    const operation = vi.fn(async () => {})
    await expect(withDeepseekHarnessGoalCleanup(native, { agentId: 'owned-root', workerId: 'another-worker' }, [], operation)).rejects.toThrow('scenario Worker')
    expect(operation).not.toHaveBeenCalled()
    expect(mocks.channel).not.toHaveBeenCalled()
  })

  it('closes only the captured root before release when clearing the goal fails', async () => {
    const native = context()
    const failure = new Error('The native goal clear failed.')
    const calls: string[] = []
    mocks.callWorker.mockImplementation(async (_worker: string, method: string) => {
      calls.push(method)
      if (method === 'UpdateAgentGoal')
        throw failure
      if (method === 'CloseAgent')
        return { result: {} }
      throw new Error('The fallback cleanup called an unexpected Worker method.')
    })
    mocks.nativeAgentById.mockResolvedValue({ status: AgentStatus.INACTIVE, closedAt: '2026-10-03T00:00:00Z' })
    native.modelScript.releaseGateIfHeld.mockImplementation(async () => {
      calls.push('release')
      return true
    })
    await expect(withDeepseekHarnessGoalCleanup(native, { agentId: 'owned-root', workerId: 'owned-worker' }, ['held-round'], async () => {})).rejects.toBe(failure)
    expect(mocks.callWorker).toHaveBeenNthCalledWith(2, 'owned-worker', 'CloseAgent', expect.anything(), expect.anything(), { agentId: 'owned-root', worktreeAction: WorktreeAction.KEEP })
    expect(calls).toEqual(['UpdateAgentGoal', 'CloseAgent', 'release'])
  })

  it.each([{ goalLoaded: false }, { goalLoaded: true, goal: { objective: 'The goal remains active.' } }])('refuses an unconfirmed clear and still releases after close refusal: %j', async (snapshot) => {
    const native = context()
    mocks.callWorker.mockImplementation(async (_worker: string, method: string) => {
      if (method === 'UpdateAgentGoal')
        return {}
      if (method === 'ListAgentMessages')
        return snapshot
      if (method === 'CloseAgent')
        return { result: { failureMessage: 'The native root did not close.' } }
      throw new Error('The cleanup called an unexpected Worker method.')
    })
    await expect(withDeepseekHarnessGoalCleanup(native, { agentId: 'owned-root', workerId: 'owned-worker' }, ['held-round'], async () => {})).rejects.toMatchObject({ errors: [expect.objectContaining({ message: 'The Worker did not confirm removal of the captured native goal.' }), expect.objectContaining({ message: 'The Worker did not confirm closure of the captured native root.' })] })
    expect(native.modelScript.releaseGateIfHeld).toHaveBeenCalledExactlyOnceWith('held-round')
  })
})
