import type { HandshakeState } from './noise'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { x25519 } from '@noble/curves/ed25519.js'
/**
 * Simulates the responder (Worker) side of Noise_NK handshake.
 * This mirrors the Go `noise.ResponderHandshake` function.
 */
import { blake2b } from '@noble/hashes/blake2.js'

import { hmac } from '@noble/hashes/hmac.js'
import { describe, expect, it } from 'vitest'
import { concatBytes } from './bytes'
import { CipherState, initiatorHandshake1, initiatorHandshake2 } from './noise'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'
/** The error of `chacha20poly1305` from `@noble/ciphers` when the tag does not authenticate the ciphertext. */
const AEAD_TAG_FAILURE = 'invalid tag'

function hkdf2(ck: Uint8Array, ikm: Uint8Array): [Uint8Array, Uint8Array] {
  const tempKey = hmac(blake2b, ck, ikm)
  const out1 = hmac(blake2b, tempKey, new Uint8Array([1]))
  const out2 = hmac(blake2b, tempKey, concatBytes(out1, new Uint8Array([2])))
  return [out1, out2]
}

function encrypt(key: Uint8Array, nonce: number, ad: Uint8Array, plaintext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true)
  const cipher = chacha20poly1305(key, nonceBytes, ad)
  return cipher.encrypt(plaintext)
}

function decrypt(key: Uint8Array, nonce: number, ad: Uint8Array, ciphertext: Uint8Array): Uint8Array {
  const nonceBytes = new Uint8Array(12)
  new DataView(nonceBytes.buffer).setUint32(4, nonce, true)
  const cipher = chacha20poly1305(key, nonceBytes, ad)
  return cipher.decrypt(ciphertext)
}

interface SimpleCipherState {
  k: Uint8Array
  n: number
}

/**
 * Minimal responder handshake implementation for testing (BLAKE2b version).
 * HASHLEN=64 for BLAKE2b, cipher keys truncated to 32 for ChaChaPoly.
 */
function responderHandshake(staticPrivateKey: Uint8Array, staticPublicKey: Uint8Array, message1: Uint8Array): {
  message2: Uint8Array
  send: SimpleCipherState
  receive: SimpleCipherState
} {
  // Init symmetric state (BLAKE2b: HASHLEN=64)
  const nameBytes = new TextEncoder().encode(PROTOCOL_NAME)
  let h: Uint8Array
  if (nameBytes.length <= 64) {
    h = new Uint8Array(64)
    h.set(nameBytes)
  }
  else {
    h = new Uint8Array(blake2b(nameBytes))
  }
  let ck = new Uint8Array(h)
  let hasK = false
  let k = new Uint8Array(32)
  let n = 0

  function mixHash(data: Uint8Array) {
    h = new Uint8Array(blake2b(concatBytes(h, data)))
  }

  function mixKey(ikm: Uint8Array) {
    const [newCk, tempK] = hkdf2(ck, ikm)
    ck = new Uint8Array(newCk) // 64 bytes
    k = new Uint8Array(tempK.slice(0, 32)) // truncate to 32 for ChaChaPoly
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

  // Mix empty prologue (required by Noise spec).
  mixHash(new Uint8Array(0))

  // Pre-message: <- s
  mixHash(staticPublicKey)

  // Read message 1: -> e, es
  const re = message1.slice(0, 32) // initiator's ephemeral public key
  mixHash(re)

  // es: DH(s, re)
  const dhES = x25519.getSharedSecret(staticPrivateKey, re)
  mixKey(dhES)

  // Decrypt payload
  const payload1 = message1.slice(32)
  decryptAndHash(payload1)

  // Write message 2: <- e, ee
  const ePrivate = x25519.utils.randomSecretKey()
  const ePublic = x25519.getPublicKey(ePrivate)
  mixHash(ePublic)

  // ee: DH(e, re)
  const dhEE = x25519.getSharedSecret(ePrivate, re)
  mixKey(dhEE)

  const encPayload = encryptAndHash(new Uint8Array(0))
  const message2 = concatBytes(ePublic, encPayload)

  // Split (truncate to 32 bytes for ChaChaPoly)
  const [tempK1, tempK2] = hkdf2(ck, new Uint8Array(0))

  // Responder: send=cs1 (tempK1), receive=cs2 (tempK2)
  // Initiator: send=cs2 (tempK2), receive=cs1 (tempK1)
  return {
    message2,
    send: {
      k: tempK1.slice(0, 32),
      n: 0,
    },
    receive: {
      k: tempK2.slice(0, 32),
      n: 0,
    },
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

describe('noise_NK handshake', () => {
  it('should complete a full handshake and exchange messages', () => {
    // Generate responder (Worker) static keypair.
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)

    // Initiator (Frontend) creates message 1.
    const { handshakeState, message1 } = initiatorHandshake1(staticPublic)
    expect(message1.length).toBeGreaterThan(32) // ephemeral key + encrypted payload

    // Responder processes message 1 and creates message 2.
    const responder = responderHandshake(staticPrivate, staticPublic, message1)

    // Initiator completes handshake.
    const session = initiatorHandshake2(handshakeState, responder.message2)

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
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)

    const { handshakeState, message1 } = initiatorHandshake1(staticPublic)
    const responder = responderHandshake(staticPrivate, staticPublic, message1)
    const session = initiatorHandshake2(handshakeState, responder.message2)

    // Send 100 messages in each direction.
    for (let i = 0; i < 100; i++) {
      // initiator → responder
      const fwd = new TextEncoder().encode(`forward-${i}`)
      const fwdCt = session.send.encrypt(fwd)
      const fwdPt = simpleCipherDecrypt(responder.receive, fwdCt)
      expect(new TextDecoder().decode(fwdPt)).toBe(`forward-${i}`)

      // responder → initiator
      const bwd = new TextEncoder().encode(`backward-${i}`)
      const bwdCt = simpleCipherEncrypt(responder.send, bwd)
      const bwdPt = session.receive.decrypt(bwdCt)
      expect(new TextDecoder().decode(bwdPt)).toBe(`backward-${i}`)
    }
  })

  it('should fail with wrong static key', () => {
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)

    // Initiator uses wrong key.
    const wrongPrivate = x25519.utils.randomSecretKey()
    const wrongPublic = x25519.getPublicKey(wrongPrivate)

    const { message1 } = initiatorHandshake1(wrongPublic)

    // Responder processes with correct key. In Noise_NK, after the es DH,
    // the AEAD decrypt of the empty payload in message 1 will fail because
    // the derived key is wrong (initiator DH'd with wrong public key).
    expect(() => responderHandshake(staticPrivate, staticPublic, message1)).toThrow(AEAD_TAG_FAILURE)
  })

  it('should encrypt empty messages', () => {
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)

    const { handshakeState, message1 } = initiatorHandshake1(staticPublic)
    const responder = responderHandshake(staticPrivate, staticPublic, message1)
    const session = initiatorHandshake2(handshakeState, responder.message2)

    const ct = session.send.encrypt(new Uint8Array(0))
    expect(ct.length).toBeGreaterThan(0) // Auth tag
    const pt = simpleCipherDecrypt(responder.receive, ct)
    expect(pt.length).toBe(0)
  })

  it('should reject short message2', () => {
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)

    const { handshakeState } = initiatorHandshake1(staticPublic)

    expect(() => initiatorHandshake2(handshakeState, new Uint8Array(16))).toThrow('too short')
  })

  /** Whether every byte of `bytes` is zero. */
  const allZero = (bytes: Uint8Array) => bytes.every(byte => byte === 0)

  /** A handshake state after message 1, with its secrets checked to be non-zero first. */
  function liveHandshakeState(): HandshakeState {
    const staticPublic = x25519.getPublicKey(x25519.utils.randomSecretKey())
    const { handshakeState } = initiatorHandshake1(staticPublic)
    expect(allZero(handshakeState.e.privateKey)).toBe(false)
    expect(allZero(handshakeState.ss.ck)).toBe(false)
    expect(allZero(handshakeState.ss.k)).toBe(false)
    return handshakeState
  }

  it('zeroes the handshake state after a handshake that succeeds, and the session keeps its own keys', () => {
    const staticPrivate = x25519.utils.randomSecretKey()
    const staticPublic = x25519.getPublicKey(staticPrivate)
    const { handshakeState, message1 } = initiatorHandshake1(staticPublic)
    const responder = responderHandshake(staticPrivate, staticPublic, message1)
    expect(allZero(handshakeState.e.privateKey)).toBe(false)

    const session = initiatorHandshake2(handshakeState, responder.message2)

    expect(allZero(handshakeState.e.privateKey)).toBe(true)
    expect(allZero(handshakeState.ss.ck)).toBe(true)
    expect(allZero(handshakeState.ss.k)).toBe(true)
    // The split copied the keys out, so zeroing the state left the session intact.
    const ct = session.send.encrypt(new TextEncoder().encode('after the zeroing'))
    expect(new TextDecoder().decode(simpleCipherDecrypt(responder.receive, ct))).toBe('after the zeroing')
  })

  it('zeroes the handshake state when message 2 is too short', () => {
    const state = liveHandshakeState()

    expect(() => initiatorHandshake2(state, new Uint8Array(16))).toThrow('too short')

    expect(allZero(state.e.privateKey)).toBe(true)
    expect(allZero(state.ss.ck)).toBe(true)
    expect(allZero(state.ss.k)).toBe(true)
  })

  it('zeroes the handshake state when message 2 fails to authenticate', () => {
    const state = liveHandshakeState()
    // A real ephemeral key, followed by a tag that no key produced.
    const forged = concatBytes(x25519.getPublicKey(x25519.utils.randomSecretKey()), new Uint8Array(16))

    expect(() => initiatorHandshake2(state, forged)).toThrow(AEAD_TAG_FAILURE)

    expect(allZero(state.e.privateKey)).toBe(true)
    expect(allZero(state.ss.ck)).toBe(true)
    expect(allZero(state.ss.k)).toBe(true)
  })
})

describe('CipherState limits', () => {
  it('should reject plaintext exceeding MAX_PLAINTEXT_SIZE', () => {
    const key = new Uint8Array(32)
    key[0] = 1
    const cs = new CipherState(key)
    const tooBig = new Uint8Array(65535 - 16 + 1) // 65520 bytes, one over limit
    expect(() => cs.encrypt(tooBig)).toThrow('plaintext too large')
  })

  it('should accept plaintext at MAX_PLAINTEXT_SIZE', () => {
    const key = new Uint8Array(32)
    key[0] = 1
    const cs = new CipherState(key)
    const maxSize = new Uint8Array(65535 - 16) // 65519 bytes, exactly at limit
    expect(() => cs.encrypt(maxSize)).not.toThrow()
  })

  it('should return false from needsRekey initially', () => {
    const key = new Uint8Array(32)
    key[0] = 1
    const cs = new CipherState(key)
    expect(cs.needsRekey()).toBe(false)
  })

  it('zeroes the key and the previous key in destroy, and refuses every later use', () => {
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    // The receive direction moves the key array that this test keeps into prev.
    cs.rekeyWithSecret(dh, null, true)
    // @ts-expect-error accessing private field to keep the current key
    const current: Uint8Array = cs.k
    expect(current.every(byte => byte === 0)).toBe(false)

    cs.destroy()

    expect(key.every(byte => byte === 0)).toBe(true)
    expect(current.every(byte => byte === 0)).toBe(true)
    // @ts-expect-error accessing private field to assert prev was dropped
    expect(cs.prev).toBeNull()
    expect(() => cs.encrypt(new Uint8Array(1))).toThrow(/destroyed/)
    expect(() => cs.decrypt(new Uint8Array(17))).toThrow(/destroyed/)
    // A second destroy does nothing more.
    expect(() => cs.destroy()).not.toThrow()
  })

  // A rekey would install fresh keys into a state that every user already
  // treats as dead, and the keys would then outlive the close that zeroed it.
  it('refuses a rekey after destroy, zeroes the secrets it was given, and keeps the zeroed key', () => {
    const cs = new CipherState(new Uint8Array(32).fill(7))
    cs.destroy()
    const dhSecret = new Uint8Array(32).fill(9)
    const pqSecret = new Uint8Array(32).fill(5)

    expect(() => cs.rekeyWithSecret(dhSecret, pqSecret, false)).toThrow(/destroyed/)
    // A caller relies on the rekey to zero its fresh secrets on every exit.
    expect(dhSecret.every(byte => byte === 0)).toBe(true)
    expect(pqSecret.every(byte => byte === 0)).toBe(true)
    // @ts-expect-error accessing private field to assert no key was installed
    expect((cs.k as Uint8Array).every(byte => byte === 0)).toBe(true)
  })

  it('should change the key, reset nonce, and decrypt new traffic when rekeyWithSecret runs', () => {
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const peer = new CipherState(key)
    cs.encrypt(new TextEncoder().encode('before')) // advance nonce
    expect(cs.nonce()).toBe(1)

    // A fresh 32-byte DH secret shared by both sides.
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i

    // Each side needs its own copy: rekeyWithSecret zeroes its input.
    const dhPeer = dh.slice()
    cs.rekeyWithSecret(dh, null, true)
    peer.rekeyWithSecret(dhPeer, null, true)
    expect(cs.nonce()).toBe(0)

    const ctNew = cs.encrypt(new TextEncoder().encode('after'))
    expect(new TextDecoder().decode(peer.decrypt(ctNew))).toBe('after')
  })

  it('should match the Go fresh-DH interop vector in rekeyWithSecret', async () => {
    // Fixed vectors from testdata/noise_rekey_vectors.json, shared with
    // backend/internal/noise/noise_test.go so Go and TS stay on the same
    // fresh-DH rekey derivation:
    //   ck1 = HKDF(k, dhSecret); k' = HKDF(ck1, nil)[0:32]
    // Resolved from this file like ipAddress.test.ts, since the fixture lives
    // at the repo root outside vite's root.
    const { readFileSync } = await import('node:fs')
    const { dirname, resolve } = await import('node:path')
    const { fileURLToPath } = await import('node:url')
    const fixture = JSON.parse(readFileSync(
      resolve(dirname(fileURLToPath(import.meta.url)), '../../../testdata/noise_rekey_vectors.json'),
      'utf8',
    )) as { vectors: { name: string, keyHex: string, dhSecretHex: string, warmupPlaintext: string, plaintext: string, expectedCiphertextHex: string }[] }
    expect(fixture.vectors.length).toBeGreaterThan(0)
    const fromHex = (hex: string) => new Uint8Array(hex.match(/../g)!.map(b => Number.parseInt(b, 16)))
    for (const v of fixture.vectors) {
      const cs = new CipherState(fromHex(v.keyHex))
      cs.encrypt(new TextEncoder().encode(v.warmupPlaintext))
      cs.rekeyWithSecret(fromHex(v.dhSecretHex), null, true)
      const ct = cs.encrypt(new TextEncoder().encode(v.plaintext))
      const hex = Array.from(ct, b => b.toString(16).padStart(2, '0')).join('')
      expect(hex).toBe(v.expectedCiphertextHex)
    }
  })

  it('grace window: an old-key straddling frame decrypts via prev after rekey', () => {
    // Mirrors Go's TestCipherStateRekey straddle assertion: after the receiver
    // rotates, a frame the peer encrypted under the OLD key (still in flight on
    // the wire) must decrypt via the retained previous key within the window.
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const oldKey = new CipherState(key) // peer keeps the pre-rekey key

    // Both sides at nonce 0; rotate cs, then the straddling frame is at nonce 0.
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    cs.rekeyWithSecret(dh, null, true)

    const ctStraddle = oldKey.encrypt(new TextEncoder().encode('straddle'))
    const pt = cs.decrypt(ctStraddle)
    expect(new TextDecoder().decode(pt)).toBe('straddle')
  })

  it('grace window: after expiry, an old-key frame fails closed', () => {
    // Mirrors Go's TestCipherStateRekeyGraceWindowExpiry: once prevExpiresAt is
    // in the past, an old-key frame must NOT decrypt (fail closed), proving the
    // previous key is genuinely retired at window expiry.
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    cs.rekeyWithSecret(dh, null, true)

    // Force expiry by rewinding the deadline into the past.
    // @ts-expect-error accessing private field to simulate clock advance
    cs.prevExpiresAt = -1
    const ctStraddle = oldKey.encrypt(new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(ctStraddle)).toThrow(AEAD_TAG_FAILURE)
  })

  it('drops the retained previous key in clearPrev so an old-key frame fails', () => {
    // clearPrev is the explicit "drop the retained prev" method (exported for
    // tests and any future send path that needs it). The production send
    // rotation goes through rekeyWithSecret(..., false) and never sets prev in
    // the first place (see the next test); this pins clearPrev itself.
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    cs.rekeyWithSecret(dh, null, true)
    cs.clearPrev()

    const ctStraddle = oldKey.encrypt(new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(ctStraddle)).toThrow(AEAD_TAG_FAILURE)
  })

  it('keeps no previous key when rekeyWithSecret runs with retainPrev=false (send direction)', () => {
    // The send direction keeps no grace window. retainPrev=false must zero the
    // replaced key and NOT retain a prev — structurally, so a caller cannot
    // forget a clearPrev. An old-key frame must fail immediately after the
    // rotation, and prev must be null.
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    cs.rekeyWithSecret(dh, null, false)

    // @ts-expect-error accessing private field to assert no prev was retained
    expect(cs.prev).toBeNull()
    const ctStraddle = oldKey.encrypt(new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(ctStraddle)).toThrow(AEAD_TAG_FAILURE)
  })

  it('grace window: an expired prev is zeroed on the next decrypt (not left in the heap)', () => {
    // SCAN-3: when the grace window elapses, the next Decrypt must retire prev
    // (zero it and drop the reference) rather than just skip it — otherwise an
    // idle channel leaves the still-valid previous-epoch key in the heap until
    // the next rekey or close, widening the forward-secrecy surface the window
    // introduced.
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const cs = new CipherState(key)
    const oldKey = new CipherState(key) // peer view, still on the old key
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    cs.rekeyWithSecret(dh, null, true)
    // @ts-expect-error accessing private field to force the deadline into the past
    cs.prevExpiresAt = -1

    // A straddling old-key frame: current key fails, prev is expired, so the
    // decrypt must retire prev (zero + drop) and then fail closed.
    const ctStraddle = oldKey.encrypt(new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(ctStraddle)).toThrow(AEAD_TAG_FAILURE)
    // @ts-expect-error accessing private field to assert prev was retired on expiry
    expect(cs.prev).toBeNull()
  })
})
