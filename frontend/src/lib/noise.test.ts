import { bytesToHex, hexToBytes } from '@noble/hashes/utils.js'
import { describe, expect, it, vi } from 'vitest'
import { allZero, entropy, expectHandshakeFrames, handshakeStateIsZero, handshakeVector, readNoiseVectors, vectorKeys } from '~/test-support/noiseVectors'
import { CipherState, initiatorHandshake1WithEntropy, initiatorHandshake2, SymmetricState } from './noise'

const classic = handshakeVector('classic')
const EMPTY = new Uint8Array(0)

function start(kept?: Uint8Array[], workerId = classic.workerId, keys = vectorKeys(classic)) {
  return initiatorHandshake1WithEntropy(entropy(classic.initiatorEntropy, kept), workerId, keys)
}

function expectStateZero(state: ReturnType<typeof start>['handshakeState']): void {
  expect(handshakeStateIsZero(state)).toBe(true)
}

describe('classic Noise handshake', () => {
  it('matches message 1 of the Go vector and wipes the encapsulation seed', () => {
    const kept: Uint8Array[] = []
    const { handshakeState, message1 } = start(kept)
    expect(message1).toHaveLength(1632)
    expect(bytesToHex(message1)).toBe(classic.message1Hex)
    expect(allZero(kept[0]!)).toBe(false)
    expect(allZero(kept[1]!)).toBe(true)
    initiatorHandshake2(handshakeState, hexToBytes(classic.message2Hex))
    expectStateZero(handshakeState)
  })

  it('derives the session keys and frame authentication of the Go vector', () => {
    const { handshakeState } = start()
    const session = initiatorHandshake2(handshakeState, hexToBytes(classic.message2Hex))
    expectHandshakeFrames(session, classic)
    expectStateZero(handshakeState)
  })

  it('refuses another Worker id and zeroes the state', () => {
    const { handshakeState } = start(undefined, 'worker-other')
    expect(() => initiatorHandshake2(handshakeState, hexToBytes(classic.message2Hex))).toThrow('invalid tag')
    expectStateZero(handshakeState)
  })

  it('refuses a forged payload tag and zeroes the state', () => {
    const { handshakeState } = start()
    const message2 = hexToBytes(classic.message2Hex)
    message2[32] = (message2[32] ?? 0) ^ 1
    expect(() => initiatorHandshake2(handshakeState, message2)).toThrow('invalid tag')
    expectStateZero(handshakeState)
  })

  it.each([16, 49])('refuses a message 2 of %i bytes and zeroes the state', (size) => {
    const { handshakeState } = start()
    expect(() => initiatorHandshake2(handshakeState, new Uint8Array(size))).toThrow('wrong size')
    expectStateZero(handshakeState)
  })

  it('refuses a low-order responder ephemeral and zeroes the state', () => {
    const { handshakeState } = start()
    expect(() => initiatorHandshake2(handshakeState, new Uint8Array(48))).toThrow('invalid private or public key received')
    expectStateZero(handshakeState)
  })

  it('refuses an empty Worker id', () => {
    expect(() => start(undefined, '')).toThrow('Worker id is required')
  })

  it('zeroes every seed when message 1 fails', () => {
    const kept: Uint8Array[] = []
    expect(() => start(kept, classic.workerId, vectorKeys(classic, new Uint8Array(1568).fill(0xFF)))).toThrow('wrong publicKey modulus')
    expect(kept.length).toBe(2)
    for (const bytes of kept)
      expect(allZero(bytes)).toBe(true)
  })
})

describe('SymmetricState', () => {
  it('replaces key material in the owned buffers and clears them together', () => {
    const state = new SymmetricState('test')
    const chainingKey = state.ck
    const cipherKey = state.k
    state.mixKey(new Uint8Array(32).fill(1))
    const firstChainingKey = chainingKey.slice()
    const firstCipherKey = cipherKey.slice()

    state.mixKey(new Uint8Array(32).fill(2))

    expect(state.ck).toBe(chainingKey)
    expect(state.k).toBe(cipherKey)
    expect(chainingKey).not.toEqual(firstChainingKey)
    expect(cipherKey).not.toEqual(firstCipherKey)
    state.clear()
    expect(allZero(chainingKey)).toBe(true)
    expect(allZero(cipherKey)).toBe(true)
  })
})

describe('CipherState limits', () => {
  it('clears the previous receive key while idle', () => {
    vi.useFakeTimers()
    try {
      const oldKey = new Uint8Array(32).fill(1)
      const cipher = new CipherState(oldKey)
      cipher.rekeyWithSecret(new Uint8Array(32).fill(2), null, true)
      expect(allZero(oldKey)).toBe(false)
      vi.advanceTimersByTime(10_000)
      expect(allZero(oldKey)).toBe(true)
      cipher.destroy()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('rejects plaintext above the chunk limit', () => {
    const cs = new CipherState(new Uint8Array(32).fill(1))
    expect(() => cs.encrypt(EMPTY, new Uint8Array(65520))).toThrow('plaintext too large')
  })

  it('accepts plaintext at the chunk limit', () => {
    const cs = new CipherState(new Uint8Array(32).fill(1))
    expect(() => cs.encrypt(EMPTY, new Uint8Array(65519))).not.toThrow()
  })

  it('returns false from needsRekey initially', () => {
    expect(new CipherState(new Uint8Array(32)).needsRekey()).toBe(false)
  })

  it('zeroes the key and previous key in destroy and refuses every later use', () => {
    const key = Uint8Array.from({ length: 32 }, (_, i) => i + 1)
    const cs = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    // @ts-expect-error access the private current key to verify its cleanup
    const current: Uint8Array = cs.k
    expect(allZero(current)).toBe(false)
    cs.destroy()
    expect(allZero(key)).toBe(true)
    expect(allZero(current)).toBe(true)
    // @ts-expect-error assert that destroy dropped prev
    expect(cs.prev).toBeNull()
    expect(() => cs.encrypt(EMPTY, new Uint8Array(1))).toThrow(/destroyed/)
    expect(() => cs.decrypt(EMPTY, new Uint8Array(17))).toThrow(/destroyed/)
    expect(() => cs.destroy()).not.toThrow()
  })

  it('refuses a rekey after destroy and zeroes both supplied secrets', () => {
    const cs = new CipherState(new Uint8Array(32).fill(7))
    cs.destroy()
    const dhSecret = new Uint8Array(32).fill(9)
    const pqSecret = new Uint8Array(32).fill(5)
    expect(() => cs.rekeyWithSecret(dhSecret, pqSecret, false)).toThrow(/destroyed/)
    expect(allZero(dhSecret)).toBe(true)
    expect(allZero(pqSecret)).toBe(true)
    // @ts-expect-error assert that no new key was installed
    expect(allZero(cs.k)).toBe(true)
  })

  it('changes the key, resets the nonce, and decrypts new traffic', () => {
    const key = Uint8Array.from({ length: 32 }, (_, i) => i + 1)
    const cs = new CipherState(key)
    const peer = new CipherState(key)
    cs.encrypt(EMPTY, new TextEncoder().encode('before'))
    expect(cs.nonce()).toBe(1)
    const dh = new Uint8Array(32).fill(0xA0)
    const peerDH = dh.slice()
    cs.rekeyWithSecret(dh, null, true)
    peer.rekeyWithSecret(peerDH, null, true)
    expect(cs.nonce()).toBe(0)
    const ciphertext = cs.encrypt(EMPTY, new TextEncoder().encode('after'))
    expect(new TextDecoder().decode(peer.decrypt(EMPTY, ciphertext))).toBe('after')
  })

  it('matches the Go rekey ratchet vectors', () => {
    const fixture = readNoiseVectors<{
      vectors: { keyHex: string, dhSecretHex: string, pqSecretHex?: string, warmupPlaintext: string, plaintext: string, expectedCiphertextHex: string }[]
    }>('noise_rekey_vectors.json')
    for (const vector of fixture.vectors) {
      const cs = new CipherState(hexToBytes(vector.keyHex))
      cs.encrypt(EMPTY, new TextEncoder().encode(vector.warmupPlaintext))
      cs.rekeyWithSecret(hexToBytes(vector.dhSecretHex), vector.pqSecretHex === undefined
        ? null
        : hexToBytes(vector.pqSecretHex), true)
      const ciphertext = cs.encrypt(EMPTY, new TextEncoder().encode(vector.plaintext))
      expect(bytesToHex(ciphertext)).toBe(vector.expectedCiphertextHex)
    }
  })

  it('decrypts an old-key straddling frame through the grace window', () => {
    const key = new Uint8Array(32).fill(1)
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    const ciphertext = oldKey.encrypt(EMPTY, new TextEncoder().encode('straddle'))
    expect(new TextDecoder().decode(cs.decrypt(EMPTY, ciphertext))).toBe('straddle')
  })

  it('fails an old-key frame after the grace window expires', () => {
    const key = new Uint8Array(32).fill(1)
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    // @ts-expect-error move the private deadline into the past
    cs.prevExpiresAt = -1
    const ciphertext = oldKey.encrypt(EMPTY, new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(EMPTY, ciphertext)).toThrow('invalid tag')
  })

  it('drops the previous key in clearPrev', () => {
    const key = new Uint8Array(32).fill(1)
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    cs.clearPrev()
    const ciphertext = oldKey.encrypt(EMPTY, new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(EMPTY, ciphertext)).toThrow('invalid tag')
  })

  it('keeps no previous key for a send-direction rekey', () => {
    const key = new Uint8Array(32).fill(1)
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, false)
    // @ts-expect-error assert that send retained no previous key
    expect(cs.prev).toBeNull()
    const ciphertext = oldKey.encrypt(EMPTY, new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(EMPTY, ciphertext)).toThrow('invalid tag')
  })

  it('zeroes and drops an expired previous key on the next decrypt', () => {
    const key = new Uint8Array(32).fill(1)
    const cs = new CipherState(key)
    const oldKey = new CipherState(key)
    cs.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    // @ts-expect-error move the private deadline into the past
    cs.prevExpiresAt = -1
    const ciphertext = oldKey.encrypt(EMPTY, new TextEncoder().encode('straddle'))
    expect(() => cs.decrypt(EMPTY, ciphertext)).toThrow('invalid tag')
    // @ts-expect-error assert that expiry dropped prev
    expect(cs.prev).toBeNull()
  })
})

describe('CipherState associated data', () => {
  it('decrypts only with the encryption associated data, without advancing on failure', () => {
    const key = new Uint8Array(32).fill(3)
    const receiver = new CipherState(key)
    const sender = new CipherState(key)
    const ad = new TextEncoder().encode('envelope')
    const ciphertext = sender.encrypt(ad, new TextEncoder().encode('payload'))
    expect(() => receiver.decrypt(new TextEncoder().encode('other'), ciphertext)).toThrow('invalid tag')
    expect(() => receiver.decrypt(EMPTY, ciphertext)).toThrow('invalid tag')
    expect(new TextDecoder().decode(receiver.decrypt(ad, ciphertext))).toBe('payload')

    const previousKey = new Uint8Array(32).fill(4)
    const rotated = new CipherState(previousKey)
    const oldSender = new CipherState(previousKey)
    rotated.rekeyWithSecret(new Uint8Array(32).fill(0xA0), null, true)
    const oldCiphertext = oldSender.encrypt(ad, new TextEncoder().encode('old epoch'))
    expect(() => rotated.decrypt(new TextEncoder().encode('other'), oldCiphertext)).toThrow('invalid tag')
    expect(() => rotated.decrypt(EMPTY, oldCiphertext)).toThrow('invalid tag')
    expect(new TextDecoder().decode(rotated.decrypt(ad, oldCiphertext))).toBe('old epoch')
  })
})
