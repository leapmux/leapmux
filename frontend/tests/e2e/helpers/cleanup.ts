/** Wait for every cleanup operation before reporting all failures. */
export async function finishCleanup(operations: Promise<unknown>[]): Promise<void> {
  const results = await Promise.allSettled(operations)
  const errors = results.flatMap(result => result.status === 'rejected' ? [result.reason] : [])
  if (errors.length > 0)
    throw new AggregateError(errors, 'Test cleanup failed')
}

/** Release a partially constructed resource if initialization fails. */
export async function cleanupOnFailure<T>(operation: () => Promise<T>, cleanup: () => Promise<void>): Promise<T> {
  try {
    return await operation()
  }
  catch (error) {
    try {
      await cleanup()
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The operation and its cleanup failed')
    }
    throw error
  }
}

/** Release a resource after the operation succeeds or fails. */
export async function withCleanup<T>(operation: () => Promise<T>, cleanup: () => Promise<void>): Promise<T> {
  const result = await cleanupOnFailure(operation, cleanup)
  await cleanup()
  return result
}

/**
 * Release a resource after a synchronous operation succeeds or fails, with the error rules of `withCleanup`:
 * - A failed operation and a failed cleanup throw one `AggregateError` that holds both errors, in that order.
 * - A failed operation alone throws its own error.
 * - A failed cleanup after a successful operation throws the cleanup error.
 *
 * An operation that returns a promise is a caller error, because the cleanup would run before the promise settles.
 * The function runs the cleanup and throws for such an operation. Use `withCleanup` for an asynchronous operation.
 */
export function withCleanupSync<T>(operation: () => T, cleanup: () => void): T {
  let result: T
  try {
    result = operation()
  }
  catch (error) {
    try {
      cleanup()
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The operation and its cleanup failed')
    }
    throw error
  }
  cleanup()
  if (isPromiseLike(result))
    throw new TypeError('withCleanupSync requires a synchronous operation. Use withCleanup for an operation that returns a promise.')
  return result
}

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return (typeof value === 'object' || typeof value === 'function') && value !== null && typeof (value as { then?: unknown }).then === 'function'
}
