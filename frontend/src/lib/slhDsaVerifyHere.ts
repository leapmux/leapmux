import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'

// The verification on the calling thread, in a module of its own, which the
// channel session (channelSession.ts) passes to the hybrid handshake. That code
// runs in the channel session worker, so the verification runs off the main
// thread.

/**
 * Verify an SLH-DSA-SHAKE-256f signature, synchronously or not. The hybrid
 * handshake takes one, so that the handshake code stays free of any choice of
 * thread.
 */
export type SlhDsaVerify = (signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array) => boolean | Promise<boolean>

/**
 * Verify on the calling thread: about 26 ms of JavaScript on a fast desktop.
 *
 * A signature or a public key that the verifier cannot parse is invalid, so this
 * answers `false` and never throws: the handshake then reports it as it reports
 * every other invalid signature.
 */
export function verifySlhDsaHere(signature: Uint8Array, message: Uint8Array, publicKey: Uint8Array): boolean {
  try {
    return slh_dsa_shake_256f.verify(signature, message, publicKey)
  }
  catch {
    return false
  }
}
