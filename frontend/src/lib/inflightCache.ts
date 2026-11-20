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
      let resolve!: (value: V | PromiseLike<V>) => void
      let reject!: (reason?: unknown) => void
      const promise = new Promise<V>((res, rej) => {
        resolve = res
        reject = rej
      })
      // Record the flight BEFORE invoking the factory. A factory that throws
      // synchronously completes inside the block below before `run` returns; if
      // the record were set afterwards, that early cleanup would find nothing to
      // delete and the settled entry would stay stuck, handing the stale error
      // to every later caller instead of letting one start a fresh flight.
      pending.set(key, promise)
      void (async () => {
        try {
          resolve(await factory())
        }
        catch (error) {
          reject(error)
        }
        finally {
          // Remove the settled entry only if this flight still owns its slot: a
          // clear() plus a replacement flight must survive this completion.
          if (pending.get(key) === promise) {
            pending.delete(key)
          }
        }
      })()
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
