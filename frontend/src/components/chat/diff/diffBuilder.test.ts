import type { StructuredPatchHunk } from './diffTypes'
import { describe, expect, it } from 'vitest'
import { limitDiffLines } from './diffBuilder'

const first: StructuredPatchHunk = {
  oldStart: 1,
  oldLines: 3,
  newStart: 1,
  newLines: 3,
  lines: [' a', '-b', '+B', ' c'],
}

const second: StructuredPatchHunk = {
  oldStart: 20,
  oldLines: 3,
  newStart: 20,
  newLines: 2,
  lines: [' x', '-y', '-z', '+Z'],
}

describe('limitDiffLines', () => {
  it('keeps the hunks themselves when they fit', () => {
    const hunks = [first, second]

    const limited = limitDiffLines(hunks, 8)

    expect(limited.hunks).toBe(hunks)
    expect(limited.hiddenLines).toBe(0)
  })

  it('drops the hunks past the limit', () => {
    expect(limitDiffLines([first, second], 4)).toEqual({
      hunks: [first],
      hiddenLines: 4,
    })
  })

  it('cuts the hunk that the limit falls in, and counts the lines that it keeps', () => {
    expect(limitDiffLines([first, second], 6)).toEqual({
      hunks: [
        first,
        {
          oldStart: 20,
          oldLines: 2,
          newStart: 20,
          newLines: 1,
          lines: [' x', '-y'],
        },
      ],
      hiddenLines: 2,
    })
  })

  it('counts each side of a cut hunk from its own lines', () => {
    expect(limitDiffLines([first], 3)).toEqual({
      hunks: [
        {
          oldStart: 1,
          oldLines: 2,
          newStart: 1,
          newLines: 2,
          lines: [' a', '-b', '+B'],
        },
      ],
      hiddenLines: 1,
    })
  })

  it('draws nothing at a limit of zero, and hides every line', () => {
    expect(limitDiffLines([first, second], 0)).toEqual({ hunks: [], hiddenLines: 8 })
  })

  it('treats a negative limit as zero', () => {
    expect(limitDiffLines([first, second], -5)).toEqual({ hunks: [], hiddenLines: 8 })
  })

  it('keeps an empty diff empty', () => {
    expect(limitDiffLines([], 0)).toEqual({ hunks: [], hiddenLines: 0 })
  })
})
