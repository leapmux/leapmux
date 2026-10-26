import { describe, expect, it } from 'vitest'
import { makeIdentitySession } from './identitySession'

const cipherMethods = [
  { direction: 'send', method: 'encrypt' },
  { direction: 'send', method: 'decrypt' },
  { direction: 'receive', method: 'encrypt' },
  { direction: 'receive', method: 'decrypt' },
] as const

describe('makeIdentitySession', () => {
  it.each(cipherMethods)('copies a byte view for $direction $method without sharing storage', ({ direction, method }) => {
    const session = makeIdentitySession()
    const backing = new Uint8Array([0, 1, 2, 255, 0])
    const input = backing.subarray(1, 4)
    const first = session[direction][method](input)
    const second = session[direction][method](input)

    expect(first).toEqual(new Uint8Array([1, 2, 255]))
    expect(first).not.toBe(input)
    expect(first.buffer).not.toBe(input.buffer)
    expect(first.buffer).not.toBe(second.buffer)
    expect(first.byteOffset).toBe(0)
    expect(first.byteLength).toBe(input.byteLength)

    input[0] = 9
    expect(first[0]).toBe(1)
    first[1] = 8
    expect(input[1]).toBe(2)
    expect(second).toEqual(new Uint8Array([1, 2, 255]))
  })

  it.each(cipherMethods)('allocates fresh empty storage for $direction $method', ({ direction, method }) => {
    const session = makeIdentitySession()
    const input = new Uint8Array()
    const first = session[direction][method](input)
    const second = session[direction][method](input)
    expect(first).toEqual(input)
    expect(first.buffer).not.toBe(input.buffer)
    expect(first.buffer).not.toBe(second.buffer)
  })

  it.each(cipherMethods)('preserves a large payload for $direction $method', ({ direction, method }) => {
    const input = new Uint8Array(1024 * 1024).fill(0xA5)
    input[0] = 0
    input[input.length - 1] = 255
    const output = makeIdentitySession()[direction][method](input)
    expect(output).toEqual(input)
    input.fill(0)
    expect(output[0]).toBe(0)
    expect(output[output.length - 1]).toBe(255)
    expect(output[output.length / 2]).toBe(0xA5)
  })

  it('creates independent sessions and cipher states for both directions', () => {
    const first = makeIdentitySession()
    const second = makeIdentitySession()
    expect(first).not.toBe(second)
    expect(first.send).not.toBe(first.receive)
    expect(first.send).not.toBe(second.send)
    expect(first.receive).not.toBe(second.receive)
  })

  it('keeps the nonce at zero and leaves rekey material unchanged', () => {
    const session = makeIdentitySession()
    for (const cipher of [session.send, session.receive]) {
      expect(cipher.nonce()).toBe(0)
      expect(cipher.needsRekey()).toBe(false)
      for (const pq of [null, new Uint8Array(), new Uint8Array([3, 4])]) {
        for (const retainPrevious of [false, true]) {
          const dh = new Uint8Array([1, 2])
          const beforePq = pq?.slice() ?? null
          cipher.rekeyWithSecret(dh, pq, retainPrevious)
          cipher.clearPrev()
          expect(dh).toEqual(new Uint8Array([1, 2]))
          expect(pq).toEqual(beforePq)
          expect(cipher.encrypt(new Uint8Array([0, 255]))).toEqual(new Uint8Array([0, 255]))
          expect(cipher.decrypt(new Uint8Array())).toEqual(new Uint8Array())
          expect(cipher.nonce()).toBe(0)
          expect(cipher.needsRekey()).toBe(false)
        }
      }
    }
  })
})
