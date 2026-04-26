import { describe, expect, it } from 'vitest'
import {
  isObject,
  pickBool,
  pickFirstNumber,
  pickFirstObject,
  pickFirstString,
  pickNumber,
  pickObject,
  pickString,
} from './jsonPick'

describe('isobject', () => {
  it('accepts plain objects', () => {
    expect(isObject({})).toBe(true)
    expect(isObject({ a: 1 })).toBe(true)
  })
  it('rejects arrays, null, primitives', () => {
    expect(isObject([])).toBe(false)
    expect(isObject(null)).toBe(false)
    expect(isObject(undefined)).toBe(false)
    expect(isObject('s')).toBe(false)
    expect(isObject(0)).toBe(false)
  })
})

describe('pickstring', () => {
  it('returns the string value when present', () => {
    expect(pickString({ a: 'x' }, 'a')).toBe('x')
  })
  it('returns empty string by default when missing or wrong type', () => {
    expect(pickString({}, 'a')).toBe('')
    expect(pickString({ a: 1 }, 'a')).toBe('')
    expect(pickString(null, 'a')).toBe('')
  })
  it('returns explicit fallback when provided', () => {
    expect(pickString({}, 'a', undefined)).toBeUndefined()
    expect(pickString({ a: 1 }, 'a', 'fallback')).toBe('fallback')
  })
})

describe('picknumber', () => {
  it('returns the number value when present', () => {
    expect(pickNumber({ n: 42 }, 'n')).toBe(42)
  })
  it('returns null by default when missing or wrong type', () => {
    expect(pickNumber({}, 'n')).toBeNull()
    expect(pickNumber({ n: '42' }, 'n')).toBeNull()
  })
  it('returns explicit fallback when provided', () => {
    expect(pickNumber({}, 'n', 0)).toBe(0)
    expect(pickNumber({ n: 'x' }, 'n', undefined)).toBeUndefined()
  })
})

describe('pickbool', () => {
  it('returns true only for the strict boolean true', () => {
    expect(pickBool({ b: true }, 'b')).toBe(true)
  })
  it('returns false for everything else', () => {
    expect(pickBool({ b: false }, 'b')).toBe(false)
    expect(pickBool({ b: 'true' }, 'b')).toBe(false)
    expect(pickBool({ b: 1 }, 'b')).toBe(false)
    expect(pickBool({}, 'b')).toBe(false)
  })
})

describe('pickobject', () => {
  it('returns the nested object when present', () => {
    expect(pickObject({ o: { a: 1 } }, 'o')).toEqual({ a: 1 })
  })
  it('rejects arrays', () => {
    expect(pickObject({ o: [1, 2] }, 'o')).toBeNull()
  })
  it('returns null by default when missing or wrong type', () => {
    expect(pickObject({}, 'o')).toBeNull()
    expect(pickObject(null, 'o')).toBeNull()
    expect(pickObject({ o: 'x' }, 'o')).toBeNull()
  })
  it('returns explicit fallback when provided', () => {
    expect(pickObject({}, 'o', undefined)).toBeUndefined()
    expect(pickObject({}, 'o', { default: true })).toEqual({ default: true })
  })
})

describe('pickfirststring', () => {
  it('returns the first matching string from key candidates', () => {
    expect(pickFirstString({ b: 'second', a: 'first' }, ['a', 'b'])).toBe('first')
    expect(pickFirstString({ b: 'second' }, ['a', 'b'])).toBe('second')
  })
  it('returns undefined when no candidate matches', () => {
    expect(pickFirstString({ a: 1 }, ['a', 'b'])).toBeUndefined()
    expect(pickFirstString(null, ['a'])).toBeUndefined()
  })
})

describe('pickfirstnumber', () => {
  it('returns the first matching number from key candidates', () => {
    expect(pickFirstNumber({ b: 2, a: 1 }, ['a', 'b'])).toBe(1)
    expect(pickFirstNumber({ b: 2 }, ['a', 'b'])).toBe(2)
  })
  it('returns undefined when no candidate matches', () => {
    expect(pickFirstNumber({ a: 'x' }, ['a', 'b'])).toBeUndefined()
    expect(pickFirstNumber(null, ['a'])).toBeUndefined()
  })
})

describe('pickfirstobject', () => {
  it('returns the first matching object from key candidates', () => {
    expect(pickFirstObject({ b: { v: 2 }, a: { v: 1 } }, ['a', 'b'])).toEqual({ v: 1 })
    expect(pickFirstObject({ b: { v: 2 } }, ['a', 'b'])).toEqual({ v: 2 })
  })
  it('skips non-object candidates and falls through', () => {
    expect(pickFirstObject({ a: 'x', b: { v: 2 } }, ['a', 'b'])).toEqual({ v: 2 })
    expect(pickFirstObject({ a: [1], b: { v: 2 } }, ['a', 'b'])).toEqual({ v: 2 })
  })
  it('returns undefined when no candidate matches', () => {
    expect(pickFirstObject({ a: 'x' }, ['a', 'b'])).toBeUndefined()
    expect(pickFirstObject(null, ['a'])).toBeUndefined()
  })
})
