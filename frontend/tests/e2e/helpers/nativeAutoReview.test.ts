import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { runReviewedNativeToolTurn } from './nativeAutoReview'
import { expectNoNativeControl } from './nativeControlObservation'
import { runNativeToolTurn } from './nativeToolExecution'

vi.mock('./nativeControlObservation', () => ({
  expectNoNativeControl: vi.fn(async (_context, options) => options.relatedProof()),
}))
vi.mock('./nativeToolExecution', () => ({ runNativeToolTurn: vi.fn() }))

beforeEach(() => vi.clearAllMocks())

function fixture(before: number, after: number | undefined) {
  const status = vi.fn()
    .mockResolvedValueOnce({ ruleMatches: { reviewer: before } })
    .mockResolvedValueOnce({ ruleMatches: { reviewer: after } })
  const context = { modelScript: { status } } as unknown as ManagedNativeScenarioContext
  const requests = { start: 2, toolRequest: {}, resultRequest: {} } as Awaited<ReturnType<typeof runNativeToolTurn>>
  vi.mocked(runNativeToolTurn).mockResolvedValue(requests)
  return { context, status, requests }
}

const turn = { toolCalls: [{ id: 'tool', name: 'write', arguments: {} }], prompt: 'Write the fixture.', answer: 'The write ended.' }

describe('runReviewedNativeToolTurn', () => {
  it.each([0, 3])('requires one new review after %s previous reviews and refuses manual approvals', async (before) => {
    const { context, status, requests } = fixture(before, before + 1)
    const result = await runReviewedNativeToolTurn(context, 'reviewer', turn)
    expect(expectNoNativeControl).toHaveBeenCalledWith(context, { testId: 'control-banner', relatedProof: expect.any(Function) })
    expect(runNativeToolTurn).toHaveBeenCalledExactlyOnceWith(context, { ...turn, permissions: 'none' })
    expect(status).toHaveBeenCalledTimes(2)
    expect(result.start).toBe(requests.start)
  })

  it.each([undefined, 0, 2, 4])('rejects a missing, stale, or repeated review count: %s', async (after) => {
    const { context } = fixture(2, after)
    await expect(runReviewedNativeToolTurn(context, 'reviewer', turn)).rejects.toThrow('decides this operation exactly once')
  })

  it.each(['', '  '])('rejects an empty reviewer identity before reading the model script: %j', async (name) => {
    const { context, status } = fixture(0, 1)
    await expect(runReviewedNativeToolTurn(context, name, turn)).rejects.toThrow('requires a rule name')
    expect(status).not.toHaveBeenCalled()
    expect(runNativeToolTurn).not.toHaveBeenCalled()
  })

  it('preserves native execution failures without reading a completion receipt', async () => {
    const { context, status } = fixture(0, 1)
    const error = new Error('The native execution failed.')
    vi.mocked(runNativeToolTurn).mockRejectedValue(error)
    await expect(runReviewedNativeToolTurn(context, 'reviewer', turn)).rejects.toBe(error)
    expect(status).toHaveBeenCalledTimes(1)
  })
})
