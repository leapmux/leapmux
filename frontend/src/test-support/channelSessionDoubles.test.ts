import { describe, expect, it } from 'vitest'
import { X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { blankClassicHandshakeState, blankHybridHandshakeState, createTestSession } from './channelSessionDoubles'

describe('createTestSession', () => {
  it('pairs each direction of the initiator with the opposite direction of the responder', () => {
    const { initiator, responder } = createTestSession()

    const out = initiator.send.encrypt(new Uint8Array([1, 2, 3]))
    const back = responder.send.encrypt(new Uint8Array([4]))

    expect(responder.receive.decrypt(out)).toEqual(new Uint8Array([1, 2, 3]))
    expect(initiator.receive.decrypt(back)).toEqual(new Uint8Array([4]))
    expect(initiator.send.nonce()).toBe(1)
  })

  it('refuses a frame out of nonce order, as the real cipher state does', () => {
    const { initiator, responder } = createTestSession()
    initiator.send.encrypt(new Uint8Array([1]))
    const second = initiator.send.encrypt(new Uint8Array([2]))

    expect(() => responder.receive.decrypt(second)).toThrow('invalid tag')
  })

  it('zeroes only its own key in destroy, so the peer still decrypts what is on the wire', () => {
    const { initiator, responder } = createTestSession()
    const frame = initiator.send.encrypt(new Uint8Array([9]))

    initiator.send.destroy()

    expect(() => initiator.send.encrypt(new Uint8Array([1]))).toThrow('noise: cipher state destroyed')
    expect(responder.receive.decrypt(frame)).toEqual(new Uint8Array([9]))
  })

  it('refuses to decrypt after destroy', () => {
    const { initiator, responder } = createTestSession()
    const frame = responder.send.encrypt(new Uint8Array([9]))

    initiator.receive.destroy()

    expect(() => initiator.receive.decrypt(frame)).toThrow('noise: cipher state destroyed')
  })

  it('rekeys both sides of a direction into matching keys, which the side that did not rekey refuses', () => {
    const { initiator, responder } = createTestSession()
    responder.receive.decrypt(initiator.send.encrypt(new Uint8Array([1])))

    initiator.send.rekeyWithSecret(new Uint8Array(32), null, false)
    expect(initiator.send.nonce()).toBe(0)
    const rekeyed = initiator.send.encrypt(new Uint8Array([5]))
    // The same plaintext at the same nonce under the key before the rekey.
    expect(rekeyed).not.toEqual(createTestSession().initiator.send.encrypt(new Uint8Array([5])))
    // A failed decrypt leaves the nonce, so the same frame decrypts after the rekey.
    expect(() => responder.receive.decrypt(rekeyed)).toThrow('invalid tag')
    responder.receive.rekeyWithSecret(new Uint8Array(32), null, true)

    expect(responder.receive.nonce()).toBe(0)
    expect(responder.receive.decrypt(rekeyed)).toEqual(new Uint8Array([5]))
  })
})

describe('blankHybridHandshakeState', () => {
  it('holds zero secrets of the real sizes', () => {
    const state = blankHybridHandshakeState()

    expect(state.e.privateKey).toEqual(new Uint8Array(X25519_PUBLIC_KEY_SIZE))
    expect(state.mlkemSS).toEqual(new Uint8Array(32))
    expect(state.mlkemCT.byteLength).toBe(1568)
    expect(state.ss.k).toEqual(new Uint8Array(32))
    expect(() => state.ss.clear()).not.toThrow()
  })
})

describe('blankClassicHandshakeState', () => {
  it('holds zero secrets of the real sizes', () => {
    const state = blankClassicHandshakeState()

    expect(state.e.privateKey).toEqual(new Uint8Array(X25519_PUBLIC_KEY_SIZE))
    expect(state.rs.byteLength).toBe(X25519_PUBLIC_KEY_SIZE)
    expect(state.ss.ck.byteLength).toBe(64)
  })
})
