import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

const shared = vi.hoisted(() => ({ exerciseShellToolExecution: vi.fn() }))
vi.mock('../helpers/nativeToolExecution', async (importOriginal) => {
  const original = await importOriginal<typeof import('../helpers/nativeToolExecution')>()
  return { ...original, exerciseShellToolExecution: shared.exerciseShellToolExecution }
})

const context = { provider: AgentProvider.MIMO_CODE } as ManagedNativeScenarioContext

beforeEach(() => {
  vi.clearAllMocks()
  shared.exerciseShellToolExecution.mockResolvedValue(undefined)
})

describe('exerciseMiMoShellToolExecution', () => {
  it('holds each command until its output shows', async () => {
    await exerciseMiMoShellToolExecution(context)
    expect(shared.exerciseShellToolExecution).toHaveBeenCalledWith(context, { outputGate: true })
  })

  it('keeps the options of the caller beside the output gate', async () => {
    const prepare = vi.fn(async () => {})
    await exerciseMiMoShellToolExecution(context, { includeFailure: false, prepare })
    expect(shared.exerciseShellToolExecution).toHaveBeenCalledWith(context, { includeFailure: false, prepare, outputGate: true })
  })

  it('returns after the shared helper completes and rethrows its failure', async () => {
    const failure = new Error('the shell scenario failed')
    shared.exerciseShellToolExecution.mockRejectedValue(failure)
    await expect(exerciseMiMoShellToolExecution(context)).rejects.toBe(failure)
  })
})
