import { describe, expect, it, vi } from 'vitest'
import { cleanupOnFailure, finishCleanup, withCleanup } from './cleanup'

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
