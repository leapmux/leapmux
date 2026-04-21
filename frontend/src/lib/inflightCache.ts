/**
 * Deduplicates concurrent async operations keyed by K. Calls to run(key, factory)
 * while another is in flight for the same key reuse the in-flight promise
 * instead of invoking the factory again; the entry is removed when the
 * promise settles (success or failure).
 *
 * Use this wherever two sidebars / two effects / two callers race to start
 * the same side-effectful fetch and the second should wait on the first.
 */
export interface InflightCache<K, V> {
  /** Returns the in-flight promise for `key`, invoking `factory` only if none exists. */
  run: (key: K, factory: () => Promise<V>) => Promise<V>
  /** Is there currently an in-flight operation for `key`? */
  has: (key: K) => boolean
  /** Drop every tracked entry. Does NOT cancel in-flight work; pending factories keep running. */
  clear: () => void
}

export function createInflightCache<K, V>(): InflightCache<K, V> {
  const pending = new Map<K, Promise<V>>()
  return {
    run(key, factory) {
      const existing = pending.get(key)
      if (existing)
        return existing
      const promise = (async () => {
        try {
          return await factory()
        }
        finally {
          pending.delete(key)
        }
      })()
      pending.set(key, promise)
      return promise
    },
    has(key) {
      return pending.has(key)
    },
    clear() {
      pending.clear()
    },
  }
}
