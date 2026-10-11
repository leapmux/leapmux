import { createEffect, createRoot } from 'solid-js'

/** Hand-resolvable promise so tests can observe pending vs. settled state. */
export function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** Two microtask ticks — enough for createEffect + the immediate Promise body to flush. */
export async function flush() {
  await Promise.resolve()
  await Promise.resolve()
}

/**
 * Resolve when `condition` holds. Solid runs `condition` again each time a
 * signal that it reads changes, and the wait stops reading once it resolves.
 *
 * This waits for the EVENT that ends a transient state, so it sets no
 * deadline: the test timeout is the only limit. A poll such as `vi.waitFor`
 * gives up after a wall-clock window instead, and a loaded machine can stall a
 * process past that window while real macrotask work is still queued, such as
 * an IndexedDB request in fake-indexeddb.
 *
 * Use it for a condition that is certain to become true, such as
 * `!auth.loading()`, which every bootstrap path reaches. Then assert the
 * outcome with `expect`, so a failure reports a value and not a timeout.
 */
export function untilTrue(condition: () => boolean): Promise<void> {
  return new Promise((resolve) => {
    createRoot((dispose) => {
      createEffect(() => {
        if (!condition())
          return
        resolve()
        dispose()
      })
    })
  })
}
