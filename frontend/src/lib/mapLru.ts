/**
 * Drop insertion-order-oldest entries until `map` is within the effective cap (a Map
 * preserves insertion order, so the first key is the oldest / least-recently-set). A
 * caller that re-sets a touched key (delete + set) turns this into an LRU drop.
 * Mutates and returns the same `map`, so it can be used inline in a reactive updater.
 *
 * `opts.protect` keys are never evicted even when oldest -- e.g. the currently-
 * rendered rows, which would otherwise age toward the eviction front and be dropped
 * while still on screen. Because a protected key can never be dropped, the protect set
 * is the floor of what the map can hold, so the EFFECTIVE cap is `max(max,
 * protect.size)`: when more rows are rendered than the configured `max`, the cap rises
 * to fit them rather than leaving the map silently above `max` (a row on screen can't
 * be evicted from the cache that sizes it). Every real caller's `protect` is far below
 * `max`, so the effective cap is normally just `max`; the floor only engages in the
 * pathological large-window case, and even then the map lands at exactly the effective
 * cap -- it is never returned above its own bound.
 *
 * `opts.onEvict` runs for each dropped key BEFORE its deletion (a measured-height
 * cache uses it to subtract the evicted value from a running mean). Deleting a key
 * mid-iteration is well-defined for a Map.
 */
export function capMapInsertionOrder<K, V>(
  map: Map<K, V>,
  max: number,
  opts?: { protect?: ReadonlySet<K>, onEvict?: (key: K) => void },
): Map<K, V> {
  const { protect, onEvict } = opts ?? {}
  // Raise the cap to fit the un-evictable protected keys (see the doc above), so a
  // window rendering more rows than `max` doesn't leave the map stuck above its bound.
  const effectiveMax = protect ? Math.max(max, protect.size) : max
  if (map.size <= effectiveMax)
    return map
  for (const key of map.keys()) {
    if (map.size <= effectiveMax)
      break
    if (protect?.has(key))
      continue
    onEvict?.(key)
    map.delete(key)
  }
  return map
}
