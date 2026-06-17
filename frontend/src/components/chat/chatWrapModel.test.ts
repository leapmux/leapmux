import type { HeightInput } from './chatHeightEstimator'
import { describe, expect, it } from 'vitest'
import { diffWrappedRows, monoRowMetrics, proseRowMetrics, proseRowsFromLines, visualRows, wrapRowsForLine } from './chatWrapModel'

// 8px/char at 80px width -> 10 chars per visual row.
const CHAR = 8
const WIDTH = 80

/** A minimal HeightInput carrying just the content metrics proseRowMetrics reads. */
function input(metrics: Partial<Pick<HeightInput, 'textLength' | 'logicalLineCount' | 'lineLengths'>>): HeightInput {
  return { kind: 'assistant', hasSpanLines: false, ...metrics }
}

describe('chatWrapModel', () => {
  describe('wrapRowsForLine', () => {
    it('counts ceil(len*charWidth/width) rows, never below 1', () => {
      expect(wrapRowsForLine(0, CHAR, WIDTH)).toBe(1) // empty line still occupies a row
      expect(wrapRowsForLine(10, CHAR, WIDTH)).toBe(1) // exactly one row's worth
      expect(wrapRowsForLine(11, CHAR, WIDTH)).toBe(2) // one char over wraps
      expect(wrapRowsForLine(100, CHAR, WIDTH)).toBe(10)
    })

    it('floors the wrap width at charWidth*4 so a tiny/negative width cannot explode the count', () => {
      // width = max(CHAR*4=32, given). A 1px or negative width would otherwise blow up.
      expect(wrapRowsForLine(100, CHAR, 1)).toBe(Math.ceil((100 * CHAR) / 32)) // 25
      expect(wrapRowsForLine(100, CHAR, -5)).toBe(25)
    })
  })

  describe('visualRows', () => {
    it('takes the max of the hard-line count and the total-wrap count (bias up)', () => {
      expect(visualRows(100, 3, CHAR, WIDTH)).toBe(10) // wrap (10) beats 3 hard lines
      expect(visualRows(20, 5, CHAR, WIDTH)).toBe(5) // 5 hard lines beat the 2-row wrap
      expect(visualRows(0, 1, CHAR, WIDTH)).toBe(1) // never below 1
    })
  })

  describe('proseRowsFromLines', () => {
    it('sums per-line wrap rows and counts one gap per INTERIOR blank run', () => {
      expect(proseRowsFromLines([10, 0, 20], CHAR, WIDTH)).toEqual({ rows: 3, gaps: 1, codeRows: 0 }) // 1 + (gap) + 2
    })

    it('ignores leading and trailing blank runs (no margin rendered)', () => {
      expect(proseRowsFromLines([0, 10, 0], CHAR, WIDTH)).toEqual({ rows: 1, gaps: 0, codeRows: 0 })
    })

    it('floors rows at 1 for an all-blank body', () => {
      expect(proseRowsFromLines([0, 0], CHAR, WIDTH)).toEqual({ rows: 1, gaps: 0, codeRows: 0 })
    })

    it('counts a fenced code line (negative length) as ONE row, never char-wrapped', () => {
      // A 200-char code line would char-wrap to 20 prose rows; encoded negative
      // (-(200+1)) it is exactly one non-wrapping row.
      expect(proseRowsFromLines([-201], CHAR, WIDTH)).toEqual({ rows: 1, gaps: 0, codeRows: 1 })
    })

    it('counts code rows separately from wrapped prose rows', () => {
      // a 20-char prose line (2 wrapped rows) + 3 code lines (1 row each).
      expect(proseRowsFromLines([20, -5, -5, -5], CHAR, WIDTH)).toEqual({ rows: 5, gaps: 0, codeRows: 3 })
    })

    it('does not treat a code line as a blank-line gap (code is content)', () => {
      expect(proseRowsFromLines([10, -5, 10], CHAR, WIDTH)).toEqual({ rows: 3, gaps: 0, codeRows: 1 })
    })
  })

  describe('proseRowMetrics', () => {
    it('uses the precise per-line model when lineLengths is present', () => {
      expect(proseRowMetrics(input({ lineLengths: [10, 20], logicalLineCount: 2 }), CHAR, WIDTH))
        .toEqual({ rows: 3, gaps: 0, codeRows: 0 })
    })

    it('floors rows at the true hard-line count when the line array was folded (long body)', () => {
      // 5 hard lines folded to a single sample: each dropped line still renders >= 1
      // row, so rows floors at logicalLineCount - gaps (5), not the 1 the sample implies.
      expect(proseRowMetrics(input({ lineLengths: [10], logicalLineCount: 5 }), CHAR, WIDTH))
        .toEqual({ rows: 5, gaps: 0, codeRows: 0 })
    })

    it('falls back to the flat model (gaps 0) when lineLengths is absent', () => {
      expect(proseRowMetrics(input({ textLength: 100, logicalLineCount: 3 }), CHAR, WIDTH))
        .toEqual({ rows: 10, gaps: 0, codeRows: 0 })
    })

    it('carries code rows through from the per-line model', () => {
      expect(proseRowMetrics(input({ lineLengths: [10, -5, -5], logicalLineCount: 3 }), CHAR, WIDTH))
        .toEqual({ rows: 3, gaps: 0, codeRows: 2 })
    })
  })

  describe('monoRowMetrics', () => {
    it('sums each line\'s wrap independently (pre-wrap), unlike the flat visualRows max', () => {
      // two 15-char lines at 10 chars/row each wrap to 2 -> 4 rows. The flat model
      // gives max(2, ceil(30*8/80)=3) = 3, under-counting the per-line slack.
      expect(monoRowMetrics(input({ lineLengths: [15, 15], logicalLineCount: 2 }), CHAR, WIDTH)).toBe(4)
    })

    it('decodes code-encoded negatives and wraps them like any other line', () => {
      // -16 encodes a 15-char line -> ceil(15*8/80) = 2 rows (a mono body renders ``` literally).
      expect(monoRowMetrics(input({ lineLengths: [-16], logicalLineCount: 1 }), CHAR, WIDTH)).toBe(2)
    })

    it('falls back to the flat model when lineLengths is absent', () => {
      expect(monoRowMetrics(input({ textLength: 30, logicalLineCount: 2 }), CHAR, WIDTH)).toBe(3)
    })

    it('floors at the true hard-line count when the tail was folded', () => {
      expect(monoRowMetrics(input({ lineLengths: [10], logicalLineCount: 5 }), CHAR, WIDTH)).toBe(5)
    })
  })

  describe('diffWrappedRows', () => {
    it('sums each diff line\'s wrap count', () => {
      expect(diffWrappedRows([10, 20, 30], CHAR, WIDTH)).toBe(6) // 1 + 2 + 3
    })
  })
})
