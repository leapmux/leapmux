import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
/**
 * Noise_NK_25519_ChaChaPoly_BLAKE2b implementation for E2EE channels.
 *
 * This implements the initiator (Frontend) side of the Noise_NK handshake
 * pattern, compatible with the Go `flynn/noise` responder (Worker).
 *
 * Pattern:
 *   <- s       (Responder's static key is known to Initiator)
 *   ...
 *   -> e, es   (Initiator sends ephemeral, DH with Responder's static)
 *   <- e, ee   (Responder sends ephemeral, DH with Initiator's ephemeral)
 */
import { x25519 } from '@noble/curves/ed25519.js'
import { blake2b } from '@noble/hashes/blake2.js'
import { hmac } from '@noble/hashes/hmac.js'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'

/** Soft nonce limit — triggers re-handshake. */
const SOFT_NONCE_LIMIT = 2 ** 31 - 1
/** Hard nonce limit — refuses to operate. */
const HARD_NONCE_LIMIT = 2 ** 32 - 1
/** Noise spec transport message size limit minus auth tag. */
const MAX_PLAINTEXT_SIZE = 65535 - 16

// ---- Low-level crypto primitives ----

function dh(privateKey: Uint8Array, publicKey: Uint8Array): Uint8Array {
  return x25519.getSharedSecret(privateKey, publicKey)
}

function generateKeypair(): { privateKey: Uint8Array, publicKey: Uint8Array } {
  const privateKey = x25519.utils.randomSecretKey()
  const publicKey = x25519.getPublicKey(privateKey)
  return { privateKey, publicKey }
}

function hkdf(
  chainingKey: Uint8Array,
  inputKeyMaterial: Uint8Array,
): [Uint8Array, Uint8Array] {
  const tempKey = hmac(blake2b, chainingKey, inputKeyMaterial)
  const out1 = hmac(blake2b, tempKey, new Uint8Array([1]))
  const out2 = hmac(blake2b, tempKey, concatBytes(out1, new Uint8Array([2])))
  return [out1, out2]
}

function encrypt(key: Uint8Array, nonce: number, ad: Uint8Array, plaintext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true) // little-endian at offset 4
  const cipher = chacha20poly1305(key, nonceBytes, ad)
  return cipher.encrypt(plaintext)
}

function decrypt(key: Uint8Array, nonce: number, ad: Uint8Array, ciphertext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true)
  const cipher = chacha20poly1305(key, nonceBytes, ad)
  return cipher.decrypt(ciphertext)
}

function concatBytes(...arrays: Uint8Array[]): Uint8Array {
  let totalLen = 0
  for (const a of arrays) totalLen += a.length
  const result = new Uint8Array(totalLen)
  let offset = 0
  for (const a of arrays) {
    result.set(a, offset)
    offset += a.length
  }
  return result
}

// ---- Noise Protocol State Machines ----

/** SymmetricState manages the handshake hash (h) and chaining key (ck). */
class SymmetricState {
  h: Uint8Array // handshake hash (64 bytes for BLAKE2b)
  ck: Uint8Array // chaining key (64 bytes for BLAKE2b)
  hasK: boolean
  k: Uint8Array // cipher key (32 bytes for ChaChaPoly)
  n: number // nonce counter

  constructor(protocolName: string) {
    const nameBytes = new TextEncoder().encode(protocolName)
    if (nameBytes.length <= 64) {
      const padded = new Uint8Array(64)
      padded.set(nameBytes)
      this.h = padded
    }
    else {
      this.h = blake2b(nameBytes)
    }
    this.ck = new Uint8Array(this.h)
    this.hasK = false
    this.k = new Uint8Array(32)
    this.n = 0
  }

  mixHash(data: Uint8Array) {
    this.h = blake2b(concatBytes(this.h, data))
  }

  mixKey(inputKeyMaterial: Uint8Array) {
    const [ck, tempK] = hkdf(this.ck, inputKeyMaterial)
    this.ck = ck // 64 bytes
    this.k = tempK.slice(0, 32) // truncate to 32 for ChaChaPoly
    this.n = 0
    this.hasK = true
  }

  encryptAndHash(plaintext: Uint8Array): Uint8Array {
    if (!this.hasK) {
      this.mixHash(plaintext)
      return plaintext
    }
    const ciphertext = encrypt(this.k, this.n, this.h, plaintext)
    this.mixHash(ciphertext)
    this.n++
    return ciphertext
  }

  decryptAndHash(ciphertext: Uint8Array): Uint8Array {
    if (!this.hasK) {
      this.mixHash(ciphertext)
      return ciphertext
    }
    const plaintext = decrypt(this.k, this.n, this.h, ciphertext)
    this.mixHash(ciphertext)
    this.n++
    return plaintext
  }

  split(): [CipherState, CipherState] {
    const [tempK1, tempK2] = hkdf(this.ck, new Uint8Array(0))
    return [
      new CipherState(tempK1.slice(0, 32)), // truncate to 32 for ChaChaPoly
      new CipherState(tempK2.slice(0, 32)),
    ]
  }
}

/** CipherState for post-handshake encryption/decryption. */
export class CipherState {
  private k: Uint8Array
  private n: number

  constructor(key: Uint8Array) {
    this.k = key
    this.n = 0
  }

  encrypt(plaintext: Uint8Array): Uint8Array {
    if (plaintext.length > MAX_PLAINTEXT_SIZE) {
      throw new Error(`noise: plaintext too large (${plaintext.length} > ${MAX_PLAINTEXT_SIZE})`)
    }
    if (this.n > HARD_NONCE_LIMIT) {
      throw new Error('noise: nonce overflow (hard limit)')
    }
    const ct = encrypt(this.k, this.n, new Uint8Array(0), plaintext)
    this.n++
    return ct
  }

  decrypt(ciphertext: Uint8Array): Uint8Array {
    if (this.n > HARD_NONCE_LIMIT) {
      throw new Error('noise: nonce overflow (hard limit)')
    }
    const pt = decrypt(this.k, this.n, new Uint8Array(0), ciphertext)
    this.n++
    return pt
  }

  /** Returns true if the nonce has exceeded the soft limit and re-keying is recommended. */
  needsRekey(): boolean {
    return this.n > SOFT_NONCE_LIMIT
  }
}

/** Session holds the send and receive cipher states after a completed handshake. */
export interface Session {
  send: CipherState
  receive: CipherState
}

/** HandshakeState holds intermediate state between handshake messages. */
export interface HandshakeState {
  ss: SymmetricState
  e: { privateKey: Uint8Array, publicKey: Uint8Array }
  rs: Uint8Array // remote static public key
}

/**
 * initiatorHandshake1 creates the first handshake message for the Noise_NK
 * initiator. The caller must know the responder's static public key.
 *
 * Returns the handshake state (needed for step 2) and the first message.
 */
export function initiatorHandshake1(remoteStaticPubKey: Uint8Array): {
  handshakeState: HandshakeState
  message1: Uint8Array
} {
  const ss = new SymmetricState(PROTOCOL_NAME)

  // Mix empty prologue (required by Noise spec, even when no prologue is used).
  ss.mixHash(new Uint8Array(0))

  // Pre-message pattern: <- s
  // Mix the responder's static public key into the handshake hash.
  ss.mixHash(remoteStaticPubKey)

  // -> e, es
  // Generate ephemeral keypair.
  const e = generateKeypair()

  // Write e (send ephemeral public key as cleartext token).
  ss.mixHash(e.publicKey)

  // es: DH(e, rs)
  const dhResult = dh(e.privateKey, remoteStaticPubKey)
  ss.mixKey(dhResult)

  // Encrypt empty payload (no payload in handshake message 1).
  const encPayload = ss.encryptAndHash(new Uint8Array(0))

  const message1 = concatBytes(e.publicKey, encPayload)

  return {
    handshakeState: { ss, e, rs: remoteStaticPubKey },
    message1,
  }
}

/**
 * initiatorHandshake2 completes the initiator side by processing the
 * responder's handshake response message.
 *
 * Returns the established encrypted Session.
 */
export function initiatorHandshake2(state: HandshakeState, message2: Uint8Array): Session {
  const { ss, e } = state

  // <- e, ee
  // Read responder's ephemeral public key (first 32 bytes).
  if (message2.length < 32) {
    throw new Error('noise: message2 too short')
  }
  const re = message2.slice(0, 32)
  ss.mixHash(re)

  // ee: DH(e, re)
  const dhResult = dh(e.privateKey, re)
  ss.mixKey(dhResult)

  // Decrypt payload (should be empty).
  const payload = message2.slice(32)
  ss.decryptAndHash(payload)

  // Split into send/receive cipher states.
  // The Noise split returns (cs1, cs2). The flynn/noise Go library convention:
  //   Responder: send=cs1, receive=cs2
  //   Initiator: send=cs2, receive=cs1
  const [c1, c2] = ss.split()
  return { send: c2, receive: c1 }
}
