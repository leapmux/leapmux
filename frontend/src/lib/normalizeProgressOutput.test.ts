import { describe, expect, it } from 'vitest'
import { normalizedCommandBody, normalizeProgressOutput, stripLeadingBlankLines } from './normalizeProgressOutput'

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

describe('stripLeadingBlankLines', () => {
  it('trims a run of leading blank/whitespace-only lines but keeps interior + content', () => {
    expect(stripLeadingBlankLines('\n\n  \nfoo\nbar')).toBe('foo\nbar')
    // Pairs with normalizeProgressOutput: a leading bare `\r` becomes `\n`, then strips.
    expect(stripLeadingBlankLines(normalizeProgressOutput('\rfoo').text)).toBe('foo')
  })

  it('is a no-op when there is no leading blank line', () => {
    expect(stripLeadingBlankLines('foo\nbar')).toBe('foo\nbar')
    expect(stripLeadingBlankLines('')).toBe('')
    // Trailing blank lines are preserved (only LEADING ones are trimmed).
    expect(stripLeadingBlankLines('foo\n\n')).toBe('foo\n\n')
  })
})

describe('normalizedCommandBody', () => {
  it('applies normalize THEN strip in order (a leading bare CR becomes a trimmed blank)', () => {
    // The order matters: normalizeProgressOutput turns the leading `\r` into a `\n`,
    // which stripLeadingBlankLines then trims. Both the renderer (commandResult) and
    // the height estimate (claudeBashHeightFields) route through this one helper.
    const body = normalizedCommandBody('\rfoo')
    expect(body.text).toBe('foo')
    expect(body.hadCarriageReturns).toBe(true)
  })

  it('strips leading blanks and reports no CR for plain output', () => {
    const body = normalizedCommandBody('\n\n  \nfoo\nbar')
    expect(body.text).toBe('foo\nbar')
    expect(body.hadCarriageReturns).toBe(false)
  })

  it('matches the hand-composed normalize+strip the call sites previously inlined', () => {
    for (const input of ['\rfoo', 'a\rb\rc', 'plain', '\n\nx', 'a\r\nb']) {
      const norm = normalizeProgressOutput(input)
      expect(normalizedCommandBody(input)).toEqual({ text: stripLeadingBlankLines(norm.text), hadCarriageReturns: norm.hadCarriageReturns })
    }
  })
})
