import type { AutoReviewEntry, NotificationEntry } from './notification'
import { describe, expect, it } from 'vitest'
import { approvedReviewIdsOf, hasCompactionEnd, isCompactionStartOnly, retryEntry, tokenCount, withApprovedReviews } from './notification'

describe('isCompactionStartOnly', () => {
  it('requires at least one entry and only starts', () => {
    const start: NotificationEntry = { kind: 'compaction', phase: 'start' }
    expect(isCompactionStartOnly([])).toBe(false)
    expect(isCompactionStartOnly([start, start])).toBe(true)
    expect(isCompactionStartOnly([start, { kind: 'text', text: 'Keep this' }])).toBe(false)
    expect(isCompactionStartOnly([{ kind: 'compaction', phase: 'end' }])).toBe(false)
  })
})

describe('hasCompactionEnd', () => {
  it.each(['completed', 'failed', 'aborted', 'skipped'] as const)('accepts the %s outcome', (outcome) => {
    expect(hasCompactionEnd([{ kind: 'compaction', phase: 'end', outcome }])).toBe(true)
  })

  it('refuses an empty row and a micro end', () => {
    expect(hasCompactionEnd([])).toBe(false)
    expect(hasCompactionEnd([{ kind: 'compaction', phase: 'start' }])).toBe(false)
    expect(hasCompactionEnd([{ kind: 'compaction', phase: 'end', micro: true }])).toBe(false)
  })
})

describe('retryEntry', () => {
  it.each([undefined, -1, Number.NaN, 1.5, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('omits the invalid counter %s', (value) => {
    const facts = value === undefined ? {} : { attempt: value, maxAttempts: value }
    expect(retryEntry('api', facts)).toStrictEqual({ kind: 'retry', scope: 'api' })
  })

  it('keeps zero and the largest safe integer', () => {
    expect(retryEntry('api', { attempt: 0, maxAttempts: Number.MAX_SAFE_INTEGER })).toStrictEqual({
      kind: 'retry',
      scope: 'api',
      attempt: 0,
      maxAttempts: Number.MAX_SAFE_INTEGER,
    })
  })

  it('trims the error and keeps a fractional delay and explicit flags', () => {
    expect(retryEntry('summarization', { delayMs: 0.5, error: ' refused ', willRetry: false, succeeded: false })).toStrictEqual({
      kind: 'retry',
      scope: 'summarization',
      delayMs: 0.5,
      error: 'refused',
      willRetry: false,
      succeeded: false,
    })
    expect(retryEntry('api', { error: ' \n ', delayMs: -1 })).toStrictEqual({ kind: 'retry', scope: 'api' })
  })
})

describe('tokenCount', () => {
  it.each([undefined, null, '1', {}, -1, Number.NaN, Number.POSITIVE_INFINITY])('omits the invalid count %s', (value) => {
    expect(tokenCount(value)).toBeUndefined()
  })
  it.each([0, 0.5, 100, Number.MAX_VALUE])('keeps the finite count %s', (value) => {
    expect(tokenCount(value)).toBe(value)
  })
})

const review = (reviewId: string): AutoReviewEntry => ({ kind: 'auto-review', reviewId, outcome: 'denied', action: 'make clean', approvable: true })

describe('withApprovedReviews', () => {
  it('marks only the listed reviews', () => {
    const entries: NotificationEntry[] = [review('r1'), { kind: 'status', text: 'Working' }, review('r2')]
    expect(withApprovedReviews(entries, new Set(['r2']))).toEqual([review('r1'), { kind: 'status', text: 'Working' }, { ...review('r2'), approved: true }])
  })

  it('returns the same entries when nothing is approved', () => {
    const entries: NotificationEntry[] = [review('r1')]
    expect(withApprovedReviews(entries, new Set())).toBe(entries)
  })

  it('leaves the input entries unchanged', () => {
    const entries: NotificationEntry[] = [review('r1')]
    withApprovedReviews(entries, new Set(['r1']))
    expect(entries).toEqual([review('r1')])
  })
})

describe('approvedReviewIdsOf', () => {
  it('reads an array of review ids', () => {
    expect(approvedReviewIdsOf(['r1', 'r2'])).toEqual(new Set(['r1', 'r2']))
  })

  it('drops a member that is not a review id', () => {
    expect(approvedReviewIdsOf(['r1', '', 7, null, { id: 'r2' }])).toEqual(new Set(['r1']))
  })

  it.each([undefined, null, 'r1', 7, {}])('reads no approval from %j', (value) => {
    expect(approvedReviewIdsOf(value).size).toBe(0)
  })
})
