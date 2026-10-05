import type { MockModelRequestRecord } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { failedTurnRequests } from './nativeModelError'

function record(fields: Partial<MockModelRequestRecord>): MockModelRequestRecord {
  return { protocol: 'anthropic-messages', path: '/v1/messages', body: {}, ...fields }
}

describe('failedTurnRequests', () => {
  it('returns the requests that consumed the failed turn steps, in step order', () => {
    const retry = record({ stepIndex: 4, body: { attempt: 'retry' } })
    const first = record({ stepIndex: 3, body: { attempt: 'first' } })
    expect(failedTurnRequests([retry, first], 3, 2)).toEqual([first, retry])
  })

  it('leaves out the steps before and after the failed turn', () => {
    const earlier = record({ stepIndex: 2 })
    const failed = record({ stepIndex: 3 })
    const later = record({ stepIndex: 4 })
    expect(failedTurnRequests([earlier, failed, later], 3, 1)).toEqual([failed])
  })

  it('leaves out a request that a rule or the fallback answered', () => {
    const ruled = record({ rule: 'title' })
    const fallback = record({ fallback: true })
    const failed = record({ stepIndex: 0 })
    expect(failedTurnRequests([ruled, fallback, failed], 0, 1)).toEqual([failed])
  })

  it('returns fewer requests than attempts when a step was never consumed', () => {
    expect(failedTurnRequests([record({ stepIndex: 5 })], 5, 2)).toHaveLength(1)
  })

  it('returns no request for an empty record list', () => {
    expect(failedTurnRequests([], 0, 1)).toEqual([])
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses %s attempts', (attempts) => {
    expect(() => failedTurnRequests([], 0, attempts)).toThrow('positive whole number of attempts')
  })

  it.each([-1, 0.5])('refuses a first step of %s', (firstStep) => {
    expect(() => failedTurnRequests([], firstStep, 1)).toThrow('nonnegative first step')
  })
})
