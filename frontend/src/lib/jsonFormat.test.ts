import { describe, expect, it } from 'vitest'
import { FRACTURED_JSON_MAX_LENGTH, prettifyArgsJson, prettifyJson } from './jsonFormat'

describe('prettifyJson', () => {
  it('formats JSON for a narrow control without changing its values', () => {
    const input = '{"query":"answer","path":"sample.py","limit":20}'
    const formatted = prettifyJson(input, 28)
    expect(formatted.trim().split('\n').length).toBeGreaterThan(2)
    expect(formatted.trim().split('\n').every(line => line.length <= 28)).toBe(true)
    expect(JSON.parse(formatted)).toEqual(JSON.parse(input))
    expect(prettifyJson(input).trim().split('\n')).toHaveLength(1)
  })

  it('formats plain objects', () => {
    expect(prettifyJson({
      a: 1,
      b: { c: true },
    })).toContain('{\n')
    expect(prettifyJson({
      a: 1,
      b: { c: true },
    })).toContain('"a": 1')
  })

  it('formats arrays', () => {
    expect(prettifyJson([
      { a: 1 },
      { b: 2 },
    ])).toContain('"a": 1')
  })

  it('formats valid json strings', () => {
    expect(prettifyJson('{"a":1,"b":{"c":true}}')).toContain('{\n')
    expect(prettifyJson('{"a":1,"b":{"c":true}}')).toContain('"b":')
  })

  it('returns invalid json strings unchanged', () => {
    expect(prettifyJson('not json')).toBe('not json')
  })

  it('returns undefined and function values as strings', () => {
    expect(prettifyJson(undefined)).toBe('undefined')
    expect(prettifyJson(() => 1)).toContain('=>')
  })

  describe('a long text', () => {
    // `[0,0,...,0,10]` is 2n + 4 characters long. FracturedJson packs its numbers
    // into rows; the native layout puts each number on a line of its own.
    const zeros = (FRACTURED_JSON_MAX_LENGTH - 4) / 2
    const atLimit = `[${'0,'.repeat(zeros)}10]`
    const overLimit = `[${'0,'.repeat(zeros)}100]`

    it('lays out a text at the limit with FracturedJson', () => {
      expect(atLimit).toHaveLength(FRACTURED_JSON_MAX_LENGTH)
      const formatted = prettifyJson(atLimit)
      expect(formatted.trim().split('\n').length).toBeLessThan(zeros / 4)
      expect(JSON.parse(formatted)).toEqual(JSON.parse(atLimit))
    })

    it('lays out a text over the limit with the native indentation', () => {
      expect(overLimit).toHaveLength(FRACTURED_JSON_MAX_LENGTH + 1)
      expect(prettifyJson(overLimit)).toBe(JSON.stringify(JSON.parse(overLimit), null, 4))
    })

    it('lays out a value whose JSON is over the limit with the native indentation', () => {
      const items = JSON.parse(overLimit) as number[]
      const value = { items }
      expect(prettifyJson(value)).toBe(JSON.stringify(value, null, 4))
    })

    it('returns a text over the limit unchanged when it is not JSON', () => {
      const text = `not json ${'x'.repeat(FRACTURED_JSON_MAX_LENGTH)}`
      expect(prettifyJson(text)).toBe(text)
    })
  })
})

describe('prettifyArgsJson', () => {
  it('returns empty string for null and undefined', () => {
    expect(prettifyArgsJson(null)).toBe('')
    expect(prettifyArgsJson(undefined)).toBe('')
  })

  it('returns empty string for an empty object', () => {
    expect(prettifyArgsJson({})).toBe('')
  })

  it('prettifies non-empty objects', () => {
    const out = prettifyArgsJson({ a: 1 })
    expect(out).toContain('"a": 1')
  })

  it('prettifies arrays (not treated as empty)', () => {
    expect(prettifyArgsJson([1, 2, 3])).toContain('1')
    expect(prettifyArgsJson([])).toContain('[]')
  })

  it('passes through scalars to prettifyJson', () => {
    expect(prettifyArgsJson('hello')).toBe('hello')
    expect(prettifyArgsJson(42).trim()).toBe('42')
  })
})
