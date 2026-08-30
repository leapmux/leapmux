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
import { HARD_NONCE_LIMIT, MAX_CHUNK_SIZE, SOFT_NONCE_LIMIT } from '~/generated/contracts/wire'
import { concatBytes } from './bytes'

import { monotonicNow } from './monotonicNow'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'
/**
 * Noise spec transport message size limit minus auth tag -- the same
 * derivation contracts/wire.json single-sources as MAX_CHUNK_SIZE, so
 * retuning the ciphertext cap or the AEAD tag size moves this limit with it.
 */
const MAX_PLAINTEXT_SIZE = MAX_CHUNK_SIZE
/** X25519 ephemeral public key size (bytes), as carried by rekey dh_pub. */
export const DH_LEN = 32

// ---- Low-level crypto primitives ----

function dh(privateKey: Uint8Array, publicKey: Uint8Array): Uint8Array {
  return x25519.getSharedSecret(privateKey, publicKey)
}

function generateKeypair(): { privateKey: Uint8Array, publicKey: Uint8Array } {
  const privateKey = x25519.utils.randomSecretKey()
  const publicKey = x25519.getPublicKey(privateKey)
  return { privateKey, publicKey }
}

export function hkdf(
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

// ---- Noise Protocol State Machines ----

/** SymmetricState manages the handshake hash (h) and chaining key (ck). */
export class SymmetricState {
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

  /**
   * hybridSplit mixes extra key material (e.g. ML-KEM shared secret)
   * into the chaining key before deriving cipher keys.
   */
  hybridSplit(extraKeyMaterial: Uint8Array): [CipherState, CipherState] {
    // Mix extra key material into chaining key.
    const [ck2] = hkdf(this.ck, extraKeyMaterial)
    this.ck = ck2
    return this.split()
  }

  /** Zero all sensitive fields in the symmetric state. */
  clear(): void {
    this.h.fill(0)
    this.ck.fill(0)
    this.k.fill(0)
    this.hasK = false
    this.n = 0
  }
}

/**
 * How long a previous receive key stays live after a rekey (ms). 10s
 * accommodates a slow or contended link where straddling frames may queue well
 * beyond the bare RTT.
 */
const REKEY_GRACE_WINDOW_MS = 10_000

/** CipherState for post-handshake encryption/decryption. */
export class CipherState {
  private k: Uint8Array
  private n: number
  /** Previous-epoch key retained during the rekey grace window (receive only). */
  private prev: CipherState | null = null
  /** Monotonic deadline (ms, monotonicNow timeline) after which `prev` must be dropped; 0 ⇒ none. */
  private prevExpiresAt = 0

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
    try {
      const pt = decrypt(this.k, this.n, new Uint8Array(0), ciphertext)
      this.n++
      return pt
    }
    catch (err) {
      // Current key failed: try the previous-epoch key if it is still live, so
      // a frame the peer encrypted just before the swap still decrypts.
      if (this.prev !== null && this.prevExpiresAt !== 0) {
        if (monotonicNow() < this.prevExpiresAt) {
          // Let the prev attempt throw on its own; if it also fails, surface
          // the CURRENT key's error (the one that actually matters for the
          // active epoch) rather than swallowing it for prev's — mirrors Go's
          // Decrypt, which returns the captured `err` on prev failure.
          try {
            return this.prev.decrypt(ciphertext)
          }
          catch {
            throw err
          }
        }
        else {
          // Grace window elapsed: retire prev now so the still-valid
          // previous-epoch key does not linger in the heap. Delivers the
          // window's forward-secrecy promise here, not on the next rekey /
          // close (an idle channel would otherwise keep prev alive).
          this.prev.k.fill(0)
          this.prev = null
          this.prevExpiresAt = 0
        }
      }
      throw err
    }
  }

  /** Returns true if the nonce has exceeded the soft limit and re-keying is recommended. */
  needsRekey(): boolean {
    return this.n > SOFT_NONCE_LIMIT
  }

  /**
   * Derive a new cipher key that mixes fresh key-agreement entropy into the
   * current key, then swap it in. The previous key is retained for the grace
   * window so in-flight frames encrypted under it still decrypt. The nonce
   * resets to 0.
   *
   * Derivation (mirrors the Noise handshake's mixKey → hybridSplit ratchet):
   *
   *   ck1 = HKDF(k, dhSecret)      // fresh X25519 ECDH(e_i, e_r)
   *   k'  = HKDF(ck1, pqSecret)    // fresh ML-KEM shared secret (empty in classic)
   *
   * When retainPrev is true (the receive direction) the previous key is moved
   * into prev and marked live for the grace window. When false (the send
   * direction, which keeps no grace window) the replaced key is zeroed and
   * dropped instead — structurally, not via a follow-up clearPrev the caller
   * must remember.
   *
   * Forward secrecy: because k' mixes fresh DH + ML-KEM entropy, compromise of
   * the current epoch key does NOT let an attacker derive the next. The rekey
   * frames ride inside the AEAD-encrypted channel, so the ephemeral pubkeys /
   * ML-KEM ciphertext need no signature. See issue #321.
   *
   * Resetting n diverges from Noise §11.3 because LeapMux's transport AEAD
   * nonce is a uint32 in bytes 4–7. Matching the Go implementation.
   */
  rekeyWithSecret(dhSecret: Uint8Array, pqSecret: Uint8Array | null, retainPrev: boolean): void {
    const [ck1] = hkdf(this.k, dhSecret)
    // pqSecret is null on a classical channel; hkdf still runs a second
    // (degenerate, IKM-less) stage keyed only off ck1 — NOT skipped — so the
    // derivation stays a uniform two-stage ratchet on both channel kinds and
    // matches Go's rekeyWithSecret. pqSecret ?? empty is equivalent to null
    // here because hmac of empty bytes == hmac of nothing.
    const [newK] = hkdf(ck1, pqSecret ?? new Uint8Array(0))
    if (this.prev !== null) {
      this.prev.k.fill(0)
      this.prev = null
    }
    if (retainPrev) {
      // Receive direction: promote the current key to prev for the grace window.
      this.prev = new CipherState(this.k)
      this.prev.n = this.n
      this.prevExpiresAt = monotonicNow() + REKEY_GRACE_WINDOW_MS
    }
    else {
      // Send direction: it keeps no grace window, so the replaced key is zeroed
      // rather than retained. Doing this here (instead of promoting to prev and
      // relying on the caller to clearPrev) makes "send never holds a previous
      // key" a structural property — a caller cannot forget the clearPrev.
      this.k.fill(0)
    }
    this.k = newK.slice(0, 32)
    this.n = 0
    ck1.fill(0)
    dhSecret.fill(0)
    if (pqSecret !== null)
      pqSecret.fill(0)
  }

  /** Drop the retained previous key immediately (send direction; tests). */
  clearPrev(): void {
    if (this.prev !== null) {
      this.prev.k.fill(0)
      this.prev = null
    }
    this.prevExpiresAt = 0
  }

  /** Current nonce (for tests and rekey policy). */
  nonce(): number {
    return this.n
  }
}

/**
 * CipherStateLike is the public method surface of CipherState. Session and its
 * consumers reference this structural shape (not the CipherState class) so a
 * test double can `satisfies Session` without faking the class's private key
 * fields — and a future method addition to CipherState that the mock forgets
 * fails to compile here instead of silently diverging. CipherState the class
 * satisfies this interface by construction.
 */
export interface CipherStateLike {
  encrypt: (plaintext: Uint8Array) => Uint8Array
  decrypt: (ciphertext: Uint8Array) => Uint8Array
  needsRekey: () => boolean
  rekeyWithSecret: (dhSecret: Uint8Array, pqSecret: Uint8Array | null, retainPrev: boolean) => void
  clearPrev: () => void
  nonce: () => number
}

/** Session holds the send and receive cipher states after a completed handshake. */
export interface Session {
  send: CipherStateLike
  receive: CipherStateLike
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
  dhResult.fill(0)

  // Encrypt empty payload (no payload in handshake message 1).
  const encPayload = ss.encryptAndHash(new Uint8Array(0))

  const message1 = concatBytes(e.publicKey, encPayload)

  return {
    handshakeState: { ss, e, rs: remoteStaticPubKey },
    message1,
  }
}

/**
 * Zero all sensitive fields in a classical HandshakeState.
 * Only secrets are zeroed — public keys (rs, e.publicKey) are not sensitive.
 */
export function clearClassicalHandshakeState(state: HandshakeState): void {
  state.ss.clear()
  state.e.privateKey.fill(0)
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
  dhResult.fill(0)

  // Decrypt payload (should be empty).
  const payload = message2.slice(32)
  ss.decryptAndHash(payload)

  // Split into send/receive cipher states.
  // The Noise split returns (cs1, cs2). The flynn/noise Go library convention:
  //   Responder: send=cs1, receive=cs2
  //   Initiator: send=cs2, receive=cs1
  const [c1, c2] = ss.split()

  // Zero sensitive handshake material.
  clearClassicalHandshakeState(state)

  return { send: c2, receive: c1 }
}
