import { describe, expect, it } from 'vitest'
import { normalizeProgressOutput } from './normalizeProgressOutput'

describe('normalizeProgressOutput', () => {
  it('returns input unchanged when no carriage returns are present', () => {
    const input = 'line1\nline2\nline3'
    const result = normalizeProgressOutput(input)
    expect(result.hadCarriageReturns).toBe(false)
    expect(result.text).toBe(input)
    // Returned by reference so caller memos can short-circuit cheaply.
    expect(result.text).toBe(input)
  })

  it('treats CRLF as a regular newline without splitting on the bare carriage return', () => {
    const input = 'line1\r\nline2\r\nline3'
    const result = normalizeProgressOutput(input)
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe('line1\nline2\nline3')
  })

  it('splits a single bare carriage return between two segments into two lines', () => {
    const result = normalizeProgressOutput('foo\rbar')
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe('foo\nbar')
  })

  it('shows all 7 segments when the run length equals HEAD + 1 + TAIL', () => {
    const segments = ['a', 'b', 'c', 'd', 'e', 'f', 'g']
    const result = normalizeProgressOutput(segments.join('\r'))
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe(segments.join('\n'))
    expect(result.text.includes('…')).toBe(false)
  })

  it('collapses 8 segments to head 3 + ellipsis + tail 3', () => {
    const segments = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']
    const result = normalizeProgressOutput(segments.join('\r'))
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe('a\nb\nc\n…\nf\ng\nh')
  })

  it('collapses the worked rebase example to head/ellipsis/tail with the trailing message intact', () => {
    const progress = [
      'Rebasing (1/10)',
      'Rebasing (2/10)',
      'Rebasing (3/10)',
      'Rebasing (4/10)',
      'Rebasing (5/10)',
      'Rebasing (6/10)',
      'Rebasing (7/10)',
      'Rebasing (8/10)',
      'Rebasing (9/10)',
      'Rebasing (10/10)',
      'Successfully rebased and updated refs/heads/grid-layout.',
    ].join('\r')
    const result = normalizeProgressOutput(progress)
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe([
      'Rebasing (1/10)',
      'Rebasing (2/10)',
      'Rebasing (3/10)',
      '…',
      'Rebasing (9/10)',
      'Rebasing (10/10)',
      'Successfully rebased and updated refs/heads/grid-layout.',
    ].join('\n'))
  })

  it('only collapses the carriage-return-bearing newline group, leaving other groups verbatim', () => {
    const input = [
      'pre line a',
      'pre line b',
      ['p1', 'p2', 'p3', 'p4', 'p5', 'p6', 'p7', 'p8', 'p9'].join('\r'),
      'post line a',
      'post line b',
    ].join('\n')
    const result = normalizeProgressOutput(input)
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe([
      'pre line a',
      'pre line b',
      'p1',
      'p2',
      'p3',
      '…',
      'p7',
      'p8',
      'p9',
      'post line a',
      'post line b',
    ].join('\n'))
  })

  it('leaves a leading blank for a leading bare carriage return (caller strips it)', () => {
    const result = normalizeProgressOutput('\rfoo')
    expect(result.hadCarriageReturns).toBe(true)
    expect(result.text).toBe('\nfoo')
  })
})
