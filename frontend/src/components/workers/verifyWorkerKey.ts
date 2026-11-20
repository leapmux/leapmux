/**
 * Verify a Worker's key through the browser channel path.
 *
 * The browser's E2EE channel is the one path that both fetches the Worker's
 * keys and runs the TOFU pin decision over them: it prompts on a mismatch
 * (the "Worker public key changed" dialog) and records the accepted key in
 * the shared pin store. A tunnel carries that same pin as its expectation, so
 * verifying here is what establishes or repairs the key a create admits
 * against.
 */
import { channelManager } from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { KeyPinRejectedError } from '~/lib/keyPinStore'

/**
 * Open (or reuse) the browser channel to `workerId`, resolving its key pin.
 *
 * Rejects with a user-facing, actionable message — never the raw transport
 * error — so both surfaces can show it inline as it is.
 */
export async function verifyWorkerKey(workerId: string): Promise<void> {
  try {
    await channelManager.getOrOpenChannel(workerId)
  }
  catch (err) {
    if (err instanceof KeyPinRejectedError) {
      throw new Error(
        'The changed Worker key was rejected. Confirm the fingerprint the Worker logs when it starts, then verify again.',
      )
    }
    throw new Error(`Could not verify the Worker key: ${formatErrorMessage(err, 'the channel did not open')}`)
  }
}
