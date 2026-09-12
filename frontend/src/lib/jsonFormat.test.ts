import { describe, expect, it } from 'vitest'
import { prettifyArgsJson, prettifyJson } from './jsonFormat'

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
    expect(prettifyJson({ a: 1, b: { c: true } })).toContain('{\n')
    expect(prettifyJson({ a: 1, b: { c: true } })).toContain('"a": 1')
  })

  it('formats arrays', () => {
    expect(prettifyJson([{ a: 1 }, { b: 2 }])).toContain('"a": 1')
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
