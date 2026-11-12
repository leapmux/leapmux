import type { StructuredPatchHunk } from '../diff'
import type { FileEditBase, FileEditContent, FileEditDiff } from '../model/fileEditDiff'
import type { RenderReadinessScope } from '../renderReadiness'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fileEditDrawnHunks } from '~/test-support/fileEditHunks'
import { holdComputedDiffs, resetHeldComputedDiffs } from '~/test-support/heldComputedDiffs'
import { computeDiff, replacedWholeHunks } from '../diff/diffCompute'
import { fileEditContent, fileEditDiffFromHunks, fileEditDiffFromWholeFile, fileEditHasDiff, normalizeStructuredPatchHunks, pickFileEditDiff } from '../model/fileEditDiff'
import { createRenderReadinessScope } from '../renderReadiness'
import { fileEditCopyHunks, FileEditDiffBody, FileEditDiffTitle } from './fileEditDiff'

const tokenizeAsyncMock = vi.hoisted(() => vi.fn(async (_lang: string, code: string) =>
  code.split('\n').map(line => [
    { content: line, htmlStyle: {} },
  ]),
))

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: tokenizeAsyncMock,
}))

vi.mock('~/components/chat/diff/diffWorkerClient', async () => (await import('~/test-support/heldComputedDiffs')).heldDiffWorkerClient())

afterEach(() => {
  resetHeldComputedDiffs()
})

const PATCH: StructuredPatchHunk[] = [
  {
    oldStart: 1,
    oldLines: 1,
    newStart: 1,
    newLines: 1,
    lines: ['-old', '+new'],
  },
]

/**
 * One source, with either half overridden.
 *
 * A `structuredPatch` override REPLACES the two sides, because `FileEditContent` is a
 * union: a source carries the patch or the sides, never both.
 */
function source(over: Partial<FileEditBase> & Partial<FileEditContent> = {}): FileEditDiff {
  const { structuredPatch, oldStr, newStr, ...facts } = over
  if (structuredPatch !== undefined && structuredPatch !== null)
    return { filePath: 'a.ts', ...facts, structuredPatch }
  return {
    filePath: 'a.ts',
    oldStr: oldStr ?? '',
    newStr: newStr ?? '',
    ...facts,
  }
}

describe('fileEditContent', () => {
  // The PATCH wins when it carries hunks, because that is what `fileEditDiffInput`
  // draws -- a source that kept the sides beside it stated a second answer nothing
  // would ever read. Claude's tool_use_result is the producer that holds both.
  it('answers the patch when it carries hunks, and drops the sides', () => {
    expect(fileEditContent(PATCH, 'old', 'new')).toEqual({ structuredPatch: PATCH })
  })

  // An empty patch is NO patch: every reader already tested the length.
  it('answers the sides for an empty or absent patch', () => {
    expect(fileEditContent([], 'old', 'new')).toEqual({ structuredPatch: null, oldStr: 'old', newStr: 'new' })
    expect(fileEditContent(null, 'old', 'new')).toEqual({ structuredPatch: null, oldStr: 'old', newStr: 'new' })
    expect(fileEditContent(undefined, '', '')).toEqual({ structuredPatch: null, oldStr: '', newStr: '' })
  })

  // Whichever half it answers, the source it builds draws the same hunks the readers
  // would have picked, so the choice changes no row.
  it('draws the same hunks either way', () => {
    const patched = {
      filePath: 'a.ts',
      ...fileEditContent(PATCH, 'old', 'new'),
    }
    const sided = {
      filePath: 'a.ts',
      ...fileEditContent(null, 'old', 'new'),
    }
    expect(fileEditDrawnHunks(patched)).toEqual(PATCH)
    expect(fileEditHasDiff(patched)).toBe(true)
    expect(fileEditHasDiff(sided)).toBe(true)
  })
})

describe('fileEditHasDiff', () => {
  it('returns false for null/undefined', () => {
    expect(fileEditHasDiff(null)).toBe(false)
    expect(fileEditHasDiff(undefined)).toBe(false)
  })

  it('returns true when structuredPatch is non-empty', () => {
    expect(fileEditHasDiff(source({ structuredPatch: PATCH }))).toBe(true)
  })

  it('returns false when structuredPatch is an empty array (and strings agree)', () => {
    expect(fileEditHasDiff(source({ structuredPatch: [] }))).toBe(false)
  })

  it('returns true for new-file write (empty old, non-empty new)', () => {
    expect(fileEditHasDiff(source({ oldStr: '', newStr: 'hello\n' }))).toBe(true)
  })

  it('returns false when old and new are identical non-empty strings', () => {
    expect(fileEditHasDiff(source({ oldStr: 'same', newStr: 'same' }))).toBe(false)
  })

  it('returns true when old and new differ', () => {
    expect(fileEditHasDiff(source({ oldStr: 'a', newStr: 'b' }))).toBe(true)
  })

  it('returns false when both halves are empty and there is no patch', () => {
    expect(fileEditHasDiff(source({ oldStr: '', newStr: '' }))).toBe(false)
  })

  it('ignores malformed structuredPatch arrays instead of treating them as renderable', () => {
    const malformed = [
      {
        oldStart: 1,
        oldLines: Number.NaN,
        newStart: 1,
        newLines: 1,
        lines: ['-old'],
      },
    ]
    expect(fileEditHasDiff(source({ structuredPatch: malformed as unknown as StructuredPatchHunk[] }))).toBe(false)
  })

  it('renders a deletion when the replacement is empty', () => {
    const deletion = source({ oldStr: 'gone\n', newStr: '' })
    expect(fileEditHasDiff(deletion)).toBe(true)
    expect(fileEditDrawnHunks(deletion)).toEqual([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 0,
        lines: ['-gone'],
      },
    ])
  })
})

describe('normalizeStructuredPatchHunks', () => {
  it('keeps valid hunk arrays by reference', () => {
    expect(normalizeStructuredPatchHunks(PATCH)).toBe(PATCH)
  })

  it('rejects non-string hunk lines', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: [42],
      },
    ])).toBeNull()
  })

  it('rejects empty hunk payloads', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 0,
        newStart: 1,
        newLines: 0,
        lines: [],
      },
    ])).toBeNull()
  })

  it('rejects negative and non-finite hunk coordinates', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: -1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-old'],
      },
    ])).toBeNull()
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: Number.NaN,
        newStart: 1,
        newLines: 1,
        lines: ['-old'],
      },
    ])).toBeNull()
  })

  it('rejects hunks whose line counts do not match their payload', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 1,
        lines: ['-old', '+new'],
      },
    ])).toBeNull()
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: [' old', '+new'],
      },
    ])).toBeNull()
  })

  // One miscounted hunk used to discard the WHOLE patch, and a source built from
  // hunks alone states both string halves as '' -- so the row then drew nothing.
  it('drops one malformed hunk and keeps the rest of the patch', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 2,
        newStart: 1,
        newLines: 1,
        lines: ['-old', '+new'],
      },
      {
        oldStart: 9,
        oldLines: 1,
        newStart: 9,
        newLines: 1,
        lines: ['-two', '+three'],
      },
    ])).toEqual([
      {
        oldStart: 9,
        oldLines: 1,
        newStart: 9,
        newLines: 1,
        lines: ['-two', '+three'],
      },
    ])
  })

  it('drops unified-diff no-newline marker lines from otherwise valid hunks', () => {
    expect(normalizeStructuredPatchHunks([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-old', '\\ No newline at end of file', '+new'],
      },
    ])).toEqual([
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-old', '+new'],
      },
    ])
  })
})

describe('fileEditDiffFromWholeFile', () => {
  it('builds an all-added source with the file content as newStr', () => {
    expect(fileEditDiffFromWholeFile('/tmp/new.ts', 'package main\n', 'add')).toEqual({
      filePath: '/tmp/new.ts',
      structuredPatch: null,
      operation: 'add',
      oldStr: '',
      newStr: 'package main\n',
    })
  })

  it('preserves an empty path / empty content (defensive: no crash on edges)', () => {
    expect(fileEditDiffFromWholeFile('', '', 'add')).toEqual({
      filePath: '',
      structuredPatch: null,
      operation: 'add',
      oldStr: '',
      newStr: '',
    })
  })

  it('produces an object recognized as renderable when content is non-empty', () => {
    expect(fileEditHasDiff(fileEditDiffFromWholeFile('/x', 'a', 'add'))).toBe(true)
  })

  it('produces an object that the diff worker diffs via the string-diff path', () => {
    const hunks = fileEditDrawnHunks(fileEditDiffFromWholeFile('/x', 'one\ntwo', 'add'))
    // computeDiff turns "" → "one\ntwo" into a single hunk with the new lines.
    expect(hunks.length).toBeGreaterThan(0)
  })
})

describe('fileEditDiffFromHunks', () => {
  const hunks: StructuredPatchHunk[] = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-old', '+new'],
    },
  ]

  // A source that states a PATCH states no sides at all: `FileEditContent` is a
  // union, so the two halves cannot both be there to disagree.
  it('attaches pre-parsed hunks and states no string halves', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', hunks)).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: hunks,
    })
  })

  it('keeps the hunks reference identity (no defensive copy)', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', hunks).structuredPatch).toBe(hunks)
  })

  // An empty patch is NO patch: every reader already tested the length, so the source
  // states the sides half instead of a patch that draws nothing.
  it('reads an empty hunks array as no patch', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', [])).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })
})

describe('pickFileEditDiff', () => {
  const resultDiff = source({ filePath: 'r.ts', oldStr: 'r-old', newStr: 'r-new' })
  const toolUseDiff = source({ filePath: 'u.ts', oldStr: 'u-old', newStr: 'u-new' })
  const emptyDiff = source({ filePath: 'e.ts' }) // no diff content

  it('returns the result diff when it has a renderable diff (regardless of tool_use)', () => {
    expect(pickFileEditDiff(resultDiff, toolUseDiff)).toBe(resultDiff)
    expect(pickFileEditDiff(resultDiff, null)).toBe(resultDiff)
  })

  it('falls back to tool_use diff when result has no renderable diff', () => {
    expect(pickFileEditDiff(emptyDiff, toolUseDiff)).toBe(toolUseDiff)
    expect(pickFileEditDiff(null, toolUseDiff)).toBe(toolUseDiff)
  })

  it('prefers result over tool_use even when both are renderable', () => {
    expect(pickFileEditDiff(resultDiff, toolUseDiff)).toBe(resultDiff)
  })

  it('returns null when neither side has a renderable diff', () => {
    expect(pickFileEditDiff(emptyDiff, null)).toBeNull()
    expect(pickFileEditDiff(null, source({ filePath: 'tu.ts' }))).toBeNull()
    expect(pickFileEditDiff(emptyDiff, source({ filePath: 'tu.ts' }))).toBeNull()
  })

  it('returns null when both inputs are null/undefined', () => {
    expect(pickFileEditDiff(null, null)).toBeNull()
    expect(pickFileEditDiff(undefined, undefined)).toBeNull()
  })
})

describe('FileEditDiffBody', () => {
  it('forwards premeasure context so diff tokenization is skipped', async () => {
    tokenizeAsyncMock.mockClear()
    const originalFile = Array.from({ length: 10 }, (_, i) => `line ${i + 1}`).join('\n')

    render(() => (
      <FileEditDiffBody
        source={source({
          filePath: 'example.ts',
          structuredPatch: [
            {
              oldStart: 10,
              oldLines: 1,
              newStart: 10,
              newLines: 1,
              lines: [' line 10'],
            },
          ],
          originalFile,
        })}
        diff={{
          view: () => 'unified',
        }}
        context={{ premeasureMode: true }}
      />
    ))

    fireEvent.click(screen.getByText('9 lines hidden'))
    await Promise.resolve()

    expect(screen.getByText('line 1')).toBeInTheDocument()
    expect(tokenizeAsyncMock).not.toHaveBeenCalled()
  })
})

describe('FileEditDiffBody while the diff worker computes', () => {
  const sides = () => source({ oldStr: 'a\nb\nc\n', newStr: 'a\nB\nc\n' })

  it('draws a skeleton of its own outside a chat row, then the diff', () => {
    holdComputedDiffs(true)
    render(() => (
      <FileEditDiffBody
        source={sides()}
        diff={{
          view: () => 'unified',
        }}
      />
    ))

    expect(screen.getByTestId('diff-pending')).toBeInTheDocument()
    expect(screen.queryByText('B')).not.toBeInTheDocument()

    holdComputedDiffs(false)

    expect(screen.queryByTestId('diff-pending')).not.toBeInTheDocument()
    expect(screen.getByText('B')).toBeInTheDocument()
  })

  it('holds its chat row through the readiness scope, and draws no skeleton of its own', () => {
    holdComputedDiffs(true)
    let scope: RenderReadinessScope | undefined
    render(() => {
      scope = createRenderReadinessScope()
      return (
        <FileEditDiffBody
          source={sides()}
          diff={{
            view: () => 'unified',
          }}
          context={{ readiness: scope }}
        />
      )
    })

    expect(scope?.pending()).toBe(true)
    expect(screen.queryByTestId('diff-pending')).not.toBeInTheDocument()

    holdComputedDiffs(false)

    expect(scope?.pending()).toBe(false)
    expect(screen.getByText('B')).toBeInTheDocument()
  })

  it('reveals the unchanged lines of the old side through the gap separators', async () => {
    const oldStr = Array.from({ length: 12 }, (_, i) => `line ${i + 1}\n`).join('')
    render(() => (
      <FileEditDiffBody
        source={source({
          oldStr,
          newStr: oldStr.replace('line 12\n', 'line twelve\n'),
        })}
        diff={{
          view: () => 'unified',
        }}
      />
    ))

    // Three lines of context keep lines 9 to 11, so the gap holds lines 1 to 8.
    expect(screen.queryByText('line 1')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('8 lines hidden'))
    await Promise.resolve()

    expect(screen.getByText('line 1')).toBeInTheDocument()
    expect(screen.getByText('line 8')).toBeInTheDocument()
  })
})

describe('FileEditDiffTitle', () => {
  it('states no line counts while the diff worker computes, then the counts of the diff', () => {
    holdComputedDiffs(true)
    render(() => <FileEditDiffTitle source={source({ oldStr: 'a\nb\n', newStr: 'a\nB\nC\n' })} />)

    expect(screen.queryByTestId('git-diff-stats')).not.toBeInTheDocument()

    holdComputedDiffs(false)

    expect(screen.getByTestId('git-diff-stats')).toHaveTextContent('+2 -1')
  })
})

describe('fileEditCopyHunks', () => {
  it('answers the hunks of a patch as final, with no diff to wait for', () => {
    holdComputedDiffs(true)

    expect(fileEditCopyHunks(source({ structuredPatch: PATCH }))).toEqual({ hunks: PATCH, final: true })
  })

  it('answers the whole old side replaced as a stand-in while the diff worker computes', () => {
    holdComputedDiffs(true)

    expect(fileEditCopyHunks(source({ oldStr: 'a\nb\nc\n', newStr: 'a\nB\nc\n' }))).toEqual({
      hunks: replacedWholeHunks('a\nb\nc\n', 'a\nB\nc\n'),
      final: false,
    })
  })

  it('answers no hunk for equal sides while the diff worker computes, as the final diff does', () => {
    holdComputedDiffs(true)

    expect(fileEditCopyHunks(source({ oldStr: 'same\n', newStr: 'same\n' }))).toEqual({ hunks: [], final: false })
  })

  it('answers the computed hunks as final once the diff is ready', () => {
    const input = { kind: 'texts', oldText: 'a\nb\nc\n', newText: 'a\nB\nc\n' } as const

    expect(fileEditCopyHunks(source({ oldStr: input.oldText, newStr: input.newText }))).toEqual({
      hunks: computeDiff(input).hunks,
      final: true,
    })
  })
})
