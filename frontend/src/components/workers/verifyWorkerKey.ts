import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { establishWorkerTrustForTunnel, refreshWorkerTrustForTunnel } from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { KeyPinMissingReviewError, KeyPinPresentFirstContactError, KeyPinRejectedError } from '~/lib/keyPinStore'

export class WorkerKeyPinMissingError extends Error {
  constructor(options?: ErrorOptions) {
    super('The Worker has no stored key. Start a deliberate first-contact review.', options)
    this.name = 'WorkerKeyPinMissingError'
  }
}

export class WorkerKeyPinPresentError extends Error {
  constructor(options?: ErrorOptions) {
    super('A Worker key was recorded. Verify the stored key instead.', options)
    this.name = 'WorkerKeyPinPresentError'
  }
}

/** Prove the current Worker announcement after explicit review and return the exact approval. */
export async function verifyWorkerKey(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal): Promise<TunnelKeyApproval> {
  try {
    return await refreshWorkerTrustForTunnel(workerId, scope, signal)
  }
  catch (err) {
    if (err instanceof KeyPinMissingReviewError)
      throw new WorkerKeyPinMissingError({ cause: err })
    if (err instanceof KeyPinRejectedError) {
      throw new Error(
        'The changed Worker key was rejected. Confirm the fingerprint the Worker logs when it starts, then verify again.',
        { cause: err },
      )
    }
    throw new Error(`Could not verify the Worker key. ${formatErrorMessage(err, 'The channel did not open.')}`, { cause: err })
  }
}

export async function trustWorkerOnFirstContact(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal): Promise<TunnelKeyApproval> {
  try {
    return await establishWorkerTrustForTunnel(workerId, scope, signal)
  }
  catch (err) {
    if (err instanceof KeyPinPresentFirstContactError)
      throw new WorkerKeyPinPresentError({ cause: err })
    throw new Error(`Could not establish first-contact trust. ${formatErrorMessage(err, 'The channel did not open.')}`, { cause: err })
  }
}
