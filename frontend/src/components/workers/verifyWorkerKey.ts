import type { TunnelKeyApproval } from '~/api/workerRpc'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { refreshWorkerTrustForTunnel } from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { KeyPinRejectedError } from '~/lib/keyPinStore'

/** Prove the current Worker announcement after explicit review and return the exact approval. */
export async function verifyWorkerKey(workerId: string, scope: WorkerTrustScope, signal?: AbortSignal): Promise<TunnelKeyApproval> {
  try {
    return await refreshWorkerTrustForTunnel(workerId, scope, signal)
  }
  catch (err) {
    if (err instanceof KeyPinRejectedError) {
      throw new Error(
        'The changed Worker key was rejected. Confirm the fingerprint the Worker logs when it starts, then verify again.',
        { cause: err },
      )
    }
    throw new Error(`Could not verify the Worker key. ${formatErrorMessage(err, 'The channel did not open.')}`, { cause: err })
  }
}
