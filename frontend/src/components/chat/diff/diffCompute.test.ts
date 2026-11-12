import type { StructuredPatchHunk } from './diffTypes'
import { describe, expect, it } from 'vitest'
import {
  computeDiff,
  DIFF_CONTEXT_LINES,
  fallbackDiff,
  forEachPairedLine,
  LINE_DIFF_MAX_EDITS,
  pairKey,
  replacedWholeHunks,
  WORD_DIFF_MAX_EDITS,
} from './diffCompute'

/** `count` lines `l1` to `l<count>`, each ending in a newline. */
function numberedLines(count: number): string {
  return Array.from({ length: count }, (_, i) => `l${i + 1}\n`).join('')
}

/** `text` with line `n` (1-based) of numberedLines replaced by `L<n>`. */
function withLineChanged(text: string, n: number): string {
  return text.replace(`l${n}\n`, `L${n}\n`)
}

/** `count` words that share nothing with the words of another prefix. */
function words(prefix: string, count: number): string {
  return Array.from({ length: count }, (_, i) => `${prefix}${i}`).join(' ')
}

describe('computeDiff', () => {
  it('keeps three unchanged lines around a change', () => {
    const old = numberedLines(20)
    const diff = computeDiff({
      kind: 'texts',
      oldText: old,
      newText: withLineChanged(old, 10),
    })

    expect(DIFF_CONTEXT_LINES).toBe(3)
    expect(diff.replacedWhole).toBe(false)
    expect(diff.hunks).toEqual([
      {
        oldStart: 7,
        oldLines: 7,
        newStart: 7,
        newLines: 7,
        lines: [
          ' l7',
          ' l8',
          ' l9',
          '-l10',
          '+L10',
          ' l11',
          ' l12',
          ' l13',
        ],
      },
    ])
  })

  it('joins two changes whose contexts touch into one hunk', () => {
    const old = numberedLines(30)
    // Six unchanged lines between them: three of context after the first, three before the second.
    const diff = computeDiff({
      kind: 'texts',
      oldText: old,
      newText: withLineChanged(withLineChanged(old, 10), 17),
    })

    expect(diff.hunks).toHaveLength(1)
    expect(diff.hunks[0]).toMatchObject({ oldStart: 7, oldLines: 14 })
  })

  it('splits two changes with more unchanged lines between them than both contexts hold', () => {
    const old = numberedLines(30)
    const diff = computeDiff({
      kind: 'texts',
      oldText: old,
      newText: withLineChanged(withLineChanged(old, 10), 18),
    })

    expect(diff.hunks.map(hunk => [hunk.oldStart, hunk.oldLines])).toEqual([
      [7, 7],
      [15, 7],
    ])
  })

  it('answers no hunks for equal texts', () => {
    expect(computeDiff({ kind: 'texts', oldText: 'a\nb\n', newText: 'a\nb\n' })).toEqual({
      hunks: [],
      wordDiffs: new Map(),
      replacedWhole: false,
    })
    expect(computeDiff({ kind: 'texts', oldText: '', newText: '' }).hunks).toEqual([])
  })

  it('adds every line of a created file', () => {
    expect(computeDiff({ kind: 'texts', oldText: '', newText: 'a\nb\n' }).hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: 2,
        lines: ['+a', '+b'],
      },
    ])
  })

  it('removes every line of a deleted file', () => {
    expect(computeDiff({ kind: 'texts', oldText: 'a\nb\n', newText: '' }).hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 0,
        lines: ['-a', '-b'],
      },
    ])
  })

  it('drops the markers that state a missing final newline', () => {
    const diff = computeDiff({ kind: 'texts', oldText: 'a\nb', newText: 'a\nc' })

    expect(diff.hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 2,
        lines: [' a', '-b', '+c'],
      },
    ])
  })

  it('replaces the whole text when the texts need more than LINE_DIFF_MAX_EDITS line edits', () => {
    // Every line differs, so the diff needs one removal and one addition per line.
    const lineCount = LINE_DIFF_MAX_EDITS / 2 + 1
    const old = Array.from({ length: lineCount }, (_, i) => `old${i}`).join('\n')
    const next = Array.from({ length: lineCount }, (_, i) => `new${i}`).join('\n')

    const diff = computeDiff({ kind: 'texts', oldText: old, newText: next })

    expect(diff.replacedWhole).toBe(true)
    expect(diff.hunks).toEqual(replacedWholeHunks(old, next))
    // The rewrite pairs unrelated lines, so it has no word diffs.
    expect(diff.wordDiffs.size).toBe(0)
  })

  it('keeps a minimal diff at exactly LINE_DIFF_MAX_EDITS line edits', () => {
    const lineCount = LINE_DIFF_MAX_EDITS / 2
    const old = Array.from({ length: lineCount }, (_, i) => `old${i}`).join('\n')
    const next = Array.from({ length: lineCount }, (_, i) => `new${i}`).join('\n')

    expect(computeDiff({ kind: 'texts', oldText: old, newText: next }).replacedWhole).toBe(false)
  })

  it('computes the word diff of each paired line', () => {
    const diff = computeDiff({ kind: 'texts', oldText: 'const a = 1\n', newText: 'const b = 1\n' })

    expect(diff.wordDiffs.get(pairKey('const a = 1', 'const b = 1'))).toEqual([
      expect.objectContaining({ value: 'const ', added: false, removed: false }),
      expect.objectContaining({ value: 'a', removed: true }),
      expect.objectContaining({ value: 'b', added: true }),
      expect.objectContaining({ value: ' = 1', added: false, removed: false }),
    ])
  })

  it('keeps whitespace runs as segments of their own in a word diff', () => {
    const diff = computeDiff({ kind: 'texts', oldText: '  x\n', newText: '    x\n' })

    const parts = diff.wordDiffs.get(pairKey('  x', '    x'))
    expect(parts?.filter(part => part.removed).map(part => part.value)).toEqual(['  '])
    expect(parts?.filter(part => part.added).map(part => part.value)).toEqual(['    '])
  })

  it('leaves a pair that needs more than WORD_DIFF_MAX_EDITS word edits without a word diff', () => {
    // Each word and each space between them is a token, so the pair needs far more
    // edits than the limit.
    const removed = words('a', WORD_DIFF_MAX_EDITS)
    const added = words('b', WORD_DIFF_MAX_EDITS)

    const diff = computeDiff({ kind: 'texts', oldText: `${removed}\n`, newText: `${added}\n` })

    expect(diff.replacedWhole).toBe(false)
    expect(diff.hunks[0]?.lines).toEqual([`-${removed}`, `+${added}`])
    expect(diff.wordDiffs.has(pairKey(removed, added))).toBe(false)
  })

  it('keeps the hunks of a patch and computes their word diffs', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 4,
        oldLines: 2,
        newStart: 4,
        newLines: 2,
        lines: [' keep', '-let a', '+let b'],
      },
    ]

    const diff = computeDiff({ kind: 'hunks', hunks })

    expect(diff.hunks).toBe(hunks)
    expect(diff.replacedWhole).toBe(false)
    expect([
      ...diff.wordDiffs.keys(),
    ]).toEqual([
      pairKey('let a', 'let b'),
    ])
  })

  it('answers an empty patch with no hunks and no word diffs', () => {
    expect(computeDiff({ kind: 'hunks', hunks: [] })).toEqual({
      hunks: [],
      wordDiffs: new Map(),
      replacedWhole: false,
    })
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

describe('fallbackDiff', () => {
  it('replaces the whole old text with the new one, with no word diffs', () => {
    expect(fallbackDiff({ kind: 'texts', oldText: 'a\n', newText: 'b\n' })).toEqual({
      hunks: replacedWholeHunks('a\n', 'b\n'),
      wordDiffs: new Map(),
      replacedWhole: true,
    })
  })

  it('states no replacement for equal texts', () => {
    expect(fallbackDiff({ kind: 'texts', oldText: 'a\n', newText: 'a\n' })).toEqual({
      hunks: [],
      wordDiffs: new Map(),
      replacedWhole: false,
    })
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
    expect(diff.replacedWhole).toBe(false)
  })
})
