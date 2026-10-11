import { describe, expect, it, vi } from 'vitest'
import { cleanupOnFailure, finishCleanup, withCleanup, withCleanupSync } from './cleanup'

describe('cleanup completion', () => {
  it('handles an empty set and successful operations', async () => {
    await expect(finishCleanup([])).resolves.toBeUndefined()
    await expect(finishCleanup([Promise.resolve(), Promise.resolve(1)])).resolves.toBeUndefined()
  })

  it('waits for every operation and preserves every failure', async () => {
    const first = new Error('first failure')
    const second = new Error('second failure')
    let rejectSecond!: (error: Error) => void
    const pending = new Promise<void>((_resolve, reject) => {
      rejectSecond = reject
    })
    let finished = false
    const outcome = finishCleanup([Promise.reject(first), pending])
      .then(() => null, error => error)
      .finally(() => { finished = true })
    await Promise.resolve()
    await Promise.resolve()
    expect(finished).toBe(false)
    rejectSecond(second)
    expect((await outcome as AggregateError).errors).toEqual([first, second])
  })
})

describe('resource lifetime', () => {
  it('transfers a successfully initialized resource without cleaning it', async () => {
    const cleanup = vi.fn(async () => {})
    await cleanupOnFailure(async () => {}, cleanup)
    expect(cleanup).not.toHaveBeenCalled()
  })

  it('cleans a used resource exactly once', async () => {
    const events: string[] = []
    await withCleanup(async () => {
      events.push('use')
    }, async () => {
      events.push('cleanup')
    })
    expect(events).toEqual(['use', 'cleanup'])
  })

  it.each([undefined, null, 0, '', new Error('operation failed')])('preserves a failed operation and still cleans the resource: %s', async (error) => {
    const cleanup = vi.fn(async () => {})
    await expect(withCleanup(async () => {
      throw error
    }, cleanup)).rejects.toBe(error)
    expect(cleanup).toHaveBeenCalledOnce()
  })

  it('does not repeat cleanup when cleanup itself fails', async () => {
    const error = new Error('cleanup failed')
    const cleanup = vi.fn(async () => {
      throw error
    })
    await expect(withCleanup(async () => {}, cleanup)).rejects.toBe(error)
    expect(cleanup).toHaveBeenCalledOnce()
  })

  it('reports the operation error and cleanup error together', async () => {
    const operation = new Error('operation failed')
    const cleanup = new Error('cleanup failed')
    const result = await withCleanup(async () => {
      throw operation
    }, async () => {
      throw cleanup
    })
      .then(() => null, error => error)
    expect((result as AggregateError).errors).toEqual([operation, cleanup])
  })
})

describe('withCleanupSync', () => {
  /** Return what `operation` throws. A falsy value counts, because `throw undefined` is legal. */
  function thrownBy(operation: () => unknown): unknown {
    try {
      operation()
    }
    catch (error) {
      return error
    }
    throw new Error('The operation did not throw.')
  }

  it('returns the result after it cleans the resource exactly once', () => {
    const events: string[] = []
    expect(withCleanupSync(() => {
      events.push('use')
      return 42
    }, () => {
      events.push('cleanup')
    })).toBe(42)
    expect(events).toEqual(['use', 'cleanup'])
  })

  it.each([undefined, null, 0, '', new Error('operation failed')])('throws a failed operation and still cleans the resource: %s', (error) => {
    const cleanup = vi.fn()
    expect(thrownBy(() => withCleanupSync(() => {
      throw error
    }, cleanup))).toBe(error)
    expect(cleanup).toHaveBeenCalledOnce()
  })

  it('throws a cleanup failure after a successful operation, and does not repeat the cleanup', () => {
    const error = new Error('cleanup failed')
    const cleanup = vi.fn(() => {
      throw error
    })
    expect(() => withCleanupSync(() => 'read', cleanup)).toThrow(error)
    expect(cleanup).toHaveBeenCalledOnce()
  })

  it('reports the operation error and the cleanup error together, in that order', () => {
    const operation = new Error('operation failed')
    const cleanup = new Error('cleanup failed')
    const thrown = thrownBy(() => withCleanupSync(() => {
      throw operation
    }, () => {
      throw cleanup
    }))
    expect(thrown).toBeInstanceOf(AggregateError)
    expect((thrown as AggregateError).errors).toEqual([operation, cleanup])
    expect((thrown as AggregateError).message).toBe('The operation and its cleanup failed')
  })

  it('cleans the resource and refuses an operation that returns a promise', async () => {
    const cleanup = vi.fn()
    const pending = Promise.resolve('late')
    expect(() => withCleanupSync(() => pending, cleanup)).toThrow('requires a synchronous operation')
    expect(cleanup).toHaveBeenCalledOnce()
    await pending
  })
})
