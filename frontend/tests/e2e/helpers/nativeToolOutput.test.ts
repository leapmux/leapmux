import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'
import { computedNativeToolOutput } from './nativeToolOutput'

describe('computedNativeToolOutput', () => {
  it('requires real execution to create complete middle and final markers', () => {
    const output = computedNativeToolOutput({ prefix: 'OutputFileProbe', lineCount: 5, padding: 0 })
    expect(output.source).not.toContain(output.omittedMarker)
    expect(output.source).not.toContain(output.lastMarker)
    expect(runInNewContext(`${output.source}\ncompleteOutput`, {}, { timeout: 1000 })).toBe(output.text)
    expect(output.text.split('\n')).toEqual([
      'OutputFileProbe-line-0:',
      'OutputFileProbe-line-1:',
      'OutputFileProbe-line-2:-middle-77',
      'OutputFileProbe-line-3:',
      'OutputFileProbe-line-4:',
      'OutputFileProbe-complete-42',
    ])
  })

  it('creates distinct outputs without a supplied prefix', () => {
    const first = computedNativeToolOutput()
    const second = computedNativeToolOutput()
    expect(first.text).not.toBe(second.text)
    expect(first.text.split('\n')).toHaveLength(3001)
    expect(first.text).toContain(first.omittedMarker)
  })

  it('preserves a large complete output beyond native excerpt limits', () => {
    const output = computedNativeToolOutput({ prefix: 'LargeOutputFile', lineCount: 20_000, padding: 100 })
    expect(output.text.length).toBeGreaterThan(2_000_000)
    expect(runInNewContext(`${output.source}\ncompleteOutput`, {}, { timeout: 1000 })).toBe(output.text)
    expect(output.source).not.toContain(output.omittedMarker)
  })

  it.each(['', '1prefix', 'a-b', 'a b', 'a\0b', 'a'.repeat(81)])('rejects an invalid prefix %j', (prefix) => {
    expect(() => computedNativeToolOutput({ prefix })).toThrow('prefix')
  })

  it.each([0, -1, 2, 20_001, 3.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid line count %j', (lineCount) => {
    expect(() => computedNativeToolOutput({ lineCount })).toThrow('lines')
  })

  it.each([-1, 101, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects invalid padding %j', (padding) => {
    expect(() => computedNativeToolOutput({ padding })).toThrow('padding')
  })

  it('accepts the smallest complete output with one distinct middle line', () => {
    const output = computedNativeToolOutput({ prefix: 'A', lineCount: 3, padding: 0 })
    expect(output.firstMarker).toBe('A-line-0:')
    expect(output.omittedMarker).toBe('A-line-1:-middle-77')
    expect(output.lastMarker).toBe('A-complete-42')
    expect(output.text).toBe('A-line-0:\nA-line-1:-middle-77\nA-line-2:\nA-complete-42')
  })
})
