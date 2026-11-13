import type { Session } from './noise'
import type { SlhDsaVerify } from './slhDsaVerifyHere'
import { x25519 } from '@noble/curves/ed25519.js'
import { blake2b } from '@noble/hashes/blake2.js'
/**
 * Hybrid post-quantum Noise_NK handshake — initiator (Frontend) side.
 *
 * Extends the classical Noise_NK_25519_ChaChaPoly_BLAKE2b with:
 *   - ML-KEM-1024 (FIPS 203) key encapsulation
 *   - SLH-DSA-SHAKE-256f (FIPS 205) transcript authentication
 *
 * Message 1: noise_msg1 (48 bytes) || mlkem_ciphertext (1568 bytes) = 1616 bytes
 * Message 2: noise_msg2 (48 bytes) || slhdsa_signature (49856 bytes) = 49904 bytes
 */
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'

import { AEAD_TAG_SIZE, SLHDSA_SIG_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { concatBytes } from './bytes'
import { SymmetricState } from './noise'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'
const NOISE_MSG_LEN = X25519_PUBLIC_KEY_SIZE + AEAD_TAG_SIZE // 48 bytes

export interface HybridHandshakeState {
  ss: SymmetricState
  e: { privateKey: Uint8Array, publicKey: Uint8Array }
  rs: Uint8Array
  mlkemSS: Uint8Array
  mlkemCT: Uint8Array
}

/**
 * initiatorHandshake1 creates the first hybrid handshake message.
 *
 * @param remoteX25519Pub - Worker's X25519 static public key (32 bytes)
 * @param remoteMlkemPub - Worker's ML-KEM-1024 encapsulation key (1568 bytes)
 * @returns Handshake state and message1 (1616 bytes)
 */
export function initiatorHandshake1(
  remoteX25519Pub: Uint8Array,
  remoteMlkemPub: Uint8Array,
): {
  handshakeState: HybridHandshakeState
  message1: Uint8Array
} {
  const ss = new SymmetricState(PROTOCOL_NAME)

  // Mix empty prologue.
  ss.mixHash(new Uint8Array(0))

  // Pre-message pattern: <- s
  ss.mixHash(remoteX25519Pub)

  // -> e, es
  const ePriv = x25519.utils.randomSecretKey()
  const ePub = x25519.getPublicKey(ePriv)
  ss.mixHash(ePub)

  // es: DH(e, rs)
  const dhES = x25519.getSharedSecret(ePriv, remoteX25519Pub)
  ss.mixKey(dhES)
  dhES.fill(0)

  // Encrypt empty payload.
  const encPayload = ss.encryptAndHash(new Uint8Array(0))
  const noiseMsg1 = concatBytes(ePub, encPayload)

  // ML-KEM-1024 encapsulation.
  const { cipherText: mlkemCT, sharedSecret: mlkemSS } = ml_kem1024.encapsulate(remoteMlkemPub)

  // Bind ML-KEM ciphertext into the handshake hash (matches responder's mixHash).
  ss.mixHash(mlkemCT)

  return {
    handshakeState: {
      ss,
      e: { privateKey: ePriv, publicKey: ePub },
      rs: remoteX25519Pub,
      mlkemSS,
      mlkemCT,
    },
    message1: concatBytes(noiseMsg1, mlkemCT),
  }
}

/**
 * Zero all sensitive fields in a HybridHandshakeState.
 * Only secrets are zeroed — public keys (rs, e.publicKey) are not sensitive.
 */
export function clearHandshakeState(state: HybridHandshakeState): void {
  state.ss.clear()
  state.e.privateKey.fill(0)
  state.mlkemSS.fill(0)
  state.mlkemCT.fill(0)
}

/**
 * Generate a fresh X25519 ephemeral keypair for an in-band rekey. Shared by the
 * rekey path (and mirroring the handshake's ephemeral generation). The caller
 * must zero the private key when done.
 */
export function generateRekeyEphemeral(): { privateKey: Uint8Array, publicKey: Uint8Array } {
  const privateKey = x25519.utils.randomSecretKey()
  const publicKey = x25519.getPublicKey(privateKey)
  return { privateKey, publicKey }
}

/**
 * Encapsulate a fresh ML-KEM-1024 shared secret under the worker's static
 * encapsulation key, for an in-band rekey. Empty mlkemPub returns null/null so a
 * classic channel skips PQ entropy uniformly. The caller must zero sharedSecret
 * once both directions have rotated.
 */
export function encapsulateRekeyPQ(mlkemPub: Uint8Array): { sharedSecret: Uint8Array | null, cipherText: Uint8Array | null } {
  if (mlkemPub.length === 0) {
    return { sharedSecret: null, cipherText: null }
  }
  const { cipherText, sharedSecret } = ml_kem1024.encapsulate(mlkemPub)
  return { sharedSecret, cipherText }
}

/**
 * Derive the (dhSecret, pqSecret) pair both peers feed into
 * CipherState.rekeyWithSecret. dhSecret is the X25519 ECDH of the two fresh
 * ephemerals; pqSecret is the ML-KEM shared secret (null in classic mode). The
 * caller must zero the returned dhSecret once both directions have rotated.
 */
export function deriveRekeySecrets(
  localEphemeralPriv: Uint8Array,
  peerEphemeralPub: Uint8Array,
  pqSharedSecret: Uint8Array | null,
): { dhSecret: Uint8Array, pqSecret: Uint8Array | null } {
  // An X25519 private key has the size of a public key.
  if (localEphemeralPriv.length !== X25519_PUBLIC_KEY_SIZE) {
    throw new Error(`noise: rekey ephemeral private key wrong size (${localEphemeralPriv.length} !== ${X25519_PUBLIC_KEY_SIZE})`)
  }
  if (peerEphemeralPub.length !== X25519_PUBLIC_KEY_SIZE) {
    throw new Error(`noise: rekey peer ephemeral public key wrong size (${peerEphemeralPub.length} !== ${X25519_PUBLIC_KEY_SIZE})`)
  }
  const dhSecret = x25519.getSharedSecret(localEphemeralPriv, peerEphemeralPub)
  return { dhSecret, pqSecret: pqSharedSecret }
}

/**
 * initiatorHandshake2 completes the hybrid handshake by processing
 * the responder's message2 and verifying the SLH-DSA transcript signature.
 *
 * @param state - Handshake state from initiatorHandshake1
 * @param message2 - Responder's message (49904 bytes)
 * @param remoteSlhdsaPub - Worker's SLH-DSA-SHAKE-256f public key (64 bytes)
 * @param verify - Verifies the signature. The verification costs about 26 ms of
 *   JavaScript, so the channel runs the whole handshake in the channel session
 *   worker, and passes verifySlhDsaHere there. It has no default, so that no
 *   caller verifies on the main thread by omission.
 * @returns Established encrypted Session
 */
export async function initiatorHandshake2(
  state: HybridHandshakeState,
  message2: Uint8Array,
  remoteSlhdsaPub: Uint8Array,
  verify: SlhDsaVerify,
): Promise<Session> {
  const { ss, e, mlkemSS, mlkemCT } = state

  // The handshake state is single-use, and every way out zeroes it: the success,
  // and each failure of a malformed or forged message 2. The state stays intact
  // while the verification runs, because the split after it reads the chaining
  // key and the ML-KEM shared secret.
  try {
    const expectedLen = NOISE_MSG_LEN + SLHDSA_SIG_SIZE
    if (message2.length !== expectedLen) {
      throw new Error(`noise-hybrid: message2 wrong size (${message2.length} !== ${expectedLen})`)
    }

    const noiseMsg2 = message2.slice(0, NOISE_MSG_LEN)
    const slhdsaSig = message2.slice(NOISE_MSG_LEN)

    // <- e, ee
    const re = noiseMsg2.slice(0, X25519_PUBLIC_KEY_SIZE)
    ss.mixHash(re)

    // ee: DH(e, re)
    const dhEE = x25519.getSharedSecret(e.privateKey, re)
    ss.mixKey(dhEE)
    dhEE.fill(0)

    // Decrypt payload (should be empty).
    const encPayload = noiseMsg2.slice(X25519_PUBLIC_KEY_SIZE)
    ss.decryptAndHash(encPayload)

    // Compute transcript: BLAKE2b(handshake_hash || mlkem_ct || mlkem_ss)
    const transcript = blake2b(concatBytes(ss.h, mlkemCT, mlkemSS))

    // Verify SLH-DSA signature.
    if (!await verify(slhdsaSig, transcript, remoteSlhdsaPub))
      throw new Error('noise-hybrid: SLH-DSA signature verification failed')

    // Hybrid split: mix ML-KEM shared secret into chaining key before deriving cipher keys.
    const [c1, c2] = ss.hybridSplit(mlkemSS)

    // Initiator convention: send=c2, receive=c1
    return { send: c2, receive: c1 }
  }
  finally {
    clearHandshakeState(state)
  }
}
