/**
 * Resolve after `ms` milliseconds. A promisified `setTimeout`.
 *
 * An aborted `signal` REJECTS the promise with the signal's reason, both
 * before the wait starts and during it, and clears the timer. A caller
 * that waits between retries needs the abort to end the wait itself: a
 * backoff that keeps sleeping after its caller gave up holds the whole
 * retry sequence open for as long as the delay lasts.
 */
export function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason ?? new Error('aborted'))
      return
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    function onAbort() {
      clearTimeout(timer)
      reject(signal?.reason ?? new Error('aborted'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}
