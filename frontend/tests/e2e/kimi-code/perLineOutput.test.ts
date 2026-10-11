import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'
import { computedKimiPerLineOutputFileOutput } from './perLineOutput'

describe('computedKimiPerLineOutputFileOutput', () => {
  it('creates complete bytes only through real execution and exceeds the native total limit', () => {
    const output = computedKimiPerLineOutputFileOutput()
    expect(runInNewContext(`${output.source}\ncompleteOutput`, {}, { timeout: 1000 })).toBe(output.text)
    expect(output.text.length).toBeGreaterThan(50_000)
    expect(output.source).not.toContain(output.omittedMarker)
    expect(output.source).not.toContain(output.lastMarker)
    const lines = output.text.split('\n')
    expect(lines).toHaveLength(3)
    expect(lines[0]?.startsWith(output.firstMarker)).toBe(true)
    expect(lines[1]?.indexOf(output.omittedMarker)).toBe(30_000)
    expect(lines[2]).toBe(output.lastMarker)
  })

  it('keeps the middle marker beyond the native per-line preview limit', () => {
    const output = computedKimiPerLineOutputFileOutput()
    const middleLine = output.text.split('\n')[1]
    if (!middleLine)
      throw new Error('The complete per-line full tool output lacks its middle line.')
    expect(middleLine.slice(0, 2000)).not.toContain(output.omittedMarker)
    expect(middleLine.endsWith(output.omittedMarker)).toBe(true)
  })

  it('isolates each output with distinct complete markers', () => {
    const first = computedKimiPerLineOutputFileOutput()
    const second = computedKimiPerLineOutputFileOutput()
    expect(first.text).not.toBe(second.text)
    expect(second.text).not.toContain(first.firstMarker)
    expect(second.text).not.toContain(first.omittedMarker)
    expect(second.text).not.toContain(first.lastMarker)
  })
})
