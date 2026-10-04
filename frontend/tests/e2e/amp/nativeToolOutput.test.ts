import { describe, expect, it } from 'vitest'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { AMP_NATIVE_ASCII_TAIL_LENGTH, ampNativeOutputLimit } from './nativeToolOutput'

const tailLimit = AMP_NATIVE_ASCII_TAIL_LENGTH
const complete = computedNativeToolOutput({ prefix: 'AMPFULLOUTPUT', lineCount: 8000, padding: 30 }).text
const tail = complete.slice(-tailLimit)

describe('ampNativeOutputLimit', () => {
  it('retains the native tail and positive omitted-prefix count', () => {
    expect(ampNativeOutputLimit(JSON.stringify({ output: 'native tail', truncation: { prefixLinesOmitted: 100 } }), `${'prefix\n'.repeat(100)}native tail`)).toEqual({ output: 'native tail', prefixLinesOmitted: 100 })
  })

  it.each([0, -1, 1.5, '1', undefined])('rejects incomplete native truncation metadata: %j', (prefixLinesOmitted) => {
    expect(() => ampNativeOutputLimit(JSON.stringify({ output: 'tail', truncation: { prefixLinesOmitted } }), 'prefix\ntail')).toThrow('omitted-prefix')
  })

  it('reads the exact current native tail without omitted-prefix metadata', () => {
    const retained = ampNativeOutputLimit(JSON.stringify({ output: tail, exitCode: 0 }), complete)
    expect(retained).toEqual({ output: tail, exitCode: 0 })
    expect(retained).not.toHaveProperty('prefixLinesOmitted')
    expect(retained.output).toBe(complete.slice(-tailLimit))
    expect(retained.output).not.toContain(complete)
  })

  it('accepts only the exact final bytes of the controlled complete output', () => {
    for (const output of [complete.slice(0, tailLimit), tail.slice(1), `X${tail.slice(1)}`, `${tail}X`, complete])
      expect(() => ampNativeOutputLimit(JSON.stringify({ output, exitCode: 0 }), complete)).toThrow()
  })

  it('requires a complete reference that proves actual omission', () => {
    const result = JSON.stringify({ output: tail, exitCode: 0 })
    expect(() => Reflect.apply(ampNativeOutputLimit, undefined, [result])).toThrow()
    expect(() => ampNativeOutputLimit(result, '')).toThrow()
    expect(() => ampNativeOutputLimit(result, tail)).toThrow()
    expect(() => ampNativeOutputLimit(result, complete.slice(0, tailLimit))).toThrow()
  })

  it.each([undefined, null, false, '', '0', 0.5, Number.MAX_SAFE_INTEGER + 1])('rejects an absent or invalid native exit code: %j', (exitCode) => {
    expect(() => ampNativeOutputLimit(JSON.stringify({ output: tail, exitCode }), complete)).toThrow()
  })

  it.each([-1, 0, 7])('preserves the exact native exit code: %j', (exitCode) => {
    expect(ampNativeOutputLimit(JSON.stringify({ output: tail, exitCode }), complete)).toEqual({ output: tail, exitCode })
  })

  it.each([null, false, 0, '', [], {}, { output: 0, exitCode: 0 }, { output: false, exitCode: 0 }, { output: [], exitCode: 0 }].map(value => [value]))('rejects an absent or malformed native record: %j', (value) => {
    expect(() => ampNativeOutputLimit(JSON.stringify(value), complete)).toThrow()
  })

  it.each(['', '{', '{"output":"partial"', '{}\n{}'])('rejects malformed native JSON: %j', (text) => {
    expect(() => ampNativeOutputLimit(text, complete)).toThrow()
  })

  it.each([null, false, 0, 'ignored', [], {}, { prefixLinesOmitted: 0 }, { prefixLinesOmitted: -1 }, { prefixLinesOmitted: 0.5 }, { prefixLinesOmitted: '1' }, { prefixLinesOmitted: Number.MAX_SAFE_INTEGER + 1 }].map(truncation => [truncation]))('rejects malformed metadata instead of selecting the tail route: %j', (truncation) => {
    expect(() => ampNativeOutputLimit(JSON.stringify({ output: tail, exitCode: 0, truncation }), complete)).toThrow('omitted-prefix')
  })

  it('keeps a stated native count without deriving another omitted count', () => {
    const count = 17
    const metadataComplete = `${'prefix\n'.repeat(count)}${tail}`
    expect(ampNativeOutputLimit(JSON.stringify({ output: tail, exitCode: 0, truncation: { prefixLinesOmitted: count } }), metadataComplete)).toEqual({ output: tail, prefixLinesOmitted: count })
  })

  it('retains explicit native metadata with empty output', () => {
    expect(ampNativeOutputLimit(JSON.stringify({ output: '', truncation: { prefixLinesOmitted: 1 } }), 'prefix')).toEqual({ output: '', prefixLinesOmitted: 1 })
  })
})
