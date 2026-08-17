/**
 * Run calls one at a time per key, in the order they arrive.
 *
 * A per-key sequence on the REPLY (see `~/lib/keyedSeq`) decides which
 * answer is applied; it cannot decide which REQUEST the server commits
 * first. A server that merges a partial document under a row lock stores
 * whatever commits LAST, so two fast clicks on one control can leave the
 * server holding the older document while the screen shows the newer one.
 * Serializing the requests per key removes that race at the source.
 *
 * The keys are independent: a call for one key never waits for another.
 */
export interface KeyedQueue {
  /** Run `call` after every call already queued for `key` settles. */
  run: <T>(key: string, call: () => Promise<T>) => Promise<T>
}

/**
 * Start `call` in this tick, and hand a SYNCHRONOUS throw back as a
 * rejection.
 *
 * The queued position gets this for free: `pending.then(call, call)` turns a
 * throw inside `call` into a rejected promise. The first call for a key runs
 * bare, so that it starts now rather than one microtask later, and without
 * this the throw would escape `run` itself — the caller would receive an
 * exception where the signature promises a promise, and the key would record
 * no chain entry for the calls queued behind it.
 */
function runNow<T>(call: () => Promise<T>): Promise<T> {
  try {
    return call()
  }
  catch (error) {
    return Promise.reject(error)
  }
}

export function createKeyedQueue(): KeyedQueue {
  const chain = new Map<string, Promise<unknown>>()
  return {
    run: <T>(key: string, call: () => Promise<T>): Promise<T> => {
      const pending = chain.get(key)
      // The chain holds `settled` below, which never rejects, so the next
      // call runs whether the previous one succeeded or failed. `then(call,
      // call)` keeps that true for whatever the chain holds later: a single
      // refusal must not stall the key.
      const run = pending === undefined ? runNow(call) : pending.then(call, call)
      const settled = run.then(() => {}, () => {})
      chain.set(key, settled)
      // Drop the entry once nothing waits behind it, so a long session
      // does not accumulate one promise per key it ever wrote.
      void settled.then(() => {
        if (chain.get(key) === settled)
          chain.delete(key)
      })
      return run
    },
  }
}
