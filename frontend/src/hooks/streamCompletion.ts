// Returns a Promise that resolves when the handle ends or the signal
// aborts, and rejects when the handle errors. The end / error / abort
// callbacks are wired synchronously so a signal that fires — or a
// buffered end/error that flushes — before the returned Promise is
// awaited still terminates the wait. Without this captured-flag dance,
// a synchronous abort fired between callback wiring and the await
// would leave the consumer hung.

export interface StreamCompletionHandle {
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
}

export function waitForStreamCompletion(
  handle: StreamCompletionHandle,
  signal: AbortSignal,
): Promise<void> {
  if (signal.aborted)
    return Promise.resolve()

  let streamEnded = false
  let streamAborted = false
  let streamError: unknown = null
  let resolveStream: (() => void) | undefined
  let rejectStream: ((err: unknown) => void) | undefined

  const onAbort = () => {
    if (resolveStream)
      resolveStream()
    else
      streamAborted = true
  }
  signal.addEventListener('abort', onAbort, { once: true })

  handle.onEnd(() => {
    signal.removeEventListener('abort', onAbort)
    if (resolveStream)
      resolveStream()
    else
      streamEnded = true
  })

  handle.onError((err) => {
    signal.removeEventListener('abort', onAbort)
    if (rejectStream)
      rejectStream(err)
    else
      streamError = err
  })

  return new Promise<void>((resolve, reject) => {
    if (streamError) {
      reject(streamError)
      return
    }
    if (streamEnded || streamAborted) {
      resolve()
      return
    }
    resolveStream = resolve
    rejectStream = reject
  })
}
