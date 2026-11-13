import type { DiffInput, StructuredPatchHunk } from './diffTypes'
import { diffWordsWithSpace } from 'diff'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DIFF_DRAWN_LINE_LIMIT } from './diffBuilder'
import { LINE_DIFF_MAX_EDITS, pairKey, replacedWholeHunks } from './diffCompute'
import { computeDiff, DIFF_CONTEXT_LINES, WORD_DIFF_MAX_EDITS } from './diffSearch'

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
    expect(diff.wholeReplacement).toBeNull()
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
      wholeReplacement: null,
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
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

    expect(diff.wholeReplacement).toBe('too-many-edits')
    expect(diff.hunks).toEqual(replacedWholeHunks(old, next))
    // The rewrite pairs unrelated lines, so it has no word diffs.
    expect(diff.wordDiffs.size).toBe(0)
  })

  it('adds every line of a created file with more lines than LINE_DIFF_MAX_EDITS, as the minimal diff', () => {
    const lineCount = LINE_DIFF_MAX_EDITS + 500
    const created = numberedLines(lineCount)

    const diff = computeDiff({ kind: 'texts', oldText: '', newText: created })

    expect(diff.wholeReplacement).toBeNull()
    expect(diff.hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: lineCount,
        lines: Array.from({ length: lineCount }, (_, i) => `+l${i + 1}`),
      },
    ])
  })

  it('removes every line of a deleted file with more lines than LINE_DIFF_MAX_EDITS, as the minimal diff', () => {
    const lineCount = LINE_DIFF_MAX_EDITS + 500

    const diff = computeDiff({
      kind: 'texts',
      oldText: numberedLines(lineCount),
      newText: '',
    })

    expect(diff.wholeReplacement).toBeNull()
    expect(diff.hunks[0]?.lines).toHaveLength(lineCount)
    expect(diff.hunks[0]).toMatchObject({ oldLines: lineCount, newLines: 0 })
  })

  it('keeps a minimal diff at exactly LINE_DIFF_MAX_EDITS line edits', () => {
    const lineCount = LINE_DIFF_MAX_EDITS / 2
    const old = Array.from({ length: lineCount }, (_, i) => `old${i}`).join('\n')
    const next = Array.from({ length: lineCount }, (_, i) => `new${i}`).join('\n')

    expect(computeDiff({ kind: 'texts', oldText: old, newText: next }).wholeReplacement).toBeNull()
  })

  describe('with the line edit limit of the input', () => {
    /** `count` lines indented by `indent`, between an unchanged first and last line. */
    function indentedLines(count: number, indent: string): string {
      return [
        'head',
        ...Array.from({ length: count }, (_, i) => `${indent}line ${i}`),
        'tail',
        '',
      ].join('\n')
    }

    // Each of the 600 lines changes, so the diff needs 1,200 line edits.
    const reindent = {
      kind: 'texts',
      oldText: indentedLines(600, ''),
      newText: indentedLines(600, '  '),
    } as const

    it('draws the minimal diff of a 600-line re-indent at a limit of 5,000', () => {
      const diff = computeDiff({ ...reindent, maxLineEdits: 5000 })

      expect(diff.wholeReplacement).toBeNull()
      expect(diff.hunks).toHaveLength(1)
      expect(diff.hunks[0]).toMatchObject({
        oldStart: 1,
        oldLines: 602,
        newStart: 1,
        newLines: 602,
      })
      expect(diff.hunks[0]?.lines[0]).toBe(' head')
      expect(diff.hunks[0]?.lines.at(-1)).toBe(' tail')
      expect(diff.wordDiffs.get(pairKey('line 0', '  line 0'))?.filter(part => part.added).map(part => part.value)).toEqual(['  '])
    })

    it('replaces the whole text of a 600-line re-indent at LINE_DIFF_MAX_EDITS, and when the input states no limit', () => {
      expect(computeDiff({ ...reindent, maxLineEdits: LINE_DIFF_MAX_EDITS }).wholeReplacement).toBe('too-many-edits')
      expect(computeDiff(reindent).wholeReplacement).toBe('too-many-edits')
    })

    it('keeps a minimal diff at exactly the limit, and replaces the whole text one line past it', () => {
      const rewrite = (lineCount: number) => ({
        kind: 'texts',
        oldText: Array.from({ length: lineCount }, (_, i) => `old${i}\n`).join(''),
        newText: Array.from({ length: lineCount }, (_, i) => `new${i}\n`).join(''),
        maxLineEdits: 10,
      } as const)

      expect(computeDiff(rewrite(5)).wholeReplacement).toBeNull()
      expect(computeDiff(rewrite(6)).wholeReplacement).toBe('too-many-edits')
    })
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

    expect(diff.wholeReplacement).toBeNull()
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
    expect(diff.wholeReplacement).toBeNull()
    expect([
      ...diff.wordDiffs.keys(),
    ]).toEqual([
      pairKey('let a', 'let b'),
    ])
  })

  it('computes no word diff for the paired lines that a view does not draw', () => {
    // One change block of DIFF_DRAWN_LINE_LIMIT pairs: the view draws the first half
    // of its removals and the first half of its additions (see limitDiffLines).
    const pairs = DIFF_DRAWN_LINE_LIMIT
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: pairs,
        newStart: 1,
        newLines: pairs,
        lines: [
          ...Array.from({ length: pairs }, (_, i) => `-let old${i} = ${i}`),
          ...Array.from({ length: pairs }, (_, i) => `+let new${i} = ${i}`),
        ],
      },
    ]

    const diff = computeDiff({ kind: 'hunks', hunks })

    expect(diff.wordDiffs.size).toBe(DIFF_DRAWN_LINE_LIMIT / 2)
    expect(diff.wordDiffs.has(pairKey('let old0 = 0', 'let new0 = 0'))).toBe(true)
    expect(diff.wordDiffs.has(pairKey(`let old${pairs - 1} = ${pairs - 1}`, `let new${pairs - 1} = ${pairs - 1}`))).toBe(false)
  })

  it('computes the word diff of every paired line when the input draws every line', () => {
    const pairs = DIFF_DRAWN_LINE_LIMIT
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: pairs,
        newStart: 1,
        newLines: pairs,
        lines: [
          ...Array.from({ length: pairs }, (_, i) => `-let old${i} = ${i}`),
          ...Array.from({ length: pairs }, (_, i) => `+let new${i} = ${i}`),
        ],
      },
    ]

    const diff = computeDiff({ kind: 'hunks', hunks, drawnLineLimit: Number.POSITIVE_INFINITY })

    expect(diff.wordDiffs.size).toBe(pairs)
    expect(diff.wordDiffs.has(pairKey(`let old${pairs - 1} = ${pairs - 1}`, `let new${pairs - 1} = ${pairs - 1}`))).toBe(true)
  })

  it('computes the word diffs of two texts within the line limit of the input', () => {
    const input = { kind: 'texts', oldText: 'a 1\nb 1\n', newText: 'a 2\nb 2\n' } as const

    // The limit keeps the first removal and the first addition (see limitDiffLines).
    expect([
      ...computeDiff({ ...input, drawnLineLimit: 2 }).wordDiffs.keys(),
    ]).toEqual([
      pairKey('a 1', 'a 2'),
    ])
    expect([
      ...computeDiff({ ...input, drawnLineLimit: Number.POSITIVE_INFINITY }).wordDiffs.keys(),
    ]).toEqual([
      pairKey('a 1', 'a 2'),
      pairKey('b 1', 'b 2'),
    ])
  })

  it('computes no word diff at a line limit of zero, and keeps every hunk', () => {
    const input = {
      kind: 'texts',
      oldText: 'a 1\n',
      newText: 'a 2\n',
      drawnLineLimit: 0,
    } as const

    const diff = computeDiff(input)

    expect(diff.wordDiffs.size).toBe(0)
    expect(diff.hunks).toEqual(computeDiff({ ...input, drawnLineLimit: Number.POSITIVE_INFINITY }).hunks)
  })

  it('searches the word diff of a repeated pair once, also when the search gives up', () => {
    const removed = words('a', WORD_DIFF_MAX_EDITS)
    const added = words('b', WORD_DIFF_MAX_EDITS)
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 5,
        newStart: 1,
        newLines: 5,
        lines: [
          `-${removed}`,
          `+${added}`,
          ' keep',
          `-${removed}`,
          `+${added}`,
          ' keep',
          `-${removed}`,
          `+${added}`,
        ],
      },
    ]

    const diff = computeDiff({ kind: 'hunks', hunks })

    expect(diff.wordDiffs.size).toBe(0)
    expect(vi.mocked(diffWordsWithSpace)).toHaveBeenCalledTimes(1)
  })

  it('answers an empty patch with no hunks and no word diffs', () => {
    expect(computeDiff({ kind: 'hunks', hunks: [] })).toEqual({
      hunks: [],
      wordDiffs: new Map(),
      wholeReplacement: null,
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    })
  })

  // DiffView draws the lines that the line limit of the diff keeps, which must be
  // the lines whose word diffs the diff computed.
  it.each<[string, DiffInput]>([
    [
      'two texts',
      { kind: 'texts', oldText: 'a 1\n', newText: 'a 2\n' },
    ],
    [
      'two texts that need more line edits than the limit',
      {
        kind: 'texts',
        oldText: 'a\nb\n',
        newText: 'c\nd\n',
        maxLineEdits: 1,
      },
    ],
    [
      'a created file',
      { kind: 'texts', oldText: '', newText: 'a\n' },
    ],
    [
      'a patch',
      {
        kind: 'hunks',
        hunks: [
          {
            oldStart: 1,
            oldLines: 1,
            newStart: 1,
            newLines: 1,
            lines: ['-a 1', '+a 2'],
          },
        ],
      },
    ],
  ])('carries the line limit of the input for %s, and the default when it states none', (_label, input) => {
    expect(computeDiff(input).drawnLineLimit).toBe(DIFF_DRAWN_LINE_LIMIT)
    expect(computeDiff({ ...input, drawnLineLimit: 7 }).drawnLineLimit).toBe(7)
    expect(computeDiff({ ...input, drawnLineLimit: 0 }).drawnLineLimit).toBe(0)
    expect(computeDiff({ ...input, drawnLineLimit: Number.POSITIVE_INFINITY }).drawnLineLimit).toBe(Number.POSITIVE_INFINITY)
  })
})
