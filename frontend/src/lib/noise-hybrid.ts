import type { NoiseEntropy, Session } from './noise'
import type { SlhDsaVerify } from './slhDsaVerifyHere'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { x25519 } from '@noble/curves/ed25519.js'
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'
import {
  AEAD_TAG_SIZE,
  MLKEM_CIPHERTEXT_SIZE,
  NOISE_HANDSHAKE_SIGNATURE_LABEL,
  NOISE_POST_QUANTUM_PROTOCOL_NAME,
  SLHDSA_SIG_SIZE,
  X25519_PUBLIC_KEY_SIZE,
} from '~/generated/contracts/wire'
import { concatBytes } from './bytes'
import { handshakePrologue, mixStaticKeys, SymmetricState, systemEntropy } from './noise'

const EMPTY = new Uint8Array(0)
const MESSAGE_2_SIZE = 1632 + SLHDSA_SIG_SIZE
const MESSAGE_2_PAYLOAD_OFFSET = X25519_PUBLIC_KEY_SIZE + MLKEM_CIPHERTEXT_SIZE + AEAD_TAG_SIZE
const MESSAGE_2_SIGNATURE_OFFSET = MESSAGE_2_PAYLOAD_OFFSET + AEAD_TAG_SIZE

export interface HybridHandshakeState {
  ss: SymmetricState
  e: { privateKey: Uint8Array, publicKey: Uint8Array }
  mlkemEphemeralSecretKey: Uint8Array
  slhdsaPublicKey: Uint8Array
}

function generateMlkemKey(entropy: NoiseEntropy): { publicKey: Uint8Array, secretKey: Uint8Array } {
  const seed = entropy.mlkemKeygenSeed()
  try {
    return ml_kem1024.keygen(seed)
  }
  finally {
    seed.fill(0)
  }
}

function encapsulateMlkem(entropy: NoiseEntropy, publicKey: Uint8Array): { cipherText: Uint8Array, sharedSecret: Uint8Array } {
  const seed = entropy.mlkemEncapsulationSeed()
  try {
    return ml_kem1024.encapsulate(publicKey, seed)
  }
  finally {
    seed.fill(0)
  }
}

/** Build post-quantum message 1 with production randomness. */
export function initiatorHandshake1(workerId: string, keys: WorkerKeyBundle): {
  handshakeState: HybridHandshakeState
  message1: Uint8Array
} {
  return initiatorHandshake1WithEntropy(systemEntropy, workerId, keys)
}

export function initiatorHandshake1WithEntropy(entropy: NoiseEntropy, workerId: string, keys: WorkerKeyBundle): {
  handshakeState: HybridHandshakeState
  message1: Uint8Array
} {
  const ss = new SymmetricState(NOISE_POST_QUANTUM_PROTOCOL_NAME)
  let ePrivate: Uint8Array | null = null
  let ephemeralSecretKey: Uint8Array | null = null
  let staticSharedSecret: Uint8Array | null = null
  try {
    ss.mixHash(handshakePrologue(workerId))
    mixStaticKeys(ss, keys)
    ePrivate = entropy.x25519Seed()
    const e = {
      privateKey: ePrivate,
      publicKey: x25519.getPublicKey(ePrivate),
    }
    ss.mixHash(e.publicKey)
    const dhES = x25519.getSharedSecret(e.privateKey, keys.x25519PublicKey)
    ss.mixKey(dhES)
    dhES.fill(0)

    const ephemeral = generateMlkemKey(entropy)
    ephemeralSecretKey = ephemeral.secretKey
    const encryptedKey = ss.encryptAndHash(ephemeral.publicKey)
    const staticResult = encapsulateMlkem(entropy, keys.mlkemPublicKey)
    staticSharedSecret = staticResult.sharedSecret
    const encryptedStatic = ss.encryptAndHash(staticResult.cipherText)
    ss.mixKey(staticSharedSecret)
    staticSharedSecret.fill(0)
    const payload = ss.encryptAndHash(EMPTY)
    return {
      handshakeState: {
        ss,
        e,
        mlkemEphemeralSecretKey: ephemeralSecretKey,
        slhdsaPublicKey: keys.slhdsaPublicKey.slice(),
      },
      message1: concatBytes(e.publicKey, encryptedKey, encryptedStatic, payload),
    }
  }
  catch (err) {
    ss.clear()
    ePrivate?.fill(0)
    ephemeralSecretKey?.fill(0)
    staticSharedSecret?.fill(0)
    throw err
  }
}

/** Zero every secret of a hybrid handshake state. Idempotent. */
export function clearHandshakeState(state: HybridHandshakeState): void {
  state.ss.clear()
  state.e.privateKey.fill(0)
  state.mlkemEphemeralSecretKey.fill(0)
}

/** Generate a fresh X25519 ephemeral keypair for an in-band rekey. */
export function generateRekeyEphemeral(): { privateKey: Uint8Array, publicKey: Uint8Array } {
  const privateKey = x25519.utils.randomSecretKey()
  return {
    privateKey,
    publicKey: x25519.getPublicKey(privateKey),
  }
}

export interface RekeyPQOffer {
  staticCipherText: Uint8Array
  ephemeralPublicKey: Uint8Array
  staticSharedSecret: Uint8Array
  ephemeralSecretKey: Uint8Array
}

export function offerRekeyPQ(workerMlkemPub: Uint8Array): RekeyPQOffer | null {
  return offerRekeyPQWithEntropy(systemEntropy, workerMlkemPub)
}

export function offerRekeyPQWithEntropy(entropy: NoiseEntropy, workerMlkemPub: Uint8Array): RekeyPQOffer | null {
  if (workerMlkemPub.length === 0)
    return null
  let staticSharedSecret: Uint8Array | null = null
  try {
    const staticResult = encapsulateMlkem(entropy, workerMlkemPub)
    staticSharedSecret = staticResult.sharedSecret
    const ephemeral = generateMlkemKey(entropy)
    return {
      staticCipherText: staticResult.cipherText,
      ephemeralPublicKey: ephemeral.publicKey,
      staticSharedSecret,
      ephemeralSecretKey: ephemeral.secretKey,
    }
  }
  catch (err) {
    staticSharedSecret?.fill(0)
    throw err
  }
}

/** Complete an offer and zero it on every exit. Null is the classic offer. */
export function acceptRekeyPQ(offer: RekeyPQOffer | null, ephemeralCipherText: Uint8Array): Uint8Array | null {
  if (offer === null) {
    if (ephemeralCipherText.length !== 0)
      throw new Error('noise: classic rekey Ack carries an ML-KEM ciphertext')
    return null
  }
  try {
    if (ephemeralCipherText.length !== MLKEM_CIPHERTEXT_SIZE)
      throw new Error(`noise: rekey ephemeral ML-KEM ciphertext is ${ephemeralCipherText.length} bytes, want ${MLKEM_CIPHERTEXT_SIZE}`)
    const ephemeralSharedSecret = ml_kem1024.decapsulate(ephemeralCipherText, offer.ephemeralSecretKey)
    try {
      return concatBytes(offer.staticSharedSecret, ephemeralSharedSecret)
    }
    finally {
      ephemeralSharedSecret.fill(0)
    }
  }
  finally {
    clearRekeyPQOffer(offer)
  }
}

export function clearRekeyPQOffer(offer: RekeyPQOffer | null): void {
  if (offer === null)
    return
  offer.staticCipherText.fill(0)
  offer.ephemeralPublicKey.fill(0)
  offer.staticSharedSecret.fill(0)
  offer.ephemeralSecretKey.fill(0)
}

/** Derive the fresh X25519 and optional 64-byte post-quantum rekey inputs. */
export function deriveRekeySecrets(
  localEphemeralPriv: Uint8Array,
  peerEphemeralPub: Uint8Array,
  pqSharedSecret: Uint8Array | null,
): { dhSecret: Uint8Array, pqSecret: Uint8Array | null } {
  if (localEphemeralPriv.length !== X25519_PUBLIC_KEY_SIZE)
    throw new Error(`noise: rekey ephemeral private key wrong size (${localEphemeralPriv.length} !== ${X25519_PUBLIC_KEY_SIZE})`)
  if (peerEphemeralPub.length !== X25519_PUBLIC_KEY_SIZE)
    throw new Error(`noise: rekey peer ephemeral public key wrong size (${peerEphemeralPub.length} !== ${X25519_PUBLIC_KEY_SIZE})`)
  return {
    dhSecret: x25519.getSharedSecret(localEphemeralPriv, peerEphemeralPub),
    pqSecret: pqSharedSecret,
  }
}

/** Complete the post-quantum handshake and verify its domain-separated hash. */
export async function initiatorHandshake2(state: HybridHandshakeState, message2: Uint8Array, verify: SlhDsaVerify): Promise<Session> {
  const { ss, e } = state
  try {
    if (message2.length !== MESSAGE_2_SIZE)
      throw new Error(`noise-hybrid: message2 wrong size (${message2.length} !== ${MESSAGE_2_SIZE})`)
    const responderEphemeral = message2.slice(0, X25519_PUBLIC_KEY_SIZE)
    ss.mixHash(responderEphemeral)
    const dhEE = x25519.getSharedSecret(e.privateKey, responderEphemeral)
    ss.mixKey(dhEE)
    dhEE.fill(0)

    const ephemeralCiphertext = ss.decryptAndHash(message2.slice(X25519_PUBLIC_KEY_SIZE, MESSAGE_2_PAYLOAD_OFFSET))
    const ephemeralSharedSecret = ml_kem1024.decapsulate(ephemeralCiphertext, state.mlkemEphemeralSecretKey)
    ss.mixKey(ephemeralSharedSecret)
    ephemeralSharedSecret.fill(0)
    ss.decryptAndHash(message2.slice(MESSAGE_2_PAYLOAD_OFFSET, MESSAGE_2_SIGNATURE_OFFSET))

    const signature = message2.slice(MESSAGE_2_SIGNATURE_OFFSET)
    const signatureInput = concatBytes(new TextEncoder().encode(NOISE_HANDSHAKE_SIGNATURE_LABEL), new Uint8Array([0]), ss.h)
    if (!await verify(signature, signatureInput, state.slhdsaPublicKey))
      throw new Error('noise-hybrid: SLH-DSA signature verification failed')
    const [c1, c2] = ss.split()
    return { send: c2, receive: c1 }
  }
  finally {
    clearHandshakeState(state)
  }
}
