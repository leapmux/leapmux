import type { HandshakeState, Session } from '~/lib/noise'
import type { HybridHandshakeState } from '~/lib/noise-hybrid'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { SymmetricState } from '~/lib/noise'

// Test doubles of the channel session's Noise state: a cipher-state pair with the
// real AEAD, and handshake states of the real shape.
//
// This module has no side effect, so a node test can load it. The channel
// manager harness (~/lib/channel.test-support) clears the key pins of browser
// storage before every test, which only the dom project can run.

/** Minimal CipherState for testing (same crypto as the real one). */
class TestCipherState {
  private k: Uint8Array
  private n: number
  private destroyed = false

  /**
   * Keep a copy of `key`. The two sides of a paired session start from the same
   * key, and `destroy` on one side must not zero the key of the other side.
   */
  constructor(key: Uint8Array) {
    this.k = new Uint8Array(key)
    this.n = 0
  }

  encrypt(plaintext: Uint8Array): Uint8Array {
    if (this.destroyed)
      throw new Error('noise: cipher state destroyed')
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const ct = cipher.encrypt(plaintext)
    this.n++
    return ct
  }

  decrypt(ciphertext: Uint8Array): Uint8Array {
    if (this.destroyed)
      throw new Error('noise: cipher state destroyed')
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const pt = cipher.decrypt(ciphertext)
    this.n++
    return pt
  }

  needsRekey(): boolean {
    return false
  }

  /**
   * Test double for the fresh-DH rekey: ignore the secrets (the test harness
   * uses deterministic tweaked keys so paired sessions stay in sync without
   * real HKDF) and apply the same deterministic key tweak the old rekey() did.
   * Both directions of a paired session call this with mirrored keys, so they
   * produce matching new keys.
   */
  rekeyWithSecret(_dhSecret: Uint8Array, _pqSecret: Uint8Array | null, _retainPrev: boolean): void {
    const next = new Uint8Array(this.k)
    for (let i = 0; i < next.length; i++)
      next[i] = ((next[i] ?? 0) + 1) & 0xFF
    this.k = next
    this.n = 0
  }

  /** No-op for the test double (production retains a prev key for the grace window). */
  clearPrev(): void {}

  nonce(): number {
    return this.n
  }

  /** Zero the key and refuse every later encrypt or decrypt, as CipherState.destroy does. */
  destroy(): void {
    this.k.fill(0)
    this.destroyed = true
  }
}

/** Create a matched pair of cipher states (send/receive mirrors). */
export function createTestSession(): { initiator: Session, responder: Session } {
  const key1 = new Uint8Array(32)
  const key2 = new Uint8Array(32)
  key1[0] = 1 // Different keys for send/receive
  key2[0] = 2
  return {
    initiator: {
      send: new TestCipherState(key1),
      receive: new TestCipherState(key2),
    } satisfies Session,
    responder: {
      send: new TestCipherState(key2),
      receive: new TestCipherState(key1),
    } satisfies Session,
  }
}

/** The size of an ML-KEM-1024 shared secret (FIPS 203). */
const MLKEM_SHARED_SECRET_SIZE = 32
/** The size of an ML-KEM-1024 ciphertext (FIPS 203). */
const MLKEM_CIPHERTEXT_SIZE = 1568

/**
 * A hybrid handshake state of the real shape. Every secret is zero, except the
 * chaining key and the hash, which hold the padded protocol name 'test'. A mock
 * handshake returns it, because the session zeroes the state on every exit,
 * and zeroing an empty object throws.
 */
export function blankHybridHandshakeState(): HybridHandshakeState {
  return {
    ss: new SymmetricState('test'),
    e: {
      privateKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
      publicKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
    },
    rs: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
    mlkemSS: new Uint8Array(MLKEM_SHARED_SECRET_SIZE),
    mlkemCT: new Uint8Array(MLKEM_CIPHERTEXT_SIZE),
  }
}

/**
 * A classic handshake state of the real shape. Every secret is zero, except the
 * chaining key and the hash (see blankHybridHandshakeState).
 */
export function blankClassicHandshakeState(): HandshakeState {
  return {
    ss: new SymmetricState('test'),
    e: {
      privateKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
      publicKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
    },
    rs: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
  }
}
