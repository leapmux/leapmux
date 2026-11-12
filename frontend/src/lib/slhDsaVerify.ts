import type { SlhDsaVerifyRequest } from './slhDsaVerifyWorker'
import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'
import { createWorkerClient } from './workerClient'

/**
 * Verify an SLH-DSA-SHAKE-256f signature, synchronously or not. The hybrid
 * handshake takes one, so that the channel can verify off the main thread while
 * the handshake code stays free of worker plumbing.
 */
export type SlhDsaVerify = (signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array) => boolean | Promise<boolean>

/** Verify on the calling thread: about 26 ms of JavaScript on a fast desktop. */
export function verifySlhDsaHere(signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array): boolean {
  return slh_dsa_shake_256f.verify(signature, message, publicKey)
}

// `undefined` is the client's failure value: the worker could not start, or it
// crashed before it answered. The verification then runs on this thread, so a
// broken worker costs time, never a verdict.
const verifyClient = createWorkerClient<SlhDsaVerifyRequest, boolean | undefined>({
  spawn: () => new Worker(new URL('./slhDsaVerifyWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: { id: number, valid: boolean }) => ({ id: data.id, value: data.valid }),
  failureValue: undefined,
})

/**
 * Start the verification worker ahead of its first request, so that it loads
 * while the handshake waits for the network.
 */
export function prepareSlhDsaVerifyWorker(): void {
  if (typeof Worker !== 'undefined')
    verifyClient.prepare()
}

/**
 * Verify in the verification worker, or on this thread where no Worker exists
 * (unit tests) or the worker fails.
 */
export async function verifySlhDsaInWorker(signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array): Promise<boolean> {
  if (typeof Worker === 'undefined')
    return verifySlhDsaHere(signature, message, publicKey)
  const valid = await verifyClient.request(id => ({ id, signature, message, publicKey }))
  return valid ?? verifySlhDsaHere(signature, message, publicKey)
}
