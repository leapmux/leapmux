import type { WorkerPublicKeys } from '~/generated/proto/leapmux/v1/channel_pb'
import { MLKEM_PUBLIC_KEY_SIZE, SLHDSA_PUBLIC_KEY_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { ChannelError } from './channelError'

/**
 * Worker Noise public-key material shared by transport open, TOFU pin resolve,
 * and ChannelSession handshake. One type so the three seams cannot drift.
 */
export interface WorkerKeyBundle {
  x25519PublicKey: Uint8Array
  mlkemPublicKey: Uint8Array
  slhdsaPublicKey: Uint8Array
}

/**
 * The key bundle of the worker public keys that the hub reported, or a
 * ChannelError when the hub sent none or a key of the wrong size.
 *
 * The check runs before any pin decision. An absent key set used to become
 * three empty keys: a pinned worker then showed a "key differs" prompt with a
 * zero-byte fingerprint, and accepting it started a handshake with an empty
 * X25519 key. Every encryption mode takes all three keys, because the hub
 * refuses a worker hello without them and the pin covers all three.
 */
export function workerKeyBundleFromWire(workerId: string, keys: WorkerPublicKeys | undefined): WorkerKeyBundle {
  if (!keys)
    throw malformedReply(`the hub sent no public keys for worker ${workerId}`)
  return {
    x25519PublicKey: requireKeySize(workerId, 'X25519', keys.x25519, X25519_PUBLIC_KEY_SIZE),
    mlkemPublicKey: requireKeySize(workerId, 'ML-KEM', keys.mlkem, MLKEM_PUBLIC_KEY_SIZE),
    slhdsaPublicKey: requireKeySize(workerId, 'SLH-DSA', keys.slhdsa, SLHDSA_PUBLIC_KEY_SIZE),
  }
}

function requireKeySize(workerId: string, algorithm: string, key: Uint8Array, size: number): Uint8Array {
  if (key.length !== size)
    throw malformedReply(`the ${algorithm} public key that the hub sent for worker ${workerId} is ${key.length} bytes, not ${size}`)
  return key
}

/** A malformed hub reply is a transport fault, but not a dropped link. */
function malformedReply(detail: string): ChannelError {
  return new ChannelError('transport', `open channel: ${detail}`, { disconnected: false })
}
