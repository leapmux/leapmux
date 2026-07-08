/**
 * Monotonic-predicate binary searches over an index range [0, n-1].
 *
 * Both assume `pred` is monotonic across the range (all-true then all-false for
 * `largestIndexWhere`; all-false then all-true for `smallestIndexWhere`) -- the shape
 * every offset/seq lookup in the chat virtualizer + scroll rail produces. Kept in one
 * leaf so the virtualizer's offset scans and the rail geometry's row lookup share a
 * single tested implementation instead of each hand-rolling the off-by-one.
 */

/**
 * Largest index in [0, n-1] for which `pred(mid)` holds, or 0 when none does.
 * Lower-bound scan: each satisfied probe raises the floor. `pred` must be
 * true-then-false across the range.
 */
export function largestIndexWhere(n: number, pred: (mid: number) => boolean): number {
  let lo = 0
  let hi = n - 1
  let ans = 0
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1
    if (pred(mid)) {
      ans = mid
      lo = mid + 1
    }
    else {
      hi = mid - 1
    }
  }
  return ans
}

/**
 * Smallest index in [0, n-1] for which `pred(mid)` holds, or `fallback` when none
 * does. Upper-bound scan: each satisfied probe lowers the ceiling. `pred` must be
 * false-then-true across the range.
 */
export function smallestIndexWhere(n: number, pred: (mid: number) => boolean, fallback: number): number {
  let lo = 0
  let hi = n - 1
  let ans = fallback
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1
    if (pred(mid)) {
      ans = mid
      hi = mid - 1
    }
    else {
      lo = mid + 1
    }
  }
  return ans
}

/**
 * Lower bound by seq: the first index in `[0, hi)` whose `seq` is `>= target`, or `hi` when every
 * seq is smaller (the insertion point) -- so `items[idx]?.seq === target` tests membership. The one
 * home for the ascending-by-seq lower-bound lookup the chat window, the marks store, and the rail
 * all repeat, backed by {@link smallestIndexWhere} so the `seq >= target` predicate + off-by-one
 * live in a single tested place. `hi` defaults to the full length; pass a smaller bound to restrict
 * the search to a prefix -- e.g. the ascending server-row region, excluding trailing optimistic
 * locals (seq 0n) that pin to the tail out of seq order.
 */
export function lowerBoundBySeq(items: readonly { seq: bigint }[], target: bigint, hi: number = items.length): number {
  return smallestIndexWhere(hi, mid => items[mid].seq >= target, hi)
}
