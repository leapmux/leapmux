import type { DeepseekHarnessGoalOwner } from './goalCleanup'
import { describe, expect, it, vi } from 'vitest'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from '../helpers/cleanup'
import { cleanupDeepseekHarnessGoal } from './goalCleanup'

describe('cleanupDeepseekHarnessGoal', () => {
  it('clears the captured root before a failed operation releases its model reply', async () => {
    const owner = { workerId: 'owned-worker', agentId: 'owned-root' }
    const calls: string[] = []
    const clearGoal = vi.fn(async (actualOwner: DeepseekHarnessGoalOwner) => {
      expect(actualOwner).toEqual(owner)
      calls.push('clear-owned-root')
    })
    const closeAgent = vi.fn(async () => {
      calls.push('close-owned-root')
    })
    const releaseReplies = vi.fn(async () => {
      calls.push('release-replies')
    })
    const failure = new Error('The goal view assertion failed.')
    await expect(withCleanup(async () => {
      throw failure
    }, () => cleanupDeepseekHarnessGoal(owner, { clearGoal, closeAgent, releaseReplies }))).rejects.toBe(failure)
    expect(calls).toEqual(['clear-owned-root', 'release-replies'])
    expect(closeAgent).not.toHaveBeenCalled()
  })

  it('holds every model reply until the owned goal clear completes', async () => {
    const owner = { workerId: 'owned-worker', agentId: 'owned-root' }
    const entered = deferred<'clear-entered'>()
    const clear = deferred<void>()
    const released = vi.fn(async () => {})
    const finished = cleanupDeepseekHarnessGoal(owner, {
      clearGoal: async () => {
        entered.resolve('clear-entered')
        await clear.promise
      },
      closeAgent: async () => { throw new Error('The successful clear must not close the agent.') },
      releaseReplies: released,
    }).then(() => 'cleanup-returned' as const)
    await withCleanup(async () => {
      await expect(Promise.race([entered.promise, finished])).resolves.toBe('clear-entered')
      expect(released).not.toHaveBeenCalled()
      clear.resolve()
      await expect(finished).resolves.toBe('cleanup-returned')
      expect(released).toHaveBeenCalledOnce()
    }, async () => {
      clear.resolve()
      await finished
    })
  })

  it('closes the exact owned root after a goal clear failure and still releases replies', async () => {
    const owner = { workerId: 'owned-worker', agentId: 'owned-root' }
    const failure = new Error('The native goal clear failed.')
    const calls: string[] = []
    await expect(cleanupDeepseekHarnessGoal(owner, {
      clearGoal: async () => {
        calls.push('clear')
        throw failure
      },
      closeAgent: async (actual) => {
        expect(actual).toEqual(owner)
        calls.push('close-owned-root')
      },
      releaseReplies: async () => {
        calls.push('release')
      },
    })).rejects.toBe(failure)
    expect(calls).toEqual(['clear', 'close-owned-root', 'release'])
  })

  it('preserves every cleanup failure after all operations finish', async () => {
    const clearError = new Error('The goal clear failed.')
    const closeError = new Error('The owned root close failed.')
    const releaseError = new Error('The held model release failed.')
    const release = vi.fn(async () => {
      throw releaseError
    })
    const cleanup = cleanupDeepseekHarnessGoal({ workerId: 'owned-worker', agentId: 'owned-root' }, {
      clearGoal: async () => {
        throw clearError
      },
      closeAgent: async () => {
        throw closeError
      },
      releaseReplies: release,
    })
    await expect(cleanup).rejects.toMatchObject({ errors: [clearError, closeError, releaseError] })
    expect(release).toHaveBeenCalledOnce()
  })

  it.each([{ agentId: '', workerId: 'owned-worker' }, { agentId: 'owned-root', workerId: '' }])('refuses an absent owner while releasing all held replies: %j', async (owner) => {
    const clearGoal = vi.fn(async () => {})
    const closeAgent = vi.fn(async () => {})
    const releaseReplies = vi.fn(async () => {})
    await expect(cleanupDeepseekHarnessGoal(owner, { clearGoal, closeAgent, releaseReplies })).rejects.toThrow('captured root and Worker identities')
    expect(clearGoal).not.toHaveBeenCalled()
    expect(closeAgent).not.toHaveBeenCalled()
    expect(releaseReplies).toHaveBeenCalledOnce()
  })

  it('preserves the original operation failure with a cleanup failure', async () => {
    const operationError = new Error('The original view failed.')
    const releaseError = new Error('The model release failed.')
    await expect(withCleanup(async () => {
      throw operationError
    }, () => cleanupDeepseekHarnessGoal({ workerId: 'owned-worker', agentId: 'owned-root' }, {
      clearGoal: async () => {},
      closeAgent: async () => {},
      releaseReplies: async () => {
        throw releaseError
      },
    }))).rejects.toMatchObject({ errors: [operationError, releaseError] })
  })
})
