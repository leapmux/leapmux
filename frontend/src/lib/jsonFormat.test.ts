import { describe, expect, it } from 'vitest'
import { prettifyJson } from './jsonFormat'

describe('prettifyJson', () => {
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
