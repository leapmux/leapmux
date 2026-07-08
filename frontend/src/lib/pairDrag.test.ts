import { describe, expect, it } from 'vitest'
import { rebalancePair } from '~/lib/pairDrag'

describe('rebalancePair', () => {
  it('returns the unchanged pair when delta is 0', () => {
    const [a, b] = rebalancePair(0.4, 1.0, 0, 0.05)
    expect(a).toBeCloseTo(0.4, 10)
    expect(b).toBeCloseTo(0.6, 10)
  })

  it('shifts the boundary by delta within the valid range', () => {
    const [a, b] = rebalancePair(0.4, 1.0, 0.1, 0.05)
    expect(a).toBeCloseTo(0.5, 10)
    expect(b).toBeCloseTo(0.5, 10)
  })

  it('clamps newA at the absolute floor', () => {
    const [a, b] = rebalancePair(0.4, 1.0, -10, 0.05)
    expect(a).toBeCloseTo(0.05, 10)
    expect(b).toBeCloseTo(0.95, 10)
  })

  it('clamps newA at sumPair − floor (so newB hits the floor)', () => {
    const [a, b] = rebalancePair(0.4, 1.0, 10, 0.05)
    expect(a).toBeCloseTo(0.95, 10)
    expect(b).toBeCloseTo(0.05, 10)
  })

  it('preserves the pair sum exactly under any delta (the reciprocal invariant)', () => {
    const cases: Array<[number, number, number]> = [
      [0.4, 1.0, 0.123],
      [0.4, 1.0, -0.456],
      [0.4, 1.0, 999],
      [0.4, 1.0, -999],
      [0.5, 0.5, 0],
      [0.05, 0.5, 0.001],
    ]
    for (const [startA, sumPair, delta] of cases) {
      const [a, b] = rebalancePair(startA, sumPair, delta, 0.05)
      expect(a + b).toBeCloseTo(sumPair, 10)
    }
  })

  it('honors a relative floor (MIN_FRACTION × sumPair, used by the sidebar handle)', () => {
    // sumPair = 0.6, floor = 0.15 × 0.6 = 0.09 — neither side may dip below 9%
    // of the pair (15% of what the pair owns).
    const sumPair = 0.6
    const floor = 0.15 * sumPair
    const [aMax] = rebalancePair(0.3, sumPair, 999, floor)
    expect(aMax).toBeCloseTo(sumPair - floor, 10)
    const [aMin] = rebalancePair(0.3, sumPair, -999, floor)
    expect(aMin).toBeCloseTo(floor, 10)
  })

  it('keeps both sides ≥ floor for any delta when sumPair ≥ 2 × floor (the documented precondition)', () => {
    const sumPair = 0.5
    const floor = 0.05 // 2 × floor = 0.1 ≤ 0.5 ✓
    for (const delta of [-1, -0.5, -0.05, 0, 0.05, 0.5, 1]) {
      const [a, b] = rebalancePair(0.25, sumPair, delta, floor)
      expect(a).toBeGreaterThanOrEqual(floor - 1e-12)
      expect(b).toBeGreaterThanOrEqual(floor - 1e-12)
    }
  })
})
