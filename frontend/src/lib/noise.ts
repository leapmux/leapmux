import type { WorkerKeyBundle } from './workerKeyBundle'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
/**
 * Classic channel Noise implementation for the Frontend initiator.
 *
 * The handshake combines ephemeral X25519 exchanges with an encapsulation to
 * the Worker's static ML-KEM-1024 key. It has no SLH-DSA signature and no
 * post-quantum forward secrecy.
 */
import { x25519 } from '@noble/curves/ed25519.js'
import { blake2b } from '@noble/hashes/blake2.js'
import { hmac } from '@noble/hashes/hmac.js'
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'
import { randomBytes } from '@noble/post-quantum/utils.js'
import { AEAD_TAG_SIZE, HARD_NONCE_LIMIT, MAX_CHUNK_SIZE, NOISE_CLASSIC_PROTOCOL_NAME, NOISE_HANDSHAKE_CONTEXT_LABEL, SOFT_NONCE_LIMIT, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { concatBytes } from './bytes'

import { monotonicNow } from './monotonicNow'

/**
 * Noise spec transport message size limit minus auth tag -- the same
 * derivation contracts/wire.json single-sources as MAX_CHUNK_SIZE, so
 * retuning the ciphertext cap or the AEAD tag size moves this limit with it.
 */
const MAX_PLAINTEXT_SIZE = MAX_CHUNK_SIZE
const CLASSIC_MESSAGE_2_SIZE = X25519_PUBLIC_KEY_SIZE + AEAD_TAG_SIZE

// ---- Low-level crypto primitives ----

/** Fresh randomness for one handshake or rekey offer. */
export interface NoiseEntropy {
  x25519Seed: () => Uint8Array
  mlkemKeygenSeed: () => Uint8Array
  mlkemEncapsulationSeed: () => Uint8Array
}

export const systemEntropy: NoiseEntropy = {
  x25519Seed: () => x25519.utils.randomSecretKey(),
  mlkemKeygenSeed: () => randomBytes(64),
  mlkemEncapsulationSeed: () => randomBytes(32),
}

/** The versioned label and Worker id that both peers hash before message 1. */
export function handshakePrologue(workerId: string): Uint8Array {
  if (workerId.length === 0)
    throw new Error('noise: Worker id is required for the channel handshake')
  return concatBytes(new TextEncoder().encode(NOISE_HANDSHAKE_CONTEXT_LABEL), new Uint8Array([0]), new TextEncoder().encode(workerId))
}

/** Hash the Worker's complete pinned key set in the pre-message order. */
export function mixStaticKeys(ss: SymmetricState, keys: WorkerKeyBundle): void {
  ss.mixHash(keys.x25519PublicKey)
  ss.mixHash(keys.mlkemPublicKey)
  ss.mixHash(keys.slhdsaPublicKey)
}

export function hkdf(
  chainingKey: Uint8Array,
  inputKeyMaterial: Uint8Array,
): [Uint8Array, Uint8Array] {
  const tempKey = hmac(blake2b, chainingKey, inputKeyMaterial)
  try {
    const out1 = hmac(blake2b, tempKey, Uint8Array.of(1))
    const out2 = hmac.create(blake2b, tempKey).update(out1).update(Uint8Array.of(2)).digest()
    return [out1, out2]
  }
  finally {
    tempKey.fill(0)
  }
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
    this.ck.set(ck)
    this.k.set(tempK.subarray(0, this.k.length))
    ck.fill(0)
    tempK.fill(0)
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
    const c1 = new CipherState(tempK1.slice(0, this.k.length))
    const c2 = new CipherState(tempK2.slice(0, this.k.length))
    tempK1.fill(0)
    tempK2.fill(0)
    return [c1, c2]
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
  private prevTimer: ReturnType<typeof setTimeout> | undefined
  /** Set by `destroy`: the keys are zero, and every later encrypt or decrypt throws. */
  private destroyed = false

  constructor(key: Uint8Array) {
    this.k = key
    this.n = 0
  }

  encrypt(ad: Uint8Array, plaintext: Uint8Array): Uint8Array {
    if (this.destroyed) {
      throw new Error('noise: cipher state destroyed')
    }
    if (plaintext.length > MAX_PLAINTEXT_SIZE) {
      throw new Error(`noise: plaintext too large (${plaintext.length} > ${MAX_PLAINTEXT_SIZE})`)
    }
    if (this.n > HARD_NONCE_LIMIT) {
      throw new Error('noise: nonce overflow (hard limit)')
    }
    const ct = encrypt(this.k, this.n, ad, plaintext)
    this.n++
    return ct
  }

  decrypt(ad: Uint8Array, ciphertext: Uint8Array): Uint8Array {
    if (this.destroyed) {
      throw new Error('noise: cipher state destroyed')
    }
    if (this.n > HARD_NONCE_LIMIT) {
      throw new Error('noise: nonce overflow (hard limit)')
    }
    try {
      const pt = decrypt(this.k, this.n, ad, ciphertext)
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
            return this.prev.decrypt(ad, ciphertext)
          }
          catch {
            throw err
          }
        }
        else {
          this.clearPrev()
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
   * Derivation (two HKDF stages over the transport key):
   *
   *   ck1 = HKDF(k, dhSecret)      // fresh X25519 ECDH(e_i, e_r)
   *   k'  = HKDF(ck1, pqSecret)    // static || ephemeral ML-KEM secret (null in classic)
   *
   * When retainPrev is true (the receive direction) the previous key is moved
   * into prev and marked live for the grace window. When false (the send
   * direction, which keeps no grace window) the replaced key is zeroed and
   * dropped instead — structurally, not via a follow-up clearPrev the caller
   * must remember.
   *
   * Forward secrecy: because k' mixes fresh DH entropy, and ML-KEM entropy on
   * post-quantum channels, compromise of the current epoch key does NOT let an
   * attacker derive the next. The rekey frames ride inside the AEAD-encrypted
   * channel, so the ephemeral pubkeys / ML-KEM ciphertext need no signature. See
   * issue #321.
   *
   * Resetting n diverges from Noise §11.3 because LeapMux's transport AEAD
   * nonce is a uint32 in bytes 4–7. Matching the Go implementation.
   */
  rekeyWithSecret(dhSecret: Uint8Array, pqSecret: Uint8Array | null, retainPrev: boolean): void {
    if (this.destroyed) {
      // Every exit zeroes the fresh secrets, as the success path below does: a
      // caller hands them over and keeps no copy to clear.
      dhSecret.fill(0)
      pqSecret?.fill(0)
      throw new Error('noise: cipher state destroyed')
    }
    const [ck1, unusedFirst] = hkdf(this.k, dhSecret)
    unusedFirst.fill(0)
    // pqSecret is null on a classical channel; hkdf still runs a second
    // (degenerate, IKM-less) stage keyed only off ck1 — NOT skipped — so the
    // derivation stays a uniform two-stage ratchet on both channel kinds and
    // matches Go's rekeyWithSecret. pqSecret ?? empty is equivalent to null
    // here because hmac of empty bytes == hmac of nothing.
    const [newK, unusedSecond] = hkdf(ck1, pqSecret ?? new Uint8Array(0))
    unusedSecond.fill(0)
    this.clearPrev()
    if (retainPrev) {
      const previous = new CipherState(this.k)
      previous.n = this.n
      this.prev = previous
      this.prevExpiresAt = monotonicNow() + REKEY_GRACE_WINDOW_MS
      this.prevTimer = setTimeout(() => {
        if (this.prev === previous)
          this.clearPrev()
      }, REKEY_GRACE_WINDOW_MS)
    }
    else {
      // Send direction: it keeps no grace window, so the replaced key is zeroed
      // rather than retained. Doing this here (instead of promoting to prev and
      // relying on the caller to clearPrev) makes "send never holds a previous
      // key" a structural property — a caller cannot forget the clearPrev.
      this.k.fill(0)
    }
    this.k = newK.slice(0, this.k.length)
    newK.fill(0)
    this.n = 0
    ck1.fill(0)
    dhSecret.fill(0)
    if (pqSecret !== null)
      pqSecret.fill(0)
  }

  /** Retire the previous receive key and stop its grace timer. */
  clearPrev(): void {
    clearTimeout(this.prevTimer)
    this.prevTimer = undefined
    this.prev?.destroy()
    this.prev = null
    this.prevExpiresAt = 0
  }

  /** Current nonce (for tests and rekey policy). */
  nonce(): number {
    return this.n
  }

  /**
   * Zero the current key and the previous key, and refuse every later encrypt or
   * decrypt. A closed channel calls this, so that its keys do not stay in the
   * heap until the garbage collector reclaims them. Idempotent.
   */
  destroy(): void {
    this.k.fill(0)
    this.clearPrev()
    this.destroyed = true
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
  encrypt: (ad: Uint8Array, plaintext: Uint8Array) => Uint8Array
  decrypt: (ad: Uint8Array, ciphertext: Uint8Array) => Uint8Array
  needsRekey: () => boolean
  rekeyWithSecret: (dhSecret: Uint8Array, pqSecret: Uint8Array | null, retainPrev: boolean) => void
  clearPrev: () => void
  nonce: () => number
  destroy: () => void
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
}

/** Encapsulate a secret and clear the randomness supplied to ML-KEM. */
export function encapsulateMlkem(entropy: NoiseEntropy, publicKey: Uint8Array): { cipherText: Uint8Array, sharedSecret: Uint8Array } {
  const seed = entropy.mlkemEncapsulationSeed()
  try {
    return ml_kem1024.encapsulate(publicKey, seed)
  }
  finally {
    seed.fill(0)
  }
}

export function mixX25519(ss: SymmetricState, privateKey: Uint8Array, peerKey: Uint8Array): void {
  const sharedSecret = x25519.getSharedSecret(privateKey, peerKey)
  try {
    ss.mixKey(sharedSecret)
  }
  finally {
    sharedSecret.fill(0)
  }
}

/** Set up the prologue and first X25519 exchange common to both modes. */
export function prepareInitiatorHandshake(
  entropy: NoiseEntropy,
  workerId: string,
  keys: WorkerKeyBundle,
  protocolName: string,
): HandshakeState {
  const ss = new SymmetricState(protocolName)
  let privateKey: Uint8Array | null = null
  try {
    ss.mixHash(handshakePrologue(workerId))
    mixStaticKeys(ss, keys)
    privateKey = entropy.x25519Seed()
    const publicKey = x25519.getPublicKey(privateKey)
    ss.mixHash(publicKey)
    mixX25519(ss, privateKey, keys.x25519PublicKey)
    return {
      ss,
      e: { privateKey, publicKey },
    }
  }
  catch (err) {
    ss.clear()
    privateKey?.fill(0)
    throw err
  }
}

/** Encrypt the static ML-KEM ciphertext and mix the secret into the transcript. */
export function encryptStaticMlkem(ss: SymmetricState, entropy: NoiseEntropy, publicKey: Uint8Array): Uint8Array {
  const { cipherText, sharedSecret } = encapsulateMlkem(entropy, publicKey)
  try {
    const encryptedCiphertext = ss.encryptAndHash(cipherText)
    ss.mixKey(sharedSecret)
    return encryptedCiphertext
  }
  finally {
    sharedSecret.fill(0)
  }
}

/** Build classic message 1 with production randomness. */
export function initiatorHandshake1(workerId: string, keys: WorkerKeyBundle): {
  handshakeState: HandshakeState
  message1: Uint8Array
} {
  return initiatorHandshake1WithEntropy(systemEntropy, workerId, keys)
}

export function initiatorHandshake1WithEntropy(entropy: NoiseEntropy, workerId: string, keys: WorkerKeyBundle): {
  handshakeState: HandshakeState
  message1: Uint8Array
} {
  const handshakeState = prepareInitiatorHandshake(entropy, workerId, keys, NOISE_CLASSIC_PROTOCOL_NAME)
  const { ss, e } = handshakeState
  try {
    const encryptedCiphertext = encryptStaticMlkem(ss, entropy, keys.mlkemPublicKey)
    const payload = ss.encryptAndHash(new Uint8Array(0))
    return {
      handshakeState,
      message1: concatBytes(e.publicKey, encryptedCiphertext, payload),
    }
  }
  catch (err) {
    clearClassicalHandshakeState(handshakeState)
    throw err
  }
}

/**
 * Zero all sensitive fields in a classical HandshakeState.
 * The public ephemeral key needs no zeroing.
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
  try {
    // A classic message 2 is exactly one X25519 key and one empty-payload tag.
    if (message2.length !== CLASSIC_MESSAGE_2_SIZE) {
      throw new Error(`noise: message2 wrong size (${message2.length} !== ${CLASSIC_MESSAGE_2_SIZE})`)
    }
    const re = message2.slice(0, X25519_PUBLIC_KEY_SIZE)
    ss.mixHash(re)

    // ee: DH(e, re)
    mixX25519(ss, e.privateKey, re)

    // Decrypt payload (should be empty).
    const payload = message2.slice(X25519_PUBLIC_KEY_SIZE)
    ss.decryptAndHash(payload)

    // Split into send/receive cipher states.
    // The Noise split returns (cs1, cs2). The flynn/noise Go library convention:
    //   Responder: send=cs1, receive=cs2
    //   Initiator: send=cs2, receive=cs1
    const [c1, c2] = ss.split()
    return { send: c2, receive: c1 }
  }
  finally {
    // Zero sensitive handshake material on every exit: a short or forged
    // message 2 must not leave the ephemeral key and the chaining key in the heap.
    clearClassicalHandshakeState(state)
  }
}
