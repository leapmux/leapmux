/// <reference types="vitest/globals" />
import { describe, expect, it, vi } from 'vitest'
import { createWorkspaceAccessAnnouncer } from './workspaceAccess'

/** A `prepare` whose resolution the test controls. */
function deferredPrepare() {
  const resolvers: { resolve: () => void, reject: (e: Error) => void }[] = []
  const prepare = vi.fn(() => new Promise<void>((resolve, reject) => {
    resolvers.push({ resolve: () => resolve(), reject })
  }))
  return { prepare, resolvers }
}

describe('createWorkspaceAccessAnnouncer', () => {
  it('announces a pair once and reports who did it', async () => {
    const prepare = vi.fn().mockResolvedValue(undefined)
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    expect(await ensure('w1', 'ws1'), 'this call announced it').toBe(true)
    expect(await ensure('w1', 'ws1'), 'this one found it already announced').toBe(false)
    expect(await ensure('w1', 'ws1')).toBe(false)

    expect(prepare, 'one PrepareWorkspaceAccess for the life of the page').toHaveBeenCalledTimes(1)
    expect(prepare).toHaveBeenCalledWith('w1', 'ws1')
  })

  /**
   * The whole point of the boolean: a caller that was refused re-issues its
   * request only when an announcement actually landed. Both racers were refused
   * before this announcement, so both have a reason to retry.
   */
  it('shares one in-flight announcement, and tells both racers it landed', async () => {
    const { prepare, resolvers } = deferredPrepare()
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    const a = ensure('w1', 'ws1')
    const b = ensure('w1', 'ws1')
    expect(prepare, 'the second caller joined the first attempt').toHaveBeenCalledTimes(1)

    resolvers[0].resolve()
    expect(await a).toBe(true)
    expect(await b).toBe(true)
    expect(await ensure('w1', 'ws1'), 'but a later caller is told it was already done').toBe(false)
  })

  it('does not remember a failure, so a later caller retries', async () => {
    const prepare = vi.fn()
      .mockRejectedValueOnce(new Error('hub unreachable'))
      .mockResolvedValue(undefined)
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    await expect(ensure('w1', 'ws1'), 'the failure reaches the caller').rejects.toThrow('hub unreachable')
    expect(await ensure('w1', 'ws1'), 'and the next attempt is not short-circuited').toBe(true)
    expect(prepare).toHaveBeenCalledTimes(2)
  })

  it('propagates a failure to every racer without wedging the pair', async () => {
    const { prepare, resolvers } = deferredPrepare()
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    const a = ensure('w1', 'ws1')
    const b = ensure('w1', 'ws1')
    resolvers[0].reject(new Error('boom'))
    await expect(a).rejects.toThrow('boom')
    await expect(b).rejects.toThrow('boom')

    resolvers.length = 0
    const c = ensure('w1', 'ws1')
    expect(prepare, 'the pair is retryable again').toHaveBeenCalledTimes(2)
    resolvers[0].resolve()
    expect(await c).toBe(true)
  })

  it('tracks each (worker, workspace) pair separately', async () => {
    const prepare = vi.fn().mockResolvedValue(undefined)
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    expect(await ensure('w1', 'ws1')).toBe(true)
    expect(await ensure('w1', 'ws2'), 'same worker, different workspace').toBe(true)
    expect(await ensure('w2', 'ws1'), 'same workspace, different worker').toBe(true)
    expect(await ensure('w1', 'ws1')).toBe(false)
    expect(prepare).toHaveBeenCalledTimes(3)
  })

  /**
   * Guards the encoding: a flat `${workerId}:${workspaceId}` cache key makes
   * these two pairs collide, and the collision reports the SECOND pair as
   * already announced. Its workspace-scoped RPCs would then keep being refused
   * with nothing left that would ever announce it -- the exact dead end this
   * announcer exists to clear.
   */
  it('does not confuse two pairs whose ids share a separator', async () => {
    const prepare = vi.fn().mockResolvedValue(undefined)
    const { ensure } = createWorkspaceAccessAnnouncer(prepare)

    expect(await ensure('a:b', 'c')).toBe(true)
    expect(await ensure('a', 'b:c'), 'a different pair, not the same one').toBe(true)
    expect(prepare).toHaveBeenCalledTimes(2)
    expect(prepare.mock.calls).toEqual([['a:b', 'c'], ['a', 'b:c']])
  })
})
