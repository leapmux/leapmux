import { describe, expect, it } from 'vitest'
import { fileEditContent, fileEditDrawsDiff, fileEditHasDiff } from './fileEditDiff'

const noChange = { filePath: '/repo/a.ts', ...fileEditContent(null, 'same', 'same') }
const changed = { filePath: '/repo/a.ts', ...fileEditContent(null, 'before', 'after') }

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
      ...fileEditContent([{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-a', '+b'] }], 'same', 'same'),
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
