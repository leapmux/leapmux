import { create } from '@bufbuild/protobuf'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { x25519 } from '@noble/curves/ed25519.js'
import { ml_kem1024 } from '@noble/post-quantum/ml-kem.js'
import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'
import { describe, expect, it } from 'vitest'
import { AEAD_TAG_SIZE, MLKEM_PUBLIC_KEY_SIZE, SLHDSA_PUBLIC_KEY_SIZE, SLHDSA_SIG_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { WorkerPublicKeysSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { workerKeyBundleFromWire } from './workerKeyBundle'

// The handshake runs on these libraries, so a size that differed from theirs
// would refuse every valid worker or admit a key the handshake cannot use.
describe('worker public key sizes', () => {
  it('match the key sizes of the handshake libraries', () => {
    expect(X25519_PUBLIC_KEY_SIZE).toBe(x25519.lengths.publicKey)
    expect(MLKEM_PUBLIC_KEY_SIZE).toBe(ml_kem1024.lengths.publicKey)
    expect(SLHDSA_PUBLIC_KEY_SIZE).toBe(slh_dsa_shake_256f.lengths.publicKey)
  })
})

describe('Noise message sizes', () => {
  it('match the sizes of the handshake libraries', () => {
    expect(SLHDSA_SIG_SIZE).toBe(slh_dsa_shake_256f.lengths.signature)
    expect(AEAD_TAG_SIZE).toBe(chacha20poly1305.tagLength)
  })
})

describe('workerKeyBundleFromWire', () => {
  const keys = () => create(WorkerPublicKeysSchema, {
    x25519: new Uint8Array(X25519_PUBLIC_KEY_SIZE).fill(1),
    mlkem: new Uint8Array(MLKEM_PUBLIC_KEY_SIZE).fill(2),
    slhdsa: new Uint8Array(SLHDSA_PUBLIC_KEY_SIZE).fill(3),
  })

  it('returns the keys of a well-formed key set unchanged', () => {
    const wire = keys()

    const bundle = workerKeyBundleFromWire('w-1', wire)

    expect(bundle.x25519PublicKey).toBe(wire.x25519)
    expect(bundle.mlkemPublicKey).toBe(wire.mlkem)
    expect(bundle.slhdsaPublicKey).toBe(wire.slhdsa)
  })

  it('refuses an absent key set with a ChannelError that is not a dropped link', () => {
    let refusal: unknown
    try {
      workerKeyBundleFromWire('w-1', undefined)
    }
    catch (err) {
      refusal = err
    }

    expect(refusal).toBeInstanceOf(ChannelError)
    expect((refusal as ChannelError).source).toBe('transport')
    expect((refusal as ChannelError).disconnected).toBe(false)
    expect((refusal as ChannelError).message).toBe('open channel: the hub sent no public keys for worker w-1')
  })

  it('refuses the first key of the wrong size in a set with several', () => {
    expect(() => workerKeyBundleFromWire('w-1', {
      ...keys(),
      x25519: new Uint8Array(1),
      slhdsa: new Uint8Array(1),
    })).toThrow('open channel: the X25519 public key that the hub sent for worker w-1 is 1 bytes, not 32')
  })
})
