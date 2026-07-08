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
