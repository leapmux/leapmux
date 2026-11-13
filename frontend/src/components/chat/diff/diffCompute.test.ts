import type { StructuredPatchHunk } from './diffTypes'
import { diffWordsWithSpace } from 'diff'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DIFF_DRAWN_LINE_LIMIT } from './diffBuilder'
import {
  diffWithoutSearch,
  drawnLineLimitOf,
  fallbackDiff,
  forEachPairedLine,
  LINE_DIFF_MAX_EDITS,
  maxLineEditsOf,
  pairKey,
  patchWithoutWordDiffs,
  replacedWholeHunks,
} from './diffCompute'
import { computeDiff } from './diffSearch'

// The real word diff, counted, so a test can state how many searches ran.
vi.mock('diff', async (importOriginal) => {
  const actual = await importOriginal<typeof import('diff')>()
  return {
    ...actual,
    diffWordsWithSpace: vi.fn(actual.diffWordsWithSpace),
  }
})

beforeEach(() => {
  vi.mocked(diffWordsWithSpace).mockClear()
})

describe('drawnLineLimitOf', () => {
  it('answers DIFF_DRAWN_LINE_LIMIT for an input that states no limit', () => {
    expect(drawnLineLimitOf({ kind: 'texts', oldText: 'a', newText: 'b' })).toBe(DIFF_DRAWN_LINE_LIMIT)
    expect(drawnLineLimitOf({ kind: 'hunks', hunks: [] })).toBe(DIFF_DRAWN_LINE_LIMIT)
  })

  it('answers the limit that the input states, zero and infinity included', () => {
    expect(drawnLineLimitOf({
      kind: 'texts',
      oldText: 'a',
      newText: 'b',
      drawnLineLimit: 10,
    })).toBe(10)
    expect(drawnLineLimitOf({ kind: 'hunks', hunks: [], drawnLineLimit: 0 })).toBe(0)
    expect(drawnLineLimitOf({
      kind: 'texts',
      oldText: 'a',
      newText: 'b',
      drawnLineLimit: Number.POSITIVE_INFINITY,
    })).toBe(Number.POSITIVE_INFINITY)
  })
})

describe('maxLineEditsOf', () => {
  it('answers LINE_DIFF_MAX_EDITS for an input that states no limit', () => {
    expect(maxLineEditsOf({ kind: 'texts', oldText: 'a', newText: 'b' })).toBe(LINE_DIFF_MAX_EDITS)
  })

  it('answers the limit that the input states, zero included', () => {
    expect(maxLineEditsOf({
      kind: 'texts',
      oldText: 'a',
      newText: 'b',
      maxLineEdits: 5000,
    })).toBe(5000)
    expect(maxLineEditsOf({
      kind: 'texts',
      oldText: 'a',
      newText: 'b',
      maxLineEdits: 0,
    })).toBe(0)
  })
})

describe('pairKey', () => {
  it('keeps two pairs apart when a line holds the separator', () => {
    expect(pairKey('a\u0000b', 'c')).not.toBe(pairKey('a', 'b\u0000c'))
  })

  it('keeps two pairs apart when only their split point differs', () => {
    expect(pairKey('ab', 'c')).not.toBe(pairKey('a', 'bc'))
    expect(pairKey('', 'x')).not.toBe(pairKey('x', ''))
  })
})

describe('replacedWholeHunks', () => {
  it('removes every old line and adds every new line in one hunk', () => {
    expect(replacedWholeHunks('a\nb\n', 'c\n')).toEqual([
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 1,
        lines: ['-a', '-b', '+c'],
      },
    ])
  })

  it('answers no hunk for equal texts', () => {
    expect(replacedWholeHunks('same\n', 'same\n')).toEqual([])
    expect(replacedWholeHunks('', '')).toEqual([])
  })

  it('counts no line on an empty side', () => {
    expect(replacedWholeHunks('', 'x')).toEqual([
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: 1,
        lines: ['+x'],
      },
    ])
    expect(replacedWholeHunks('x\n', '')).toEqual([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 0,
        lines: ['-x'],
      },
    ])
  })

  it('keeps an empty line in the middle of a text', () => {
    expect(replacedWholeHunks('a\n\nb', 'c')[0]?.lines).toEqual(['-a', '-', '-b', '+c'])
  })
})

describe('forEachPairedLine', () => {
  function pairsOf(lines: string[]): Array<[string, string]> {
    const pairs: Array<[string, string]> = []
    forEachPairedLine([
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: 0,
        lines,
      },
    ], (removed, added) => {
      pairs.push([removed, added])
    })
    return pairs
  }

  it('pairs the n-th removal of a run with the n-th addition that follows it', () => {
    expect(pairsOf(['-a', '-b', '-c', '+x', '+y'])).toEqual([
      ['a', 'x'],
      ['b', 'y'],
    ])
  })

  it('pairs nothing across an unchanged line', () => {
    expect(pairsOf(['-a', ' keep', '+x'])).toEqual([])
  })

  it('pairs no addition that comes before its removal', () => {
    expect(pairsOf(['+x', '-a'])).toEqual([])
  })

  it('pairs each run of a hunk on its own', () => {
    expect(pairsOf(['-a', '+x', ' keep', '-b', '+y'])).toEqual([
      ['a', 'x'],
      ['b', 'y'],
    ])
  })

  it('pairs the lines of every hunk', () => {
    const pairs: string[] = []
    forEachPairedLine([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-a', '+x'],
      },
      {
        oldStart: 9,
        oldLines: 1,
        newStart: 9,
        newLines: 1,
        lines: ['-b', '+y'],
      },
    ], (removed, added) => {
      pairs.push(`${removed}>${added}`)
    })
    expect(pairs).toEqual(['a>x', 'b>y'])
  })
})

describe('diffWithoutSearch', () => {
  it('answers undefined for a patch', () => {
    expect(diffWithoutSearch({
      kind: 'hunks',
      hunks: [
        {
          oldStart: 1,
          oldLines: 0,
          newStart: 1,
          newLines: 1,
          lines: ['+a'],
        },
      ],
    })).toBeUndefined()
  })

  it('answers undefined for two different texts that both hold lines', () => {
    expect(diffWithoutSearch({ kind: 'texts', oldText: 'a\n', newText: 'b\n' })).toBeUndefined()
  })

  it.each([
    ['a created file', '', 'a\nb\n'],
    ['a deleted file', 'a\nb\n', ''],
    ['equal texts', 'same\n', 'same\n'],
    ['two empty texts', '', ''],
  ] as const)('answers the diff that computeDiff answers for %s', (_label, oldText, newText) => {
    const input = { kind: 'texts', oldText, newText } as const

    expect(diffWithoutSearch(input)).toEqual(computeDiff(input))
  })

  it('searches no word diff', () => {
    diffWithoutSearch({ kind: 'texts', oldText: '', newText: 'a\nb\n' })

    expect(vi.mocked(diffWordsWithSpace)).not.toHaveBeenCalled()
  })
})

describe('patchWithoutWordDiffs', () => {
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-let a', '+let b'],
    },
  ]

  it('keeps the hunks of the patch, with no word diffs and the line limit of the input', () => {
    const diff = patchWithoutWordDiffs({ kind: 'hunks', hunks, drawnLineLimit: 3 })

    expect(diff.hunks).toBe(hunks)
    expect(diff.wordDiffs.size).toBe(0)
    expect(diff.wholeReplacement).toBeNull()
    expect(diff.drawnLineLimit).toBe(3)
  })

  it('draws the hunks that computeDiff draws, and searches no word diff', () => {
    const input = { kind: 'hunks', hunks } as const

    expect(patchWithoutWordDiffs(input)).toEqual({
      ...computeDiff(input),
      wordDiffs: new Map(),
    })
    vi.mocked(diffWordsWithSpace).mockClear()
    patchWithoutWordDiffs(input)
    expect(vi.mocked(diffWordsWithSpace)).not.toHaveBeenCalled()
  })
})

describe('fallbackDiff', () => {
  it('replaces the whole old text with the new one, with no word diffs', () => {
    expect(fallbackDiff({ kind: 'texts', oldText: 'a\n', newText: 'b\n' })).toEqual({
      hunks: replacedWholeHunks('a\n', 'b\n'),
      wordDiffs: new Map(),
      wholeReplacement: 'not-computed',
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    })
  })

  it('states no replacement when one side is empty, because the whole replacement is then the minimal diff', () => {
    expect(fallbackDiff({ kind: 'texts', oldText: '', newText: 'b\n' })).toEqual({
      hunks: replacedWholeHunks('', 'b\n'),
      wordDiffs: new Map(),
      wholeReplacement: null,
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    })
    expect(fallbackDiff({ kind: 'texts', oldText: 'a\n', newText: '' }).wholeReplacement).toBeNull()
  })

  it('states no replacement for equal texts', () => {
    expect(fallbackDiff({ kind: 'texts', oldText: 'a\n', newText: 'a\n' })).toEqual({
      hunks: [],
      wordDiffs: new Map(),
      wholeReplacement: null,
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    })
  })

  it('carries the line limit of the input', () => {
    expect(fallbackDiff({
      kind: 'texts',
      oldText: 'a\n',
      newText: 'b\n',
      drawnLineLimit: Number.POSITIVE_INFINITY,
    }).drawnLineLimit).toBe(Number.POSITIVE_INFINITY)
    expect(fallbackDiff({ kind: 'hunks', hunks: [], drawnLineLimit: 0 }).drawnLineLimit).toBe(0)
  })

  it('keeps the hunks of a patch, with no word diffs', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-a', '+b'],
      },
    ]

    const diff = fallbackDiff({ kind: 'hunks', hunks })

    expect(diff.hunks).toBe(hunks)
    expect(diff.wordDiffs.size).toBe(0)
    expect(diff.wholeReplacement).toBeNull()
  })
})
