import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'
import { beforeAll, describe, expect, it } from 'vitest'
import { SLHDSA_PUBLIC_KEY_SIZE, SLHDSA_SIG_SIZE } from '~/generated/contracts/wire'
import { verifySlhDsaHere } from './slhDsaVerifyHere'

/** One signed transcript, made once: SLH-DSA-SHAKE-256f signs slowly. */
let keys: { publicKey: Uint8Array, secretKey: Uint8Array }
let message: Uint8Array
let signature: Uint8Array

beforeAll(() => {
  keys = slh_dsa_shake_256f.keygen()
  message = new TextEncoder().encode('handshake transcript')
  signature = slh_dsa_shake_256f.sign(message, keys.secretKey)
})

describe('verifySlhDsaHere', () => {
  it('accepts a valid signature', () => {
    expect(verifySlhDsaHere(signature, message, keys.publicKey)).toBe(true)
  })

  it('rejects a tampered signature', () => {
    const tampered = new Uint8Array(signature)
    tampered[0] = (tampered[0] ?? 0) ^ 0xFF
    expect(verifySlhDsaHere(tampered, message, keys.publicKey)).toBe(false)
  })

  it('rejects a signature over another message', () => {
    expect(verifySlhDsaHere(signature, new TextEncoder().encode('another transcript'), keys.publicKey)).toBe(false)
  })

  // The verifier refuses each of these inputs before it checks anything. The
  // answer must still be a verdict, because the handshake reports every invalid
  // signature with the same error.
  describe('with an input that the verifier cannot parse', () => {
    it.each([
      [
        'a signature that is one byte short',
        () => signature.slice(0, SLHDSA_SIG_SIZE - 1),
        () => keys.publicKey,
      ],
      [
        'an empty signature',
        () => new Uint8Array(0),
        () => keys.publicKey,
      ],
      [
        'a public key of one byte',
        () => signature,
        () => new Uint8Array(1),
      ],
      [
        'a public key that is one byte too long',
        () => signature,
        () => new Uint8Array(SLHDSA_PUBLIC_KEY_SIZE + 1),
      ],
      [
        'an empty public key',
        () => signature,
        () => new Uint8Array(0),
      ],
    ])('answers false, without a throw, for %s', (_case, makeSignature, makePublicKey) => {
      expect(() => verifySlhDsaHere(makeSignature(), message, makePublicKey())).not.toThrow()
      expect(verifySlhDsaHere(makeSignature(), message, makePublicKey())).toBe(false)
    })
  })
})
