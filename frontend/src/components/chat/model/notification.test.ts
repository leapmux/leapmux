import type { AutoReviewEntry, NotificationEntry } from './notification'
import { describe, expect, it } from 'vitest'
import { approvedReviewIdsOf, withApprovedReviews } from './notification'

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
