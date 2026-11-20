import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { describe, expect, it, vi } from 'vitest'
import { trustWorkerOnFirstContact, verifyWorkerKey, WorkerKeyPinMissingError, WorkerKeyPinPresentError } from './verifyWorkerKey'

const review = vi.hoisted(() => vi.fn<(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal) => Promise<TunnelKeyApproval>>())
const firstContact = vi.hoisted(() => vi.fn<(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal) => Promise<TunnelKeyApproval>>())
vi.mock('~/api/workerRpc', () => ({
  establishWorkerTrustForTunnel: firstContact,
  refreshWorkerTrustForTunnel: review,
}))

const KeyPinRejectedError = vi.hoisted(() => class KeyPinRejectedError extends Error {})
const KeyPinMissingReviewError = vi.hoisted(() => class KeyPinMissingReviewError extends Error {})
const KeyPinPresentFirstContactError = vi.hoisted(() => class KeyPinPresentFirstContactError extends Error {})
vi.mock('~/lib/keyPinStore', () => ({ KeyPinMissingReviewError, KeyPinPresentFirstContactError, KeyPinRejectedError }))

const scope: WorkerTrustScope = { userId: 'user', generation: 1, signal: new AbortController().signal }

describe('verifyWorkerKey', () => {
  it('returns the approval from the fresh browser review', async () => {
    const approval = {
      workerId: 'w1',
      expectedUserId: 'user',
      identityGeneration: 1,
      publicKeyHex: 'approved',
      pinSnapshot: {
        pin: { publicKeyHex: 'approved', firstSeen: 1 },
        removalRevision: 0,
      },
    }
    review.mockResolvedValueOnce(approval)
    await expect(verifyWorkerKey('w1', scope)).resolves.toBe(approval)
    expect(review).toHaveBeenCalledExactlyOnceWith('w1', scope, undefined)
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

  it('reports a missing review pin with a typed recovery error', async () => {
    review.mockRejectedValueOnce(new KeyPinMissingReviewError())
    await expect(verifyWorkerKey('w1', scope)).rejects.toBeInstanceOf(WorkerKeyPinMissingError)
  })

  it('returns the approval from deliberate first contact', async () => {
    const approval = {
      workerId: 'w1',
      expectedUserId: 'user',
      identityGeneration: 1,
      publicKeyHex: 'approved',
      pinSnapshot: {
        pin: { publicKeyHex: 'approved', firstSeen: 1 },
        removalRevision: 0,
      },
    }
    firstContact.mockResolvedValueOnce(approval)
    await expect(trustWorkerOnFirstContact('w1', scope)).resolves.toBe(approval)
  })

  it('reports a concurrent stored pin with a typed review error', async () => {
    firstContact.mockRejectedValueOnce(new KeyPinPresentFirstContactError())
    await expect(trustWorkerOnFirstContact('w1', scope)).rejects.toBeInstanceOf(WorkerKeyPinPresentError)
  })
})
