import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { x25519 } from '@noble/curves/ed25519.js'
import { blake2b } from '@noble/hashes/blake2.js'
/**
 * Tests for hybrid post-quantum Noise_NK handshake.
 *
 * Simulates the responder (Worker) side in TypeScript to test the
 * initiator implementation in noise-hybrid.ts.
 */
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'
import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'

import { describe, expect, it } from 'vitest'
import { concatBytes, hkdf } from './noise'
import { clearHandshakeState, initiatorHandshake1, initiatorHandshake2 } from './noise-hybrid'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'
const DH_LEN = 32
const AEAD_TAG_SIZE = 16

function encrypt(key: Uint8Array, nonce: number, ad: Uint8Array, plaintext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true)
  return chacha20poly1305(key, nonceBytes, ad).encrypt(plaintext)
}

function decrypt(key: Uint8Array, nonce: number, ad: Uint8Array, ciphertext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true)
  return chacha20poly1305(key, nonceBytes, ad).decrypt(ciphertext)
}

interface SimpleCipherState {
  k: Uint8Array
  n: number
}

/**
 * Minimal hybrid responder handshake for testing.
 */
function responderHandshake(
  x25519Priv: Uint8Array,
  x25519Pub: Uint8Array,
  mlkemPriv: Uint8Array,
  slhdsaPriv: Uint8Array,
  message1: Uint8Array,
): {
  message2: Uint8Array
  send: SimpleCipherState
  receive: SimpleCipherState
} {
  const noiseMsg1Len = DH_LEN + AEAD_TAG_SIZE // 48
  const mlkemCT = message1.slice(noiseMsg1Len)

  // Init symmetric state.
  const nameBytes = new TextEncoder().encode(PROTOCOL_NAME)
  let h = new Uint8Array(64)
  h.set(nameBytes)
  let ck = new Uint8Array(h)
  let hasK = false
  let k = new Uint8Array(32)
  let n = 0

  function mixHash(data: Uint8Array) {
    h = blake2b(concatBytes(h, data))
  }
  function mixKey(ikm: Uint8Array) {
    const [newCk, tempK] = hkdf(ck, ikm)
    ck = new Uint8Array(newCk)
    k = new Uint8Array(tempK.slice(0, 32))
    n = 0
    hasK = true
  }
  function decryptAndHash(ct: Uint8Array): Uint8Array {
    if (!hasK) {
      mixHash(ct)
      return ct
    }
    const pt = decrypt(k, n, h, ct)
    mixHash(ct)
    n++
    return pt
  }
  function encryptAndHash(pt: Uint8Array): Uint8Array {
    if (!hasK) {
      mixHash(pt)
      return pt
    }
    const ct = encrypt(k, n, h, pt)
    mixHash(ct)
    n++
    return ct
  }

  // Mix empty prologue.
  mixHash(new Uint8Array(0))

  // Pre-message: <- s
  mixHash(x25519Pub)

  // Read message 1: -> e, es
  const re = message1.slice(0, DH_LEN)
  mixHash(re)

  const dhES = x25519.getSharedSecret(x25519Priv, re)
  mixKey(dhES)

  decryptAndHash(message1.slice(DH_LEN, noiseMsg1Len))

  // Bind ML-KEM ciphertext into the handshake hash (matches initiator's mixHash).
  mixHash(mlkemCT)

  // Write message 2: <- e, ee
  const ePriv = x25519.utils.randomSecretKey()
  const ePub = x25519.getPublicKey(ePriv)
  mixHash(ePub)

  const dhEE = x25519.getSharedSecret(ePriv, re)
  mixKey(dhEE)

  const encPayload = encryptAndHash(new Uint8Array(0))
  const noiseMsg2 = concatBytes(ePub, encPayload)

  // ML-KEM decapsulation.
  const mlkemSS = ml_kem1024.decapsulate(mlkemCT, mlkemPriv)

  // Compute transcript: BLAKE2b(h || mlkem_ct || mlkem_ss)
  const transcript = blake2b(concatBytes(h, mlkemCT, mlkemSS))

  // Sign transcript with SLH-DSA.
  const sig = slh_dsa_shake_256f.sign(transcript, slhdsaPriv)

  // Hybrid split: mix ML-KEM SS into ck before deriving keys.
  const [ck2] = hkdf(ck, mlkemSS)
  ck = ck2
  const [tempK1, tempK2] = hkdf(ck, new Uint8Array(0))

  return {
    message2: concatBytes(noiseMsg2, sig),
    send: { k: tempK1.slice(0, 32), n: 0 },
    receive: { k: tempK2.slice(0, 32), n: 0 },
  }
}

function simpleCipherEncrypt(state: SimpleCipherState, plaintext: Uint8Array): Uint8Array {
  const ct = encrypt(state.k, state.n, new Uint8Array(0), plaintext)
  state.n++
  return ct
}

function simpleCipherDecrypt(state: SimpleCipherState, ciphertext: Uint8Array): Uint8Array {
  const pt = decrypt(state.k, state.n, new Uint8Array(0), ciphertext)
  state.n++
  return pt
}

describe('hybrid Noise_NK handshake', { timeout: 120_000 }, () => {
  // Generate worker key bundle once — these are slow operations.
  const x25519Priv = x25519.utils.randomSecretKey()
  const x25519Pub = x25519.getPublicKey(x25519Priv)
  const mlkemKeys = ml_kem1024.keygen()
  const slhdsaKeys = slh_dsa_shake_256f.keygen()

  it('should complete a full hybrid handshake and exchange messages', () => {
    // Initiator creates message 1.
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    expect(message1.length).toBe(48 + 1568) // noiseMsg1 + mlkemCT

    // Responder processes and creates message 2.
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )
    expect(responder.message2.length).toBe(48 + 49856) // noiseMsg2 + slhdsaSig

    // Initiator completes handshake.
    const session = initiatorHandshake2(handshakeState, responder.message2, slhdsaKeys.publicKey)

    // Test initiator → responder.
    const msg1 = new TextEncoder().encode('hello from initiator')
    const ct1 = session.send.encrypt(msg1)
    const pt1 = simpleCipherDecrypt(responder.receive, ct1)
    expect(new TextDecoder().decode(pt1)).toBe('hello from initiator')

    // Test responder → initiator.
    const msg2 = new TextEncoder().encode('hello from responder')
    const ct2 = simpleCipherEncrypt(responder.send, msg2)
    const pt2 = session.receive.decrypt(ct2)
    expect(new TextDecoder().decode(pt2)).toBe('hello from responder')
  })

  it('should handle multiple messages in sequence', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )
    const session = initiatorHandshake2(handshakeState, responder.message2, slhdsaKeys.publicKey)

    for (let i = 0; i < 50; i++) {
      const fwd = new TextEncoder().encode(`forward-${i}`)
      const fwdCt = session.send.encrypt(fwd)
      const fwdPt = simpleCipherDecrypt(responder.receive, fwdCt)
      expect(new TextDecoder().decode(fwdPt)).toBe(`forward-${i}`)

      const bwd = new TextEncoder().encode(`backward-${i}`)
      const bwdCt = simpleCipherEncrypt(responder.send, bwd)
      const bwdPt = session.receive.decrypt(bwdCt)
      expect(new TextDecoder().decode(bwdPt)).toBe(`backward-${i}`)
    }
  })

  it('should fail with wrong SLH-DSA key', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )

    // Use a different SLH-DSA public key for verification.
    const wrongKeys = slh_dsa_shake_256f.keygen()
    expect(() => initiatorHandshake2(handshakeState, responder.message2, wrongKeys.publicKey))
      .toThrow('SLH-DSA')
  })

  it('should fail with short message2', () => {
    const { handshakeState } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    expect(() => initiatorHandshake2(handshakeState, new Uint8Array(16), slhdsaKeys.publicKey))
      .toThrow('wrong size')
  })

  it('should fail with oversized message2', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )
    // Append an extra byte to the valid message2.
    const oversized = new Uint8Array(responder.message2.length + 1)
    oversized.set(responder.message2)
    expect(() => initiatorHandshake2(handshakeState, oversized, slhdsaKeys.publicKey))
      .toThrow('wrong size')
  })

  it('should zero handshake state after completing handshake', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )

    // Verify sensitive material exists before handshake2.
    expect(handshakeState.mlkemSS.length).toBeGreaterThan(0)
    expect(handshakeState.mlkemCT.length).toBeGreaterThan(0)
    expect(handshakeState.e.privateKey.length).toBeGreaterThan(0)

    initiatorHandshake2(handshakeState, responder.message2, slhdsaKeys.publicKey)

    // After handshake2, all sensitive fields should be zeroed.
    expect(handshakeState.mlkemSS.every(b => b === 0)).toBe(true)
    expect(handshakeState.mlkemCT.every(b => b === 0)).toBe(true)
    expect(handshakeState.e.privateKey.every(b => b === 0)).toBe(true)
    expect(handshakeState.ss.h.every(b => b === 0)).toBe(true)
    expect(handshakeState.ss.ck.every(b => b === 0)).toBe(true)
    expect(handshakeState.ss.k.every(b => b === 0)).toBe(true)
  })

  it('should zero handshake state on SLH-DSA verification failure', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )

    const wrongKeys = slh_dsa_shake_256f.keygen()
    expect(() => initiatorHandshake2(handshakeState, responder.message2, wrongKeys.publicKey))
      .toThrow('SLH-DSA')

    // Even on failure, sensitive material should be zeroed.
    expect(handshakeState.mlkemSS.every(b => b === 0)).toBe(true)
    expect(handshakeState.e.privateKey.every(b => b === 0)).toBe(true)
  })

  it('should zero handshake state via clearHandshakeState', () => {
    const { handshakeState } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)

    // Verify non-zero before clearing.
    expect(handshakeState.mlkemSS.some(b => b !== 0)).toBe(true)

    clearHandshakeState(handshakeState)

    expect(handshakeState.mlkemSS.every(b => b === 0)).toBe(true)
    expect(handshakeState.mlkemCT.every(b => b === 0)).toBe(true)
    expect(handshakeState.e.privateKey.every(b => b === 0)).toBe(true)
  })

  it('should fail when ML-KEM ciphertext is tampered (handshake hash divergence)', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)

    // Tamper with the ML-KEM ciphertext portion (after the 48-byte Noise message).
    const tampered = new Uint8Array(message1)
    tampered[48] ^= 0xFF

    // Responder processes tampered message — ML-KEM implicit rejection means
    // decapsulation succeeds with a random shared secret.
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      tampered,
    )

    // Initiator should fail because handshake hashes diverged
    // (mixHash(mlkemCT) differs between initiator and responder).
    expect(() => initiatorHandshake2(handshakeState, responder.message2, slhdsaKeys.publicKey))
      .toThrow()
  })

  it('should encrypt empty messages', () => {
    const { handshakeState, message1 } = initiatorHandshake1(x25519Pub, mlkemKeys.publicKey)
    const responder = responderHandshake(
      x25519Priv,
      x25519Pub,
      mlkemKeys.secretKey,
      slhdsaKeys.secretKey,
      message1,
    )
    const session = initiatorHandshake2(handshakeState, responder.message2, slhdsaKeys.publicKey)

    const ct = session.send.encrypt(new Uint8Array(0))
    expect(ct.length).toBeGreaterThan(0) // Auth tag
    const pt = simpleCipherDecrypt(responder.receive, ct)
    expect(pt.length).toBe(0)
  })
})
