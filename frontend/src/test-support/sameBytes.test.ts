import { describe, expect, it } from 'vitest'
import { expectSameBytes } from '~/test-support/sameBytes'

describe('expectSameBytes', () => {
  it('accepts the same bytes in another array', () => {
    expect(() => expectSameBytes(new Uint8Array([1, 2, 3]), new Uint8Array([1, 2, 3]))).not.toThrow()
  })

  it('accepts two empty arrays', () => {
    expect(() => expectSameBytes(new Uint8Array(), new Uint8Array())).not.toThrow()
  })

  it('compares the bytes of a view, not the whole buffer under it', () => {
    const buffer = new Uint8Array([9, 1, 2, 3, 9]).buffer
    expect(() => expectSameBytes(new Uint8Array(buffer, 1, 3), new Uint8Array([1, 2, 3]))).not.toThrow()
    expect(() => expectSameBytes(new Uint8Array(buffer, 1, 3), new Uint8Array([9, 1, 2, 3, 9])))
      .toThrow('the bytes differ: 3 bytes against 5 expected, and the first difference is at index 0.')
  })

  it('states the first index that differs', () => {
    expect(() => expectSameBytes(new Uint8Array([1, 2, 3, 4]), new Uint8Array([1, 2, 0, 4]), 'the supplemental bytes'))
      .toThrow('the supplemental bytes differ: 4 bytes against 4 expected, and the first difference is at index 2.')
  })

  it('states the end of the shorter array when one array is a prefix of the other', () => {
    expect(() => expectSameBytes(new Uint8Array([1, 2]), new Uint8Array([1, 2, 3])))
      .toThrow('the bytes differ: 2 bytes against 3 expected, and the first difference is at index 2.')
    expect(() => expectSameBytes(new Uint8Array([1, 2, 3]), new Uint8Array()))
      .toThrow('the bytes differ: 3 bytes against 0 expected, and the first difference is at index 0.')
  })

  it('finds a difference in the last byte of half a megabyte', () => {
    const expected = new Uint8Array(512 * 1024).map((_, index) => index % 251)
    const actual = expected.slice()
    expect(() => expectSameBytes(actual, expected)).not.toThrow()
    actual[actual.length - 1] = 255
    expect(() => expectSameBytes(actual, expected)).toThrow(`first difference is at index ${expected.length - 1}.`)
  })
})
