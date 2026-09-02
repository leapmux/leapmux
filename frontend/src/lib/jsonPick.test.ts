import { describe, expect, it } from 'vitest'
import {
  assignDefined,
  isObject,
  pickBool,
  pickBoolean,
  pickFirstNumber,
  pickFirstObject,
  pickFirstString,
  pickNumber,
  pickObject,
  pickString,
  stringArray,
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

describe('stringarray', () => {
  it('keeps only the string elements of an array', () => {
    expect(stringArray(['a', 1, 'b', null, 'c', undefined, {}])).toEqual(['a', 'b', 'c'])
  })
  it('returns an empty array for a non-array (or empty) value', () => {
    expect(stringArray(undefined)).toEqual([])
    expect(stringArray(null)).toEqual([])
    expect(stringArray('not-an-array')).toEqual([])
    expect(stringArray({ 0: 'a' })).toEqual([])
    expect(stringArray([])).toEqual([])
  })
})

describe('pickboolean', () => {
  it('returns the boolean when the value is one', () => {
    expect(pickBoolean({ b: true }, 'b')).toBe(true)
    expect(pickBoolean({ b: false }, 'b')).toBe(false)
  })

  // The whole reason it exists beside pickBool: an explicit `false` and an
  // absent key are DIFFERENT answers here, where pickBool folds both to false.
  it('separates an explicit false from an absent key, which pickBool cannot', () => {
    expect(pickBoolean({ b: false }, 'b', undefined)).toBe(false)
    expect(pickBoolean({}, 'b', undefined)).toBeUndefined()
    expect(pickBool({ b: false }, 'b')).toBe(pickBool({}, 'b'))
  })

  it('returns the fallback for a non-boolean value', () => {
    expect(pickBoolean({ b: 'true' }, 'b', undefined)).toBeUndefined()
    expect(pickBoolean({ b: 1 }, 'b', undefined)).toBeUndefined()
    expect(pickBoolean({ b: null }, 'b', undefined)).toBeUndefined()
  })

  it('defaults to null when no fallback is given', () => {
    expect(pickBoolean({}, 'b')).toBeNull()
    expect(pickBoolean(null, 'b')).toBeNull()
  })
})

describe('assignDefined', () => {
  it('assigns a defined value', () => {
    const target: { a?: number, b?: string } = {}
    assignDefined(target, 'a', 1)
    assignDefined(target, 'b', '')
    expect(target).toEqual({ a: 1, b: '' })
  })

  /**
   * Asserted on Object.keys, not with toEqual: toEqual IGNORES an
   * undefined-valued key, so it cannot tell "absent" from "present and
   * undefined" -- which is the entire distinction this helper exists for. A
   * caller whose result is later compared by key count (shallowEqual) breaks on
   * exactly that difference.
   */
  it('leaves the key ABSENT for undefined, rather than present and undefined', () => {
    const target: { a?: number } = {}
    assignDefined(target, 'a', undefined)
    expect(Object.keys(target)).toEqual([])
    expect('a' in target).toBe(false)
  })

  it('assigns a falsy value that is not undefined', () => {
    const target: { n?: number, s?: string, b?: boolean } = {}
    assignDefined(target, 'n', 0)
    assignDefined(target, 's', '')
    assignDefined(target, 'b', false)
    expect(Object.keys(target).sort()).toEqual(['b', 'n', 's'])
  })

  it('leaves an existing value alone when the new one is undefined', () => {
    const target: { a?: number } = { a: 7 }
    assignDefined(target, 'a', undefined)
    expect(target.a).toBe(7)
  })
})
