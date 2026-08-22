/**
 * Order-insensitive membership equality for a `Set` or a `Map`'s keys.
 *
 * "Did the membership change?" was answered five different ways across the
 * shell — two hand-written loops (`AppShell`'s metadata sweep,
 * repo-git refresh routing), a third in
 * `floatingWindow.store`, and two sorted-string-join surrogates
 * (`useTabHydrators`, `useWorkerPrivateStreams`). Five copies of a five-line
 * loop is well past the point where a fix to one leaves the others wrong.
 *
 * The join surrogates are worth replacing outright rather than keeping: they
 * pay an O(n log n) sort plus a string allocation on every CRDT tick to answer
 * what a size-plus-membership check answers in O(n) with no allocation — and
 * `ids.join(' ')` can compare EQUAL for two different sets if any id contains
 * the separator, which nothing about a tab id forbids.
 *
 * Accepts anything with `size` and `has`, so a `Set<K>` and a `Map<K, V>`
 * (whose `has` tests keys) both work. Values are not compared — this answers
 * membership only, which is exactly what every call site wants.
 */
export function sameKeys<K>(
  a: { size: number, has: (key: K) => boolean, keys: () => IterableIterator<K> } | null | undefined,
  b: { size: number, has: (key: K) => boolean, keys: () => IterableIterator<K> } | null | undefined,
): boolean {
  if (a === b)
    return true
  if (!a || !b || a.size !== b.size)
    return false
  for (const key of a.keys()) {
    if (!b.has(key))
      return false
  }
  return true
}
