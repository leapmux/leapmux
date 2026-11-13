import type { WordDiffPart } from './diffCompute'
import type { DiffLineMarks, DiffMarkSources, StructuredPatchHunk } from './diffTypes'
import type { CachedToken } from '~/lib/tokenCache'
import { describe, expect, it } from 'vitest'
import { pairKey } from './diffCompute'
import { buildSplitRows, buildUnifiedLines, sameLineMarks, sameSplitRowMarks, splitRowMarks, unifiedLineMarks } from './diffTokenRender'

/**
 * Two hunks. The first removes two lines and adds one in their place, so after
 * it the n-th line of the old side is no longer the n-th line of the new side.
 *
 * Old side: a, b, c, d, x. New side: a, B, d, X, y.
 */
const HUNKS: StructuredPatchHunk[] = [
  {
    oldStart: 1,
    oldLines: 4,
    newStart: 1,
    newLines: 3,
    lines: [' a', '-b', '-c', '+B', ' d'],
  },
  {
    oldStart: 9,
    oldLines: 1,
    newStart: 8,
    newLines: 2,
    lines: ['-x', '+X', '+y'],
  },
]

/** One token per line of a side, which states the side and the index of its line. */
function sideTokens(side: string, count: number): CachedToken[][] {
  return Array.from({ length: count }, (_, i) => [
    { content: `${side}${i}` },
  ])
}

const WORDS_B: WordDiffPart[] = [
  { value: 'b', removed: true },
  { value: 'B', added: true },
]
const WORDS_X: WordDiffPart[] = [
  { value: 'x', removed: true },
  { value: 'X', added: true },
]

const SOURCES: DiffMarkSources = {
  oldTokens: sideTokens('old', 5),
  newTokens: sideTokens('new', 5),
  wordDiffs: new Map([
    [
      pairKey('b', 'B'),
      WORDS_B,
    ],
    [
      pairKey('x', 'X'),
      WORDS_X,
    ],
  ]),
}

/** The text of the one token of a line's marks, or null for a line drawn plain. */
function tokenText(marks: DiffLineMarks): string | null {
  return marks.tokens?.[0]?.content ?? null
}

describe('unifiedLineMarks', () => {
  it('reads each line\'s tokens from its own side, at its own line of that side, across an unbalanced block and a second hunk', () => {
    const lines = buildUnifiedLines(HUNKS)

    expect(lines.map(line => [
      line.text,
      tokenText(unifiedLineMarks(line, SOURCES)),
    ])).toEqual([
      // An unchanged line draws the tokens of the old side.
      ['a', 'old0'],
      ['b', 'old1'],
      ['c', 'old2'],
      ['B', 'new1'],
      ['d', 'old3'],
      ['x', 'old4'],
      ['X', 'new3'],
      ['y', 'new4'],
    ])
  })

  it('reads the word diff of a paired line on both of its lines, and none for a line without a pair', () => {
    const lines = buildUnifiedLines(HUNKS)

    expect(lines.map(line => [line.text, unifiedLineMarks(line, SOURCES).words])).toEqual([
      ['a', undefined],
      ['b', WORDS_B],
      ['c', undefined],
      ['B', WORDS_B],
      ['d', undefined],
      ['x', WORDS_X],
      ['X', WORDS_X],
      ['y', undefined],
    ])
  })

  it('draws a line plain when its side has no tokens, or no tokens for its line', () => {
    const lines = buildUnifiedLines(HUNKS)
    const sources: DiffMarkSources = {
      oldTokens: null,
      // The tokenizer answered fewer lines than the side holds.
      newTokens: sideTokens('new', 2),
      wordDiffs: new Map(),
    }

    expect(lines.map(line => [
      line.text,
      tokenText(unifiedLineMarks(line, sources)),
    ])).toEqual([
      ['a', null],
      ['b', null],
      ['c', null],
      ['B', 'new1'],
      ['d', null],
      ['x', null],
      ['X', null],
      ['y', null],
    ])
  })
})

describe('splitRowMarks', () => {
  it('reads the tokens of each side of a row from that side, at its own line, and draws an empty cell plain', () => {
    const rows = buildSplitRows(HUNKS)

    expect(rows.map((row) => {
      const marks = splitRowMarks(row, SOURCES)
      return [
        row.left.text,
        tokenText(marks.left),
        row.right.text,
        tokenText(marks.right),
      ]
    })).toEqual([
      ['a', 'old0', 'a', 'new0'],
      ['b', 'old1', 'B', 'new1'],
      ['c', 'old2', '', null],
      ['d', 'old3', 'd', 'new2'],
      ['x', 'old4', 'X', 'new3'],
      ['', null, 'y', 'new4'],
    ])
  })

  it('draws no word diff on the empty cell beside an unpaired line', () => {
    const rows = buildSplitRows(HUNKS)
    const unpairedRemoval = splitRowMarks(rows[2]!, SOURCES)

    expect(unpairedRemoval).toEqual({
      left: { tokens: SOURCES.oldTokens![2], words: undefined },
      right: { tokens: null, words: undefined },
    })
  })
})

describe('sameLineMarks', () => {
  const tokens: CachedToken[] = [
    { content: 'a' },
  ]

  it('finds two marks with the same tokens and the same word diff the same', () => {
    expect(sameLineMarks({ tokens, words: WORDS_B }, { tokens, words: WORDS_B })).toBe(true)
    expect(sameLineMarks({ tokens: null, words: undefined }, { tokens: null, words: undefined })).toBe(true)
  })

  it('compares the tokens and the word diff by identity, so an equal copy of either differs', () => {
    expect(sameLineMarks({ tokens, words: WORDS_B }, {
      tokens: [
        { content: 'a' },
      ],
      words: WORDS_B,
    })).toBe(false)
    expect(sameLineMarks({ tokens, words: WORDS_B }, {
      tokens,
      words: [...WORDS_B],
    })).toBe(false)
    expect(sameLineMarks({ tokens, words: undefined }, { tokens: null, words: undefined })).toBe(false)
    expect(sameLineMarks({ tokens: null, words: WORDS_B }, { tokens: null, words: undefined })).toBe(false)
  })
})

describe('sameSplitRowMarks', () => {
  const plain: DiffLineMarks = { tokens: null, words: undefined }
  const marked: DiffLineMarks = { tokens: null, words: WORDS_B }

  it('finds two rows the same only when both of their sides are', () => {
    expect(sameSplitRowMarks({ left: plain, right: marked }, { left: plain, right: marked })).toBe(true)
    expect(sameSplitRowMarks({ left: plain, right: marked }, { left: marked, right: marked })).toBe(false)
    expect(sameSplitRowMarks({ left: plain, right: marked }, { left: plain, right: plain })).toBe(false)
  })
})
