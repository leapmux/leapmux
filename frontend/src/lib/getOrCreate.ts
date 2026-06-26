/**
 * Get `map[key]`, creating and inserting it via `factory()` when absent -- the
 * "get-or-vivify a nested container" pattern that several stores hand-rolled as
 * `let v = map.get(k); if (!v) { v = make(); map.set(k, v) }` (a per-agent Map or
 * Set, a per-message memoized parse). Typed structurally on `get`/`set`, so a
 * WeakMap-backed memo cache and a Map-of-Sets registry share one leaf.
 *
 * Absence is detected with `=== undefined` (Map/WeakMap's own absent sentinel), so a
 * legitimately-stored falsy value (`0`, `''`) is NOT re-created -- unlike the `!v`
 * idiom it replaces. Callers must not store a literal `undefined` value (it can't be
 * distinguished from an absent key); none of the vivify call sites do.
 */
export function getOrCreate<K, V>(
  map: { get: (key: K) => V | undefined, set: (key: K, value: V) => unknown },
  key: K,
  factory: () => V,
): V {
  let value = map.get(key)
  if (value === undefined) {
    value = factory()
    map.set(key, value)
  }
  return value
}
