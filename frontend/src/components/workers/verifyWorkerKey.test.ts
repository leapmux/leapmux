import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { describe, expect, it, vi } from 'vitest'
import { verifyWorkerKey } from './verifyWorkerKey'

const review = vi.hoisted(() => vi.fn<(workerId: string, scope: WorkerTrustScope) => Promise<TunnelKeyApproval>>())
vi.mock('~/api/workerRpc', () => ({ refreshWorkerTrustForTunnel: review }))

const KeyPinRejectedError = vi.hoisted(() => class KeyPinRejectedError extends Error {})
vi.mock('~/lib/keyPinStore', () => ({ KeyPinRejectedError }))

const scope: WorkerTrustScope = { userId: 'user', generation: 1, signal: new AbortController().signal }

describe('verifyWorkerKey', () => {
  it('returns the approval from the fresh browser review', async () => {
    const approval = {
      workerId: 'w1',
      expectedUserId: 'user',
      identityGeneration: 1,
      publicKeyHex: 'approved',
    }
    review.mockResolvedValueOnce(approval)
    await expect(verifyWorkerKey('w1', scope)).resolves.toBe(approval)
    expect(review).toHaveBeenCalledExactlyOnceWith('w1', scope)
  })

  it('explains how to confirm the fingerprint after a rejection', async () => {
    review.mockRejectedValueOnce(new KeyPinRejectedError())
    await expect(verifyWorkerKey('w1', scope)).rejects.toThrow(/was rejected.*fingerprint/)
  })

  it('reports another verification failure with its reason', async () => {
    review.mockRejectedValueOnce(new Error('Hub unreachable'))
    await expect(verifyWorkerKey('w1', scope)).rejects.toThrow('Could not verify the Worker key. Hub unreachable')
  })

  it('explains a rejection without an error message', async () => {
    review.mockRejectedValueOnce('nope')
    await expect(verifyWorkerKey('w1', scope)).rejects.toThrow('Could not verify the Worker key. The channel did not open.')
  })
})
