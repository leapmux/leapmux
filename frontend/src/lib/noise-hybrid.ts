import type { Session } from './noise'
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

import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'
import { concatBytes, SymmetricState } from './noise'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'
const DH_LEN = 32
const AEAD_TAG_SIZE = 16
const NOISE_MSG_LEN = DH_LEN + AEAD_TAG_SIZE // 48 bytes
const SLHDSA_SIG_SIZE = 49856

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

  // Encrypt empty payload.
  const encPayload = ss.encryptAndHash(new Uint8Array(0))
  const noiseMsg1 = concatBytes(ePub, encPayload)

  // ML-KEM-1024 encapsulation.
  const { cipherText: mlkemCT, sharedSecret: mlkemSS } = ml_kem1024.encapsulate(remoteMlkemPub)

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
 * initiatorHandshake2 completes the hybrid handshake by processing
 * the responder's message2 and verifying the SLH-DSA transcript signature.
 *
 * @param state - Handshake state from initiatorHandshake1
 * @param message2 - Responder's message (49904 bytes)
 * @param remoteSlhdsaPub - Worker's SLH-DSA-SHAKE-256f public key (64 bytes)
 * @returns Established encrypted Session
 */
export function initiatorHandshake2(
  state: HybridHandshakeState,
  message2: Uint8Array,
  remoteSlhdsaPub: Uint8Array,
): Session {
  const { ss, e, mlkemSS, mlkemCT } = state

  const expectedLen = NOISE_MSG_LEN + SLHDSA_SIG_SIZE
  if (message2.length < expectedLen) {
    throw new Error(`noise-hybrid: message2 too short (${message2.length} < ${expectedLen})`)
  }

  const noiseMsg2 = message2.slice(0, NOISE_MSG_LEN)
  const slhdsaSig = message2.slice(NOISE_MSG_LEN)

  // <- e, ee
  const re = noiseMsg2.slice(0, DH_LEN)
  ss.mixHash(re)

  // ee: DH(e, re)
  const dhEE = x25519.getSharedSecret(e.privateKey, re)
  ss.mixKey(dhEE)

  // Decrypt payload (should be empty).
  const encPayload = noiseMsg2.slice(DH_LEN)
  ss.decryptAndHash(encPayload)

  // Compute transcript: BLAKE2b(handshake_hash || mlkem_ct || mlkem_ss)
  const transcript = blake2b(concatBytes(ss.h, mlkemCT, mlkemSS))

  // Verify SLH-DSA signature.
  const valid = slh_dsa_shake_256f.verify(slhdsaSig, transcript, remoteSlhdsaPub)
  if (!valid) {
    throw new Error('noise-hybrid: SLH-DSA signature verification failed')
  }

  // Hybrid split: mix ML-KEM shared secret into chaining key before deriving cipher keys.
  const [c1, c2] = ss.hybridSplit(mlkemSS)

  // Initiator convention: send=c2, receive=c1
  return { send: c2, receive: c1 }
}
