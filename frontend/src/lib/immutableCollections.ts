// ---------------------------------------------------------------------------
// Copy-on-write single-key edits for ReadonlySet / ReadonlyMap
//
// Solid signals holding a Set/Map re-render only when the reference changes, so
// every membership edit clones the collection. The "clone, edit one key, but
// return the SAME reference when the edit is a no-op" idiom recurred verbatim
// across the chat premeasure queue and skeleton-crossfade state machines; the
// no-op short-circuit is load-bearing (a churned reference re-runs every
// subscriber for a change that didn't happen). Centralizing it keeps the
// short-circuit from being forgotten at one site and drifting from the others.
// ---------------------------------------------------------------------------

/** Add `value` to `set`, returning a NEW set — or `set` itself when it already holds `value`. */
export function setWith<T>(set: ReadonlySet<T>, value: T): ReadonlySet<T> {
  if (set.has(value))
    return set
  const next = new Set(set)
  next.add(value)
  return next
}

/** Remove `value` from `set`, returning a NEW set — or `set` itself when it doesn't hold `value`. */
export function setWithout<T>(set: ReadonlySet<T>, value: T): ReadonlySet<T> {
  if (!set.has(value))
    return set
  const next = new Set(set)
  next.delete(value)
  return next
}

/**
 * Set `key` to `value` in `map`, returning a NEW map — or `map` itself when it already
 * maps `key` to the SAME value (`Object.is`), so a redundant write churns no reference.
 */
export function mapWith<K, V>(map: ReadonlyMap<K, V>, key: K, value: V): ReadonlyMap<K, V> {
  if (map.has(key) && Object.is(map.get(key), value))
    return map
  const next = new Map(map)
  next.set(key, value)
  return next
}

/** Remove `key` from `map`, returning a NEW map — or `map` itself when it doesn't hold `key`. */
export function mapWithout<K, V>(map: ReadonlyMap<K, V>, key: K): ReadonlyMap<K, V> {
  if (!map.has(key))
    return map
  const next = new Map(map)
  next.delete(key)
  return next
}
