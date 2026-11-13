import type { StructuredPatchHunk } from './diffTypes'
import { describe, expect, it } from 'vitest'
import { changeBlockAt, limitDiffLines, sameHunks } from './diffBuilder'

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

  it('keeps the first removals and the first additions of a change block that the limit cuts', () => {
    // A rewrite: every old line removed, then every new line added.
    const rewrite: StructuredPatchHunk = {
      oldStart: 1,
      oldLines: 6,
      newStart: 1,
      newLines: 6,
      lines: [
        '-o1',
        '-o2',
        '-o3',
        '-o4',
        '-o5',
        '-o6',
        '+n1',
        '+n2',
        '+n3',
        '+n4',
        '+n5',
        '+n6',
      ],
    }

    expect(limitDiffLines([rewrite], 4)).toEqual({
      hunks: [
        {
          oldStart: 1,
          oldLines: 2,
          newStart: 1,
          newLines: 2,
          lines: ['-o1', '-o2', '+n1', '+n2'],
        },
      ],
      hiddenLines: 8,
    })
  })

  it('gives the removals the part of the limit that a short addition run leaves', () => {
    const hunk: StructuredPatchHunk = {
      oldStart: 5,
      oldLines: 7,
      newStart: 5,
      newLines: 2,
      lines: [
        ' keep',
        '-o1',
        '-o2',
        '-o3',
        '-o4',
        '-o5',
        '-o6',
        '+n1',
      ],
    }

    expect(limitDiffLines([hunk], 5)).toEqual({
      hunks: [
        {
          oldStart: 5,
          oldLines: 4,
          newStart: 5,
          newLines: 2,
          lines: [' keep', '-o1', '-o2', '-o3', '+n1'],
        },
      ],
      hiddenLines: 3,
    })
  })

  it('gives the additions the part of the limit that a short removal run leaves', () => {
    const hunk: StructuredPatchHunk = {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 5,
      lines: [
        '-o1',
        '+n1',
        '+n2',
        '+n3',
        '+n4',
        '+n5',
      ],
    }

    expect(limitDiffLines([hunk], 4)).toEqual({
      hunks: [
        {
          oldStart: 1,
          oldLines: 1,
          newStart: 1,
          newLines: 3,
          lines: ['-o1', '+n1', '+n2', '+n3'],
        },
      ],
      hiddenLines: 2,
    })
  })

  it('cuts a run of additions with no removal before it like any other change', () => {
    const hunk: StructuredPatchHunk = {
      oldStart: 1,
      oldLines: 0,
      newStart: 1,
      newLines: 4,
      lines: ['+n1', '+n2', '+n3', '+n4'],
    }

    expect(limitDiffLines([hunk], 3)).toEqual({
      hunks: [
        {
          oldStart: 1,
          oldLines: 0,
          newStart: 1,
          newLines: 3,
          lines: ['+n1', '+n2', '+n3'],
        },
      ],
      hiddenLines: 1,
    })
  })
})

describe('changeBlockAt', () => {
  it('answers undefined for an unchanged line', () => {
    expect(changeBlockAt([' a'], 0)).toBeUndefined()
  })

  it('answers undefined for a marker line and for an empty line', () => {
    expect(changeBlockAt(['\\ No newline at end of file'], 0)).toBeUndefined()
    expect(changeBlockAt([''], 0)).toBeUndefined()
  })

  it('answers undefined past the last line', () => {
    expect(changeBlockAt(['-a'], 1)).toBeUndefined()
    expect(changeBlockAt([], 0)).toBeUndefined()
  })

  it('runs over the removals, then over the additions after them', () => {
    expect(changeBlockAt(['-a', '-b', '+x', ' c'], 0)).toEqual({ addedStart: 2, end: 3 })
  })

  it('starts at the given line', () => {
    expect(changeBlockAt([' keep', '-a', '+x', '+y'], 1)).toEqual({ addedStart: 2, end: 4 })
  })

  it('takes a run of additions with no removal before it as a block with no removals', () => {
    expect(changeBlockAt(['+x', '+y'], 0)).toEqual({ addedStart: 0, end: 2 })
  })

  it('takes a run of removals that ends the hunk as a block with no additions', () => {
    expect(changeBlockAt(['-a'], 0)).toEqual({ addedStart: 1, end: 1 })
  })

  it('ends a run of additions at the removal that follows it', () => {
    expect(changeBlockAt(['+x', '-a', '+y'], 0)).toEqual({ addedStart: 0, end: 1 })
  })
})

describe('sameHunks', () => {
  const hunk = (lines: string[], oldStart = 1): StructuredPatchHunk => ({
    oldStart,
    oldLines: lines.filter(line => !line.startsWith('+')).length,
    newStart: oldStart,
    newLines: lines.filter(line => !line.startsWith('-')).length,
    lines,
  })

  it('finds a copy of equal hunks the same', () => {
    expect(sameHunks([
      hunk(['-a', '+b']),
      hunk([' c'], 5),
    ], [
      hunk(['-a', '+b']),
      hunk([' c'], 5),
    ])).toBe(true)
  })

  it('finds two empty lists the same', () => {
    expect(sameHunks([], [])).toBe(true)
  })

  it('finds hunks of another line, another place or another count different', () => {
    const base = [
      hunk(['-a', '+b']),
    ]

    expect(sameHunks(base, [
      hunk(['-a', '+c']),
    ])).toBe(false)
    expect(sameHunks(base, [
      hunk(['-a', '+b'], 2),
    ])).toBe(false)
    expect(sameHunks(base, [
      hunk(['-a', '+b', '+c']),
    ])).toBe(false)
    expect(sameHunks(base, [
      ...base,
      hunk([' d'], 9),
    ])).toBe(false)
    expect(sameHunks(base, [])).toBe(false)
  })
})
