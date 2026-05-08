/**
 * Rebalance a pair of fractional sizes against an absolute floor while
 * preserving their sum. Given `startA` and `sumPair = startA + startB`,
 * shift the boundary by `deltaRatio` (the drag distance expressed as a
 * fraction of the same total) and return `[newA, newB]` where each side is
 * at least `floor` and `newA + newB === sumPair`.
 *
 * Callers control the floor's interpretation:
 *   - `useTileDragResize` / `startPairRebalanceDrag` pass the absolute
 *     `MIN_SPLIT_RATIO` (5%) — symmetric tile floor.
 *   - `useResizeHandle` (sidebar) passes `MIN_FRACTION * sumPair` (15%
 *     of the pair) — relative floor that scales with what the pair owns.
 *
 * Precondition: `sumPair >= 2 * floor`. Below that the clamp produces an
 * inconsistent result and the call site should bail before invoking.
 */
export function rebalancePair(
  startA: number,
  sumPair: number,
  deltaRatio: number,
  floor: number,
): [number, number] {
  const newA = Math.max(floor, Math.min(sumPair - floor, startA + deltaRatio))
  return [newA, sumPair - newA]
}
