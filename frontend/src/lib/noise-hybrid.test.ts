import type { NoiseEntropy } from './noise'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'
import { describe, expect, it, vi } from 'vitest'
import { NOISE_HANDSHAKE_SIGNATURE_LABEL, SLHDSA_SIG_SIZE } from '~/generated/contracts/wire'
import { concatBytes } from './bytes'
import { frameAssociatedData } from './channelFraming'
import {
  acceptRekeyPQ,
  clearHandshakeState,
  initiatorHandshake1WithEntropy,
  initiatorHandshake2,
  offerRekeyPQWithEntropy,
} from './noise-hybrid'
import { verifySlhDsaHere } from './slhDsaVerifyHere'

interface EntropyVector {
  x25519SeedHex?: string
  mlkemKeygenSeedHex?: string
  mlkemEncapsulationSeedHex?: string
}
interface FrameVector { correlationId: string, flags: number, plaintext: string, ciphertextHex: string }
interface HandshakeVector {
  mode: string
  workerId: string
  channelId: string
  responderPublic: { x25519Hex: string, mlkemHex: string, slhdsaHex: string }
  initiatorEntropy: EntropyVector
  message1Hex: string
  message2Hex: string
  initiatorFrame: FrameVector
  responderFrame: FrameVector
}
interface RekeyVector {
  name: string
  workerMlkemSeedHex: string
  initiatorEntropy: EntropyVector
  staticCiphertextHex: string
  ephemeralPublicKeyHex: string
  ephemeralCiphertextHex: string
  pqSecretHex: string
}

function readVectors<T>(name: string): T {
  return JSON.parse(readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), `../../../testdata/${name}`), 'utf8')) as T
}
const handshake = readVectors<{ vectors: HandshakeVector[] }>('noise_handshake_vectors.json').vectors.find(v => v.mode === 'post_quantum')!
const rekey = readVectors<{ exchanges: RekeyVector[] }>('noise_rekey_pq_vectors.json').exchanges

function fromHex(hex: string): Uint8Array {
  return Uint8Array.from(hex.match(/../g)!.map(byte => Number.parseInt(byte, 16)))
}
function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
}
function allZero(bytes: Uint8Array): boolean {
  return bytes.every(byte => byte === 0)
}
function entropy(v: EntropyVector, kept?: Uint8Array[]): NoiseEntropy {
  const draw = (hex: string | undefined): Uint8Array => {
    const bytes = fromHex(hex ?? '')
    kept?.push(bytes)
    return bytes
  }
  return {
    x25519Seed: () => draw(v.x25519SeedHex),
    mlkemKeygenSeed: () => draw(v.mlkemKeygenSeedHex),
    mlkemEncapsulationSeed: () => draw(v.mlkemEncapsulationSeedHex),
  }
}
function keysWithMlkem(key: Uint8Array) {
  return {
    x25519PublicKey: fromHex(handshake.responderPublic.x25519Hex),
    mlkemPublicKey: key,
    slhdsaPublicKey: fromHex(handshake.responderPublic.slhdsaHex),
  }
}
function vectorKeys() {
  return keysWithMlkem(fromHex(handshake.responderPublic.mlkemHex))
}
function start(kept?: Uint8Array[], workerId = handshake.workerId, keys = vectorKeys()) {
  return initiatorHandshake1WithEntropy(entropy(handshake.initiatorEntropy, kept), workerId, keys)
}
function expectStateZero(state: ReturnType<typeof start>['handshakeState']): void {
  expect(allZero(state.e.privateKey)).toBe(true)
  expect(allZero(state.mlkemEphemeralSecretKey)).toBe(true)
  expect(allZero(state.ss.h)).toBe(true)
  expect(allZero(state.ss.ck)).toBe(true)
  expect(allZero(state.ss.k)).toBe(true)
}
function expectKeptZero(kept: Uint8Array[]): void {
  expect(kept.length).toBeGreaterThan(0)
  for (const bytes of kept)
    expect(allZero(bytes)).toBe(true)
}

describe('hybrid Noise handshake', () => {
  it('matches message 1 of the Go vector and wipes its ML-KEM seeds', () => {
    const kept: Uint8Array[] = []
    const { message1, handshakeState } = start(kept)
    expect(toHex(message1)).toBe(handshake.message1Hex)
    expect(message1).toHaveLength(3216)
    expectKeptZero(kept.slice(1))
    clearHandshakeState(handshakeState)
    expectStateZero(handshakeState)
  })

  it('derives the session keys and frame authentication of the Go vector', async () => {
    const { handshakeState } = start()
    const session = await initiatorHandshake2(handshakeState, fromHex(handshake.message2Hex), verifySlhDsaHere)
    const initiatorFrame = handshake.initiatorFrame
    const initiatorAd = frameAssociatedData(handshake.channelId, BigInt(initiatorFrame.correlationId), initiatorFrame.flags)
    expect(toHex(session.send.encrypt(initiatorAd, new TextEncoder().encode(initiatorFrame.plaintext)))).toBe(initiatorFrame.ciphertextHex)
    const responderFrame = handshake.responderFrame
    const responderAd = frameAssociatedData(handshake.channelId, BigInt(responderFrame.correlationId), responderFrame.flags)
    expect(new TextDecoder().decode(session.receive.decrypt(responderAd, fromHex(responderFrame.ciphertextHex)))).toBe(responderFrame.plaintext)
    expectStateZero(handshakeState)
  })

  it('refuses another Worker id and zeroes the state', async () => {
    const { handshakeState } = start(undefined, 'worker-other')
    await expect(initiatorHandshake2(handshakeState, fromHex(handshake.message2Hex), verifySlhDsaHere)).rejects.toThrow('invalid tag')
    expectStateZero(handshakeState)
  })

  it('refuses a forged payload tag before the verifier and zeroes the state', async () => {
    const { handshakeState } = start()
    const message2 = fromHex(handshake.message2Hex)
    message2[1616]! ^= 1
    const verify = vi.fn(() => true)
    await expect(initiatorHandshake2(handshakeState, message2, verify)).rejects.toThrow('invalid tag')
    expect(verify).not.toHaveBeenCalled()
    expectStateZero(handshakeState)
  })

  it('refuses a forged signature and zeroes the state', async () => {
    const { handshakeState } = start()
    const message2 = fromHex(handshake.message2Hex)
    message2[message2.length - 1]! ^= 1
    await expect(initiatorHandshake2(handshakeState, message2, verifySlhDsaHere)).rejects.toThrow('SLH-DSA signature verification failed')
    expectStateZero(handshakeState)
  })

  it.each([16, 51489])('refuses a message 2 of %i bytes and zeroes the state', async (size) => {
    const { handshakeState } = start()
    await expect(initiatorHandshake2(handshakeState, new Uint8Array(size), verifySlhDsaHere)).rejects.toThrow('wrong size')
    expectStateZero(handshakeState)
  })

  it('refuses a low-order responder ephemeral before the verifier and zeroes the state', async () => {
    const { handshakeState } = start()
    const verify = vi.fn(() => true)
    await expect(initiatorHandshake2(handshakeState, new Uint8Array(51488), verify)).rejects.toThrow('invalid private or public key received')
    expect(verify).not.toHaveBeenCalled()
    expectStateZero(handshakeState)
  })

  it('refuses an empty Worker id', () => {
    expect(() => start(undefined, '')).toThrow('Worker id is required')
  })

  it('zeroes every seed when message 1 fails', () => {
    const kept: Uint8Array[] = []
    expect(() => start(kept, handshake.workerId, keysWithMlkem(new Uint8Array(1568).fill(0xFF)))).toThrow('wrong publicKey modulus')
    expectKeptZero(kept)
  })
})

describe('the injected signature verifier', () => {
  it('receives the signature, the domain-separated handshake hash, and the key', async () => {
    const { handshakeState } = start()
    const verify = vi.fn(verifySlhDsaHere)
    await initiatorHandshake2(handshakeState, fromHex(handshake.message2Hex), verify)
    expect(verify).toHaveBeenCalledOnce()
    const [signature, message, publicKey] = verify.mock.calls[0]!
    expect(signature).toHaveLength(SLHDSA_SIG_SIZE)
    expect(message).toHaveLength(new TextEncoder().encode(NOISE_HANDSHAKE_SIGNATURE_LABEL).length + 65)
    expect(message).toEqual(concatBytes(new TextEncoder().encode(NOISE_HANDSHAKE_SIGNATURE_LABEL), new Uint8Array([0]), message.slice(-64)))
    expect(publicKey).toEqual(fromHex(handshake.responderPublic.slhdsaHex))
    expect(verifySlhDsaHere(signature, message, publicKey)).toBe(true)
  })

  it('rejects a false answer and zeroes the state', async () => {
    const { handshakeState } = start()
    await expect(initiatorHandshake2(handshakeState, fromHex(handshake.message2Hex), () => false)).rejects.toThrow('SLH-DSA signature verification failed')
    expectStateZero(handshakeState)
  })

  it('propagates a verifier failure and zeroes the state', async () => {
    const { handshakeState } = start()
    const failure = new Error('the verifier crashed')
    const verify = () => {
      throw failure
    }
    await expect(initiatorHandshake2(handshakeState, fromHex(handshake.message2Hex), verify)).rejects.toBe(failure)
    expectStateZero(handshakeState)
  })
})

function rekeyWorkerPublicKey(vector: RekeyVector): Uint8Array {
  const seed = fromHex(vector.workerMlkemSeedHex)
  try {
    return ml_kem1024.keygen(seed).publicKey
  }
  finally {
    seed.fill(0)
  }
}

describe('post-quantum rekey exchange', () => {
  it('matches every Go rekey vector and wipes its seeds and offer', () => {
    for (const vector of rekey) {
      const kept: Uint8Array[] = []
      const offer = offerRekeyPQWithEntropy(entropy(vector.initiatorEntropy, kept), rekeyWorkerPublicKey(vector))
      // The Worker's private seed belongs to the vector, so derive its public key independently.
      expect(offer).not.toBeNull()
      expectKeptZero(kept)
      expect(toHex(offer!.staticCipherText)).toBe(vector.staticCiphertextHex)
      expect(toHex(offer!.ephemeralPublicKey)).toBe(vector.ephemeralPublicKeyHex)
      const secret = acceptRekeyPQ(offer, fromHex(vector.ephemeralCiphertextHex))
      expect(secret).not.toBeNull()
      expect(toHex(secret!)).toBe(vector.pqSecretHex)
      expect(allZero(offer!.staticSharedSecret)).toBe(true)
      expect(allZero(offer!.ephemeralSecretKey)).toBe(true)
      secret!.fill(0)
    }
  })

  it('zeroes every seed when the offer fails', () => {
    const kept: Uint8Array[] = []
    expect(() => offerRekeyPQWithEntropy(entropy(rekey[0]!.initiatorEntropy, kept), new Uint8Array(1568).fill(0xFF))).toThrow('wrong publicKey modulus')
    expectKeptZero(kept)
  })

  it('refuses a malformed ciphertext and a non-empty ciphertext with a null offer', () => {
    const offer = offerRekeyPQWithEntropy(entropy(rekey[0]!.initiatorEntropy), rekeyWorkerPublicKey(rekey[0]!))
    expect(() => acceptRekeyPQ(offer, new Uint8Array(1567))).toThrow('ciphertext')
    expect(allZero(offer!.staticCipherText)).toBe(true)
    expect(allZero(offer!.ephemeralPublicKey)).toBe(true)
    expect(allZero(offer!.staticSharedSecret)).toBe(true)
    expect(allZero(offer!.ephemeralSecretKey)).toBe(true)
    expect(() => acceptRekeyPQ(null, new Uint8Array(1))).toThrow('classic')
    expect(acceptRekeyPQ(null, new Uint8Array(0))).toBeNull()
  })
})
