import type { HandshakeState, NoiseEntropy, Session } from '~/lib/noise'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { bytesToHex, hexToBytes } from '@noble/hashes/utils.js'
import { expect } from 'vitest'
import { frameAssociatedData } from '~/lib/channelFraming'

export interface EntropyVector {
  x25519SeedHex?: string
  mlkemKeygenSeedHex?: string
  mlkemEncapsulationSeedHex?: string
}

interface FrameVector {
  correlationId: string
  flags: number
  plaintext: string
  ciphertextHex: string
}

interface HandshakeVector {
  mode: 'classic' | 'post_quantum'
  workerId: string
  channelId: string
  responderPublic: { x25519Hex: string, mlkemHex: string, slhdsaHex: string }
  initiatorEntropy: EntropyVector
  message1Hex: string
  message2Hex: string
  initiatorFrames: FrameVector[]
  responderFrames: FrameVector[]
}

export function readNoiseVectors<T>(name: string): T {
  return JSON.parse(readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), `../../../testdata/${name}`), 'utf8')) as T
}

export function handshakeVector(mode: HandshakeVector['mode']): HandshakeVector {
  const fixture = readNoiseVectors<{ vectors: HandshakeVector[] }>('noise_handshake_vectors.json')
  const vector = fixture.vectors.find(vector => vector.mode === mode)
  if (!vector)
    throw new Error(`missing Noise handshake vector for ${mode}`)
  return vector
}

export function entropy(vector: EntropyVector, kept?: Uint8Array[]): NoiseEntropy {
  const draw = (field: keyof EntropyVector): Uint8Array => {
    const hex = vector[field]
    if (hex === undefined)
      throw new Error(`Noise vector has no ${field}`)
    const bytes = hexToBytes(hex)
    kept?.push(bytes)
    return bytes
  }
  return {
    x25519Seed: () => draw('x25519SeedHex'),
    mlkemKeygenSeed: () => draw('mlkemKeygenSeedHex'),
    mlkemEncapsulationSeed: () => draw('mlkemEncapsulationSeedHex'),
  }
}

export function vectorKeys(vector: HandshakeVector, mlkem = hexToBytes(vector.responderPublic.mlkemHex)) {
  return {
    x25519PublicKey: hexToBytes(vector.responderPublic.x25519Hex),
    mlkemPublicKey: mlkem,
    slhdsaPublicKey: hexToBytes(vector.responderPublic.slhdsaHex),
  }
}

export function allZero(bytes: Uint8Array): boolean {
  return bytes.every(byte => byte === 0)
}

export function handshakeStateIsZero(state: HandshakeState): boolean {
  return [
    state.e.privateKey,
    state.ss.h,
    state.ss.ck,
    state.ss.k,
  ].every(allZero)
}

export function expectHandshakeFrames(session: Session, vector: HandshakeVector): void {
  const encoder = new TextEncoder()
  const decoder = new TextDecoder()
  try {
    for (const frame of vector.initiatorFrames) {
      const ad = frameAssociatedData(vector.channelId, BigInt(frame.correlationId), frame.flags)
      expect(bytesToHex(session.send.encrypt(ad, encoder.encode(frame.plaintext)))).toBe(frame.ciphertextHex)
    }
    for (const frame of vector.responderFrames) {
      const ad = frameAssociatedData(vector.channelId, BigInt(frame.correlationId), frame.flags)
      expect(decoder.decode(session.receive.decrypt(ad, hexToBytes(frame.ciphertextHex)))).toBe(frame.plaintext)
    }
  }
  finally {
    session.send.destroy()
    session.receive.destroy()
  }
}
