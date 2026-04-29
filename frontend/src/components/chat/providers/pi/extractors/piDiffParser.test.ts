import { describe, expect, it } from 'vitest'
import { parsePiNumberedDiff } from './piDiffParser'

describe('parsePiNumberedDiff', () => {
  it('returns empty array for empty or whitespace-only input', () => {
    expect(parsePiNumberedDiff('')).toEqual([])
    expect(parsePiNumberedDiff('   \n  ')).toEqual([])
  })

  it('parses a one-for-one substitution with surrounding context', () => {
    const diff = [
      ' 1 first line',
      '-2 old text',
      '+2 new text',
      ' 3 third line',
    ].join('\n')
    expect(parsePiNumberedDiff(diff)).toEqual([{
      oldStart: 1,
      oldLines: 3,
      newStart: 1,
      newLines: 3,
      lines: [' first line', '-old text', '+new text', ' third line'],
    }])
  })

  it('handles asymmetric substitution (1 line removed, 3 added) and tracks divergence', () => {
    // Replacing one line with three lines — old/new positions diverge by 2
    // for the rest of the file.
    const diff = [
      ' 1 a',
      ' 2 b',
      '-3 old',
      '+3 X',
      '+4 Y',
      '+5 Z',
      ' 4 c',
      ' 5 d',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 5, // a, b, old, c, d
      newStart: 1,
      newLines: 7, // a, b, X, Y, Z, c, d
      lines: [' a', ' b', '-old', '+X', '+Y', '+Z', ' c', ' d'],
    })
  })

  it('handles a pure-deletion hunk (only - lines)', () => {
    const diff = [
      ' 1 keep',
      '-2 drop me',
      '-3 drop me too',
      ' 4 keep too',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 4,
      newStart: 1,
      newLines: 2,
      lines: [' keep', '-drop me', '-drop me too', ' keep too'],
    })
  })

  it('handles a pure-addition hunk (only + lines)', () => {
    const diff = [
      ' 1 keep',
      '+2 inserted A',
      '+3 inserted B',
      ' 2 keep too',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 2,
      newStart: 1,
      newLines: 4,
      lines: [' keep', '+inserted A', '+inserted B', ' keep too'],
    })
  })

  it('handles an addition at the very top of the file (no preceding context)', () => {
    const diff = [
      '+1 brand new first',
      ' 1 existing first',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 2,
      lines: ['+brand new first', ' existing first'],
    })
  })

  it('handles a deletion at the very top of the file', () => {
    const diff = [
      '-1 was first',
      ' 2 still here',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 2,
      newStart: 1,
      newLines: 1,
      lines: ['-was first', ' still here'],
    })
  })

  it('separates hunks at skip markers and re-anchors via the next line', () => {
    const diff = [
      ' 1 first',
      '-2 removed',
      '+2 added',
      '   ...',
      ' 50 later context',
      '+51 inserted',
      ' 51 next',
    ].join('\n')
    const hunks = parsePiNumberedDiff(diff)!
    expect(hunks).toHaveLength(2)
    expect(hunks[0]).toEqual({
      oldStart: 1,
      oldLines: 2,
      newStart: 1,
      newLines: 2,
      lines: [' first', '-removed', '+added'],
    })
    expect(hunks[1]).toEqual({
      // Skip in Pi's format advances both axes by the same delta. The
      // first hunk had 1 add / 1 remove (net 0), so the second hunk
      // anchors at the same number on both axes.
      oldStart: 50,
      oldLines: 2,
      newStart: 50,
      newLines: 3,
      lines: [' later context', '+inserted', ' next'],
    })
  })

  it('preserves position divergence across a skip when the prior hunk was asymmetric', () => {
    // First hunk: 1 line replaced with 3 → new file is 2 lines longer.
    // After the skip, ` 76 ctx` is at oldPos=76, newPos=76+2=78.
    const diff = [
      ' 1 a',
      '-2 old',
      '+2 X',
      '+3 Y',
      '+4 Z',
      ' 3 b',
      '   ...',
      ' 76 later',
      '-77 also gone',
      '+79 replacement',
    ].join('\n')
    const hunks = parsePiNumberedDiff(diff)!
    expect(hunks).toHaveLength(2)
    expect(hunks[0]).toMatchObject({ oldStart: 1, newStart: 1 })
    expect(hunks[1]).toMatchObject({
      // Pi shows the old line number on context (76), but the new line
      // number is shifted by +2 from the prior hunk.
      oldStart: 76,
      newStart: 78,
      lines: [' later', '-also gone', '+replacement'],
    })
  })

  it('handles consecutive skip markers (defensive against malformed input)', () => {
    const diff = [
      ' 1 a',
      '   ...',
      '   ...',
      ' 50 later',
      '+51 added',
    ].join('\n')
    const hunks = parsePiNumberedDiff(diff)!
    expect(hunks).toHaveLength(2)
    expect(hunks[0].lines).toEqual([' a'])
    expect(hunks[1]).toMatchObject({
      oldStart: 50,
      newStart: 50,
      lines: [' later', '+added'],
    })
  })

  it('handles a leading skip marker (file starts with unchanged context)', () => {
    const diff = [
      '   ...',
      ' 100 context',
      '-101 gone',
      '+101 replacement',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 100,
      oldLines: 2,
      newStart: 100,
      newLines: 2,
      lines: [' context', '-gone', '+replacement'],
    })
  })

  it('handles padded line numbers (single-digit and three-digit widths)', () => {
    const diff = [
      '   1 small',
      '-  2 mid',
      '+  2 swap',
      ' 999 wide',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk.lines).toEqual([' small', '-mid', '+swap', ' wide'])
  })

  it('preserves trailing whitespace and content with special characters', () => {
    const diff = [
      ' 1 has trailing  ',
      '-2 with\ttab',
      '+2 with /slash and "quotes"',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk.lines).toEqual([
      ' has trailing  ',
      '-with\ttab',
      '+with /slash and "quotes"',
    ])
  })

  it('returns null when any line fails to parse (caller falls back to raw text)', () => {
    // Silently skipping unrecognized lines would corrupt line-number
    // arithmetic for everything after — better to surface that we can't
    // make sense of the input and let the caller render Pi's diff verbatim.
    const diff = [
      ' 1 valid',
      'unrecognized line shape',
      '-2 also valid',
      '+2 replacement',
    ].join('\n')
    expect(parsePiNumberedDiff(diff)).toBeNull()
  })

  it('returns null on a single malformed line at the start', () => {
    expect(parsePiNumberedDiff('this is just plain text')).toBeNull()
  })

  it('handles a trailing newline in the input', () => {
    const diff = ` 1 a\n+2 added\n`
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 2,
      lines: [' a', '+added'],
    })
  })

  it('handles multiple distinct hunks with multiple skip markers', () => {
    const diff = [
      ' 1 a',
      '-2 b',
      '+2 B',
      '   ...',
      ' 20 c',
      '+21 inserted',
      '   ...',
      ' 80 d',
      '-81 e',
    ].join('\n')
    const hunks = parsePiNumberedDiff(diff)!
    expect(hunks).toHaveLength(3)
    expect(hunks.map(h => ({ oldStart: h.oldStart, newStart: h.newStart, n: h.lines.length })))
      .toEqual([
        { oldStart: 1, newStart: 1, n: 3 },
        { oldStart: 20, newStart: 20, n: 2 },
        // After the second hunk added 1 line, new is shifted +1 from old.
        { oldStart: 80, newStart: 81, n: 2 },
      ])
  })

  it('handles the realistic edit-tool output Pi emits for a single substitution', () => {
    // Output produced by `generateDiffString` for replacing one line in a
    // 6-line file with a contextLines=4 budget — entire file fits in one
    // hunk, no skip markers.
    const diff = [
      ' 1 const a = 1',
      ' 2 const b = 2',
      '-3 const c = 3',
      '+3 const c = "three"',
      ' 4 const d = 4',
      ' 5 const e = 5',
      ' 6 const f = 6',
    ].join('\n')
    const [hunk] = parsePiNumberedDiff(diff)!
    expect(hunk).toEqual({
      oldStart: 1,
      oldLines: 6,
      newStart: 1,
      newLines: 6,
      lines: [
        ' const a = 1',
        ' const b = 2',
        '-const c = 3',
        '+const c = "three"',
        ' const d = 4',
        ' const e = 5',
        ' const f = 6',
      ],
    })
  })
})
