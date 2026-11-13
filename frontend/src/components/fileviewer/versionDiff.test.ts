import type { ComparedVersions, VersionRead } from './versionDiff'
import { describe, expect, it } from 'vitest'
import { computeDiff } from '~/components/chat/diff/diffSearch'
import { ABSENT_VERSION, comparableText, compareVersions, shownVersionDiff, VERSION_DIFF_MAX_EDITS } from './versionDiff'

function read(text: string, truncated: boolean): VersionRead {
  return {
    bytes: new TextEncoder().encode(text),
    truncated,
  }
}

/** Lines `row <from>` to `row <to - 1>`, each with its newline. */
function rows(from: number, to: number): string {
  return Array.from({ length: to - from }, (_, i) => `row ${from + i}\n`).join('')
}

/** Compare the two texts, each cut or whole, and answer what the view draws. */
function shown(oldText: string, oldTruncated: boolean, newText: string, newTruncated: boolean) {
  const versions: ComparedVersions = {
    input: { kind: 'texts', oldText, newText },
    oldTruncated,
    newTruncated,
  }
  const computed = computeDiff(versions.input)
  return {
    computed,
    versions,
    result: shownVersionDiff(computed, versions),
  }
}

describe('comparableText', () => {
  it('decodes the whole text of a version that the limit did not cut', () => {
    expect(comparableText(read('a\nb', false))).toBe('a\nb')
  })

  it('keeps only the complete lines of a cut version', () => {
    expect(comparableText(read('a\nb\nhalf a li', true))).toBe('a\nb\n')
  })

  it('keeps a cut version that ends with a newline whole', () => {
    expect(comparableText(read('a\nb\n', true))).toBe('a\nb\n')
  })

  it('drops a multi-byte character that the cut splits in the last line', () => {
    const bytes = new TextEncoder().encode('a\nüü')
    const cut: VersionRead = {
      bytes: bytes.subarray(0, bytes.length - 1),
      truncated: true,
    }

    expect(comparableText(cut)).toBe('a\n')
  })

  it('keeps the read part of a version that is one line longer than the read', () => {
    expect(comparableText(read('a long line that the read cut', true))).toBe('a long line that the read cut')
  })

  it('drops a character that the cut splits in a version of one line', () => {
    const bytes = new TextEncoder().encode('xü')

    expect(comparableText({
      bytes: bytes.subarray(0, bytes.length - 1),
      truncated: true,
    })).toBe('x')
  })

  it('answers the empty text for an empty cut read', () => {
    expect(comparableText({
      bytes: new Uint8Array(),
      truncated: true,
    })).toBe('')
  })

  it('answers the empty text for an absent version', () => {
    expect(comparableText(ABSENT_VERSION)).toBe('')
  })
})

describe('compareVersions', () => {
  // The viewer mounts one copy of the diff, so the input draws every line, and it
  // looks for the minimal diff past the line edit limit of a chat row.
  it('compares the comparable texts at the limits of the viewer, and keeps whether each version is cut', () => {
    expect(compareVersions(read('a\nb\nc', true), read('a\nB\n', false))).toEqual({
      input: {
        kind: 'texts',
        oldText: 'a\nb\n',
        newText: 'a\nB\n',
        drawnLineLimit: Number.POSITIVE_INFINITY,
        maxLineEdits: VERSION_DIFF_MAX_EDITS,
      },
      oldTruncated: true,
      newTruncated: false,
    })
  })
})

describe('shownVersionDiff', () => {
  it('draws the diff of two whole versions as it is, with no stop', () => {
    const { computed, result } = shown('a\nb\n', false, 'a\nb\nc\n', false)

    expect(result).toEqual({ diff: computed, originalFile: 'a\nb\n', stopsAtCut: false })
    expect(result.diff).toBe(computed)
  })

  it('keeps every change of a cut diff that ends with unchanged lines', () => {
    const { computed, result } = shown(rows(0, 20), true, rows(0, 20).replace('row 5\n', 'row five\n'), true)

    expect(result.diff).toBe(computed)
    expect(result.stopsAtCut).toBe(true)
  })

  it('drops the removals at the end that the cut of the new version makes up', () => {
    // 3 lines inserted at the top push the last 3 old lines past the new read.
    const { result } = shown(rows(0, 20), true, `x\ny\nz\n${rows(0, 17)}`, true)

    expect(result.diff.hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 3,
        newStart: 1,
        newLines: 6,
        lines: [
          '+x',
          '+y',
          '+z',
          ' row 0',
          ' row 1',
          ' row 2',
        ],
      },
    ])
    expect(result.originalFile).toBe(rows(0, 17))
    expect(result.stopsAtCut).toBe(true)
  })

  it('drops the additions at the end that the cut of the old version makes up', () => {
    // 3 lines deleted at the top pull 3 new lines in past the old read.
    const { result } = shown(rows(0, 20), true, rows(3, 23), true)

    expect(result.diff.hunks).toEqual([
      {
        oldStart: 1,
        oldLines: 6,
        newStart: 1,
        newLines: 3,
        lines: [
          '-row 0',
          '-row 1',
          '-row 2',
          ' row 3',
          ' row 4',
          ' row 5',
        ],
      },
    ])
    expect(result.originalFile).toBe(rows(0, 20))
  })

  it('drops a final change of both cut ends, and a hunk that it leaves with no change', () => {
    // The two reads end inside different lines, which the cut made complete.
    const { result } = shown(`${rows(0, 10)}row 10 of a long\n`, true, `${rows(0, 10)}row 10 of a\n`, true)

    expect(result.diff.hunks).toEqual([])
    expect(result.originalFile).toBe(rows(0, 10))
    expect(result.stopsAtCut).toBe(true)
  })

  it('keeps a hunk with an earlier change, and drops only the made-up lines of the final block', () => {
    const oldText = rows(0, 8)
    const newText = rows(0, 8).replace('row 4\n', 'row four\n').replace('row 7\n', '')
    const { result } = shown(oldText, false, newText, true)

    expect(result.diff.hunks).toEqual([
      {
        oldStart: 2,
        oldLines: 6,
        newStart: 2,
        newLines: 6,
        lines: [
          ' row 1',
          ' row 2',
          ' row 3',
          '-row 4',
          '+row four',
          ' row 5',
          ' row 6',
        ],
      },
    ])
    expect(result.originalFile).toBe(rows(0, 7))
  })

  it('keeps the removals at the end when only the old version is cut', () => {
    // The new version is whole, so the old lines past its end are really gone.
    const { computed, result } = shown(rows(0, 20), true, rows(0, 15), false)

    expect(result.diff).toBe(computed)
    expect(result.diff.hunks.at(-1)?.lines.filter(line => line.startsWith('-'))).toHaveLength(5)
  })

  it('keeps the additions at the end when only the new version is cut', () => {
    const { computed, result } = shown(rows(0, 15), false, rows(0, 20), true)

    expect(result.diff).toBe(computed)
  })

  it('keeps a final block that no unchanged line comes before', () => {
    // A file of one line longer than the read: the whole diff is one block.
    const { computed, result } = shown('a long line', true, 'b long line', true)

    expect(result.diff).toBe(computed)
    expect(result.diff.hunks[0]?.lines).toEqual(['-a long line', '+b long line'])
    expect(result.stopsAtCut).toBe(true)
  })

  it('keeps a whole replacement, whose notice states that it pairs no line', () => {
    const versions: ComparedVersions = {
      input: {
        kind: 'texts',
        oldText: rows(0, 5),
        newText: rows(5, 10),
      },
      oldTruncated: true,
      newTruncated: true,
    }
    const computed = {
      hunks: [
        {
          oldStart: 1,
          oldLines: 5,
          newStart: 1,
          newLines: 5,
          lines: [
            ...rows(0, 5).trim().split('\n').map(line => `-${line}`),
            ...rows(5, 10).trim().split('\n').map(line => `+${line}`),
          ],
        },
      ],
      wordDiffs: new Map(),
      wholeReplacement: 'too-many-edits' as const,
      drawnLineLimit: Number.POSITIVE_INFINITY,
    }

    expect(shownVersionDiff(computed, versions)).toEqual({
      diff: computed,
      originalFile: rows(0, 5),
      stopsAtCut: true,
    })
  })

  it('stops a diff of equal cut versions with no hunk', () => {
    const { result } = shown(rows(0, 5), true, rows(0, 5), true)

    expect(result.diff.hunks).toEqual([])
    expect(result.stopsAtCut).toBe(true)
  })

  it('keeps the word diffs of the lines that it keeps', () => {
    const { computed, result } = shown(`${rows(0, 5)}row 5 old text\n${rows(6, 12)}`, false, `${rows(0, 5)}row 5 new text\n${rows(6, 11)}`, true)

    expect(result.diff.wordDiffs).toBe(computed.wordDiffs)
    expect(result.diff.hunks.flatMap(hunk => hunk.lines)).toContain('+row 5 new text')
    expect(result.diff.hunks.flatMap(hunk => hunk.lines)).not.toContain('-row 11')
  })
})
