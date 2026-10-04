import type { MockModelScenarioStatus } from './mockModelScript'
import { describe, expect, it, vi } from 'vitest'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from './cleanup'
import { completeLiveChildTranscript } from './liveChildTranscript'

const status: MockModelScenarioStatus = { complete: true, nextStep: 2, stepCount: 2, ruleMatches: {}, pendingGates: [], requests: [], unexpectedRequests: [] }

describe('completeLiveChildTranscript', () => {
  it('waits for queued model steps before provider completion work', async () => {
    const steps = deferred<void>()
    const entered = deferred<void>()
    const completion = vi.fn(async () => {})
    const finished = completeLiveChildTranscript({ waitForSteps: async () => {
      entered.resolve()
      await steps.promise
      return status
    } }, completion)
    await withCleanup(async () => {
      await entered.promise
      expect(completion).not.toHaveBeenCalled()
      steps.resolve()
      await finished
      expect(completion).toHaveBeenCalledOnce()
    }, async () => {
      steps.resolve()
      await finished
    })
  })

  it('preserves a provider completion failure', async () => {
    const failure = new Error('The native parent report failed.')
    await expect(completeLiveChildTranscript({ waitForSteps: async () => status }, async () => {
      throw failure
    })).rejects.toBe(failure)
  })

  it('keeps the initial step wait when the provider supplies no completion hook', async () => {
    const waitForSteps = vi.fn(async () => status)
    await completeLiveChildTranscript({ waitForSteps })
    expect(waitForSteps).toHaveBeenCalledOnce()
  })
})
