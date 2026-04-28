import type { StructuredPatchHunk } from '../diff'
import type { FileEditDiffSource } from './fileEditDiff'
import { describe, expect, it } from 'vitest'
import { fileEditDiffFromHunks, fileEditDiffFromNewFile, fileEditDiffHunks, fileEditHasDiff, pickFileEditDiff } from './fileEditDiff'

const PATCH: StructuredPatchHunk[] = [
  { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] },
]

function source(over: Partial<FileEditDiffSource> = {}): FileEditDiffSource {
  return {
    filePath: 'a.ts',
    structuredPatch: null,
    oldStr: '',
    newStr: '',
    ...over,
  }
}

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

  it('returns false for an "all-removed" change (non-empty old, empty new)', () => {
    // The new-file shortcut intentionally only applies to oldStr=''+newStr='something'.
    // An empty replacement is not currently treated as a renderable diff by itself.
    expect(fileEditHasDiff(source({ oldStr: 'gone', newStr: '' }))).toBe(false)
  })
})

describe('fileEditDiffFromNewFile', () => {
  it('builds an all-added source with the file content as newStr', () => {
    expect(fileEditDiffFromNewFile('/tmp/new.ts', 'package main\n')).toEqual({
      filePath: '/tmp/new.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'package main\n',
    })
  })

  it('preserves an empty path / empty content (defensive: no crash on edges)', () => {
    expect(fileEditDiffFromNewFile('', '')).toEqual({
      filePath: '',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })

  it('produces an object recognized as renderable when content is non-empty', () => {
    expect(fileEditHasDiff(fileEditDiffFromNewFile('/x', 'a'))).toBe(true)
  })

  it('produces an object that fileEditDiffHunks renders via the string-diff path', () => {
    const hunks = fileEditDiffHunks(fileEditDiffFromNewFile('/x', 'one\ntwo'))
    // rawDiffToHunks turns "" → "one\ntwo" into a single hunk with the new lines.
    expect(hunks.length).toBeGreaterThan(0)
  })
})

describe('fileEditDiffFromHunks', () => {
  const hunks: StructuredPatchHunk[] = [
    { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] },
  ]

  it('attaches pre-parsed hunks and leaves the string halves empty', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', hunks)).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: hunks,
      oldStr: '',
      newStr: '',
    })
  })

  it('keeps the hunks reference identity (no defensive copy)', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', hunks).structuredPatch).toBe(hunks)
  })

  it('preserves an empty hunks array (a zero-hunk diff is meaningful)', () => {
    expect(fileEditDiffFromHunks('/tmp/a.ts', []).structuredPatch).toEqual([])
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
