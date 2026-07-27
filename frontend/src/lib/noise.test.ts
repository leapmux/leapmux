import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { x25519 } from '@noble/curves/ed25519.js'
/**
 * Simulates the responder (Worker) side of Noise_NK handshake.
 * This mirrors the Go `noise.ResponderHandshake` function.
 */
import { blake2b } from '@noble/hashes/blake2.js'
import { hmac } from '@noble/hashes/hmac.js'

import { describe, expect, it } from 'vitest'
import { CipherState, initiatorHandshake1, initiatorHandshake2 } from './noise'

const PROTOCOL_NAME = 'Noise_NK_25519_ChaChaPoly_BLAKE2b'

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
    expect(() => responderHandshake(staticPrivate, staticPublic, message1)).toThrow()
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
})

describe('cipherState limits', () => {
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

  it('needsRekey should return false initially', () => {
    const key = new Uint8Array(32)
    key[0] = 1
    const cs = new CipherState(key)
    expect(cs.needsRekey()).toBe(false)
  })

  it('rekeyWithSecret should change the key, reset nonce, and decrypt new traffic', () => {
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

  it('rekeyWithSecret should match the Go fresh-DH interop vector', () => {
    // Fixed vector shared with backend/internal/noise/noise_test.go so Go and TS
    // stay on the same fresh-DH rekey derivation:
    //   ck1 = HKDF(k, dhSecret); k' = HKDF(ck1, nil)[0:32]
    const key = new Uint8Array(32)
    for (let i = 0; i < 32; i++) key[i] = i + 1
    const dh = new Uint8Array(32)
    for (let i = 0; i < 32; i++) dh[i] = 0xA0 + i
    const cs = new CipherState(key)
    cs.encrypt(new TextEncoder().encode('x'))
    cs.rekeyWithSecret(dh, null, true)
    const ct = cs.encrypt(new TextEncoder().encode('hello-rekey'))
    const hex = Array.from(ct, b => b.toString(16).padStart(2, '0')).join('')
    expect(hex).toBe('2c0f3a3f9a452bfe1bdfec4bf4fcfc412c28dc17be6865a3048d46')
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
    expect(() => cs.decrypt(ctStraddle)).toThrow()
  })

  it('clearPrev drops the retained previous key so an old-key frame fails', () => {
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
    expect(() => cs.decrypt(ctStraddle)).toThrow()
  })

  it('rekeyWithSecret(retainPrev=false) keeps no previous key (send direction)', () => {
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
    expect(() => cs.decrypt(ctStraddle)).toThrow()
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
    expect(() => cs.decrypt(ctStraddle)).toThrow()
    // @ts-expect-error accessing private field to assert prev was retired on expiry
    expect(cs.prev).toBeNull()
  })
})
