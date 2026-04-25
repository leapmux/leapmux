import { shallowEqual } from './shallowEqual'

/**
 * Identity-stabilizing cache for lists fetched repeatedly from a backend.
 *
 * Solid's `<For>` keys items by object reference, so a list whose items
 * are freshly-deserialized objects on every fetch (typical of protobuf-es
 * / Tauri-IPC results) causes the entire DOM subtree under the For to
 * unmount and remount on every refresh — even when nothing actually
 * changed. That's a tens-of-millis main-thread block per refresh, with
 * second-order effects on layout (notably, a browser-internal scrollTop
 * clamp on unrelated overflow containers under the same flex root).
 *
 * `createIdentityCache` substitutes the previously-cached object reference
 * for any item whose key is unchanged AND whose `equals` comparison
 * returns true. The For diff then becomes "drop the items that
 * disappeared, mount the items that arrived, leave the rest alone".
 */
export interface IdentityCacheOptions<T> {
  /**
   * Returns a stable string key for the item. Items with the same key
   * across refreshes are eligible for identity reuse.
   */
  keyOf: (item: T) => string
  /**
   * Returns true when the cached and fresh item are content-equivalent
   * (so the cached reference can be reused). Defaults to a shallow
   * Object.is comparison across all enumerable own properties — correct
   * for plain proto messages whose fields are primitives. Override when
   * the type has nested object/array fields whose contents matter for
   * rendering.
   */
  equals?: (cached: T, fresh: T) => boolean
}

export interface IdentityCache<T> {
  /**
   * Returns a list with cached object references substituted in for any
   * unchanged items. Items whose key did not appear in the input are
   * evicted from the cache.
   */
  stabilize: (list: readonly T[]) => T[]
  /** Drops all cached entries. Mainly useful for tests. */
  clear: () => void
}

export function createIdentityCache<T>(opts: IdentityCacheOptions<T>): IdentityCache<T> {
  const cache = new Map<string, T>()
  const equals = opts.equals ?? ((a, b) => shallowEqual(a, b))

  return {
    stabilize(list) {
      const seen = new Set<string>()
      const out = list.map((fresh) => {
        const key = opts.keyOf(fresh)
        seen.add(key)
        const prior = cache.get(key)
        if (prior && equals(prior, fresh))
          return prior
        cache.set(key, fresh)
        return fresh
      })
      for (const key of cache.keys()) {
        if (!seen.has(key))
          cache.delete(key)
      }
      return out
    },
    clear() {
      cache.clear()
    },
  }
}
