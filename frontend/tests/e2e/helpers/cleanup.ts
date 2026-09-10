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
