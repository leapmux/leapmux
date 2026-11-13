import type { DiffInput, StructuredPatchHunk } from '../diff/diffTypes'
import type { DiffHunksOf, FileEditBase, FileEditDiff } from './fileEditDiff'
import { describe, expect, it, vi } from 'vitest'
import { fileEditContent, fileEditCopyableText, fileEditDiffInput, fileEditDrawsDiff, fileEditHasDiff, requestedFileChangesCopyable } from './fileEditDiff'

const noChange = {
  filePath: '/repo/a.ts',
  ...fileEditContent(null, 'same', 'same'),
}
const changed = {
  filePath: '/repo/a.ts',
  ...fileEditContent(null, 'before', 'after'),
}

/**
 * `fileEditHasDiff` narrows, and its FALSE branch narrows the argument away -- to
 * `never` for a non-null source. TypeScript cannot declare a guard whose true branch
 * narrows and whose false branch says nothing, so a caller that keeps the value and
 * reports "no change" asks `fileEditDrawsDiff` instead.
 */
describe('fileEditDrawsDiff', () => {
  it('answers false for a real source that draws nothing', () => {
    expect(fileEditDrawsDiff(noChange)).toBe(false)
  })

  it('answers true when the two sides differ', () => {
    expect(fileEditDrawsDiff(changed)).toBe(true)
  })

  it('answers true for a non-empty patch whatever the sides say', () => {
    const patched = {
      filePath: '/repo/a.ts',
      ...fileEditContent([
        {
          oldStart: 1,
          oldLines: 1,
          newStart: 1,
          newLines: 1,
          lines: ['-a', '+b'],
        },
      ], 'same', 'same'),
    }
    expect(fileEditDrawsDiff(patched)).toBe(true)
  })

  it('agrees with the guard for every non-null source', () => {
    for (const source of [noChange, changed])
      expect(fileEditHasDiff(source)).toBe(fileEditDrawsDiff(source))
  })
})

describe('fileEditHasDiff', () => {
  it('absorbs the absent source the guard exists for', () => {
    expect(fileEditHasDiff(null)).toBe(false)
    expect(fileEditHasDiff(undefined)).toBe(false)
  })
})

describe('fileEditDiffInput', () => {
  const patch = [
    {
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-a', '+b'],
    },
  ]

  it('diffs the two sides of a source that holds no patch', () => {
    expect(fileEditDiffInput(changed)).toEqual({ kind: 'texts', oldText: 'before', newText: 'after' })
  })

  it('diffs the patch of a source that holds one', () => {
    expect(fileEditDiffInput({
      filePath: '/repo/a.ts',
      ...fileEditContent(patch, 'x', 'y'),
    })).toEqual({ kind: 'hunks', hunks: patch })
  })

  // The diff client keeps a diff per input object, so the title, the body and the
  // Copy text of one edit share one diff only when they share one input.
  it('answers one input object for each source object', () => {
    const sides = {
      filePath: '/repo/a.ts',
      ...fileEditContent(null, 'before', 'after'),
    }
    const patched = {
      filePath: '/repo/a.ts',
      ...fileEditContent(patch, '', ''),
    }

    expect(fileEditDiffInput(sides)).toBe(fileEditDiffInput(sides))
    expect(fileEditDiffInput(patched)).toBe(fileEditDiffInput(patched))
    expect(fileEditDiffInput({ ...sides })).not.toBe(fileEditDiffInput(sides))
  })
})

/** One hunk that changes `a` to `b`, which a resolver answers for any input. */
const HUNKS: StructuredPatchHunk[] = [
  {
    oldStart: 1,
    oldLines: 1,
    newStart: 1,
    newLines: 1,
    lines: ['-a', '+b'],
  },
]

/** A resolver that answers HUNKS and records each input it receives. */
function hunksSpy() {
  return vi.fn<DiffHunksOf>((_input: DiffInput) => HUNKS)
}

function sides(oldStr: string, newStr: string, facts: Partial<FileEditBase> = {}): FileEditDiff {
  return {
    filePath: '/repo/a.ts',
    ...facts,
    ...fileEditContent(null, oldStr, newStr),
  }
}

describe('fileEditCopyableText', () => {
  it('asks the resolver for the hunks of the input of the source, and states them', () => {
    const source = sides('a\n', 'b\n')
    const hunksOf = hunksSpy()

    const text = fileEditCopyableText(source, hunksOf)

    expect(hunksOf).toHaveBeenCalledTimes(1)
    expect(hunksOf.mock.calls[0]?.[0]).toBe(fileEditDiffInput(source))
    expect(text).toContain('-a\n+b')
  })

  it('asks the resolver nothing for a source that draws no diff', () => {
    const hunksOf = hunksSpy()

    expect(fileEditCopyableText(sides('same', 'same'), hunksOf)).toBe('')
    expect(hunksOf).not.toHaveBeenCalled()
  })

  it('states a landed delete or add that has no diff to draw', () => {
    const hunksOf = hunksSpy()

    expect(fileEditCopyableText(sides('', '', { operation: 'delete' }), hunksOf)).toBe('Deleted /repo/a.ts')
    expect(fileEditCopyableText(sides('', '', { operation: 'add' }), hunksOf)).toBe('Created /repo/a.ts')
    expect(hunksOf).not.toHaveBeenCalled()
  })

  it('states a rename and the notice before the diff', () => {
    const text = fileEditCopyableText(sides('a\n', 'b\n', { previousPath: '/repo/old.ts', notice: 'Applied' }), hunksSpy())

    expect(text.split('\n').slice(0, 3)).toEqual([
      'rename from /repo/old.ts',
      'rename to /repo/a.ts',
      'Applied',
    ])
  })
})

describe('requestedFileChangesCopyable', () => {
  it('answers the empty text for no change', () => {
    expect(requestedFileChangesCopyable([], hunksSpy())).toBe('')
  })

  it('asks the resolver for the hunks of the input of each source that draws a diff', () => {
    const changed = sides('a\n', 'b\n')
    const unchanged = sides('same', 'same')
    const hunksOf = hunksSpy()

    requestedFileChangesCopyable([changed, unchanged], hunksOf)

    expect(hunksOf).toHaveBeenCalledTimes(1)
    expect(hunksOf.mock.calls[0]?.[0]).toBe(fileEditDiffInput(changed))
  })

  it('words each requested operation that has no diff to draw, and never as a landed one', () => {
    const text = requestedFileChangesCopyable([
      sides('', '', { filePath: '/repo/gone.ts', operation: 'delete' }),
      sides('', '', { filePath: '/repo/new.ts', operation: 'add' }),
      sides('same', 'same', { filePath: '/repo/same.ts', operation: 'edit' }),
    ], hunksSpy())

    expect(text).toBe('Requested changes\n\nDelete /repo/gone.ts\n\nCreate /repo/new.ts\n\nChange /repo/same.ts')
  })

  it('states the diff of a requested change that has one, with no landed operation', () => {
    const text = requestedFileChangesCopyable([
      sides('a\n', '', { operation: 'delete' }),
    ], hunksSpy())

    expect(text).toContain('-a\n+b')
    expect(text).not.toContain('Delete')
  })
})
