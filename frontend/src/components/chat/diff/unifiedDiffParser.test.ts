import { describe, expect, it } from 'vitest'
import { normalizeStructuredPatchHunks } from '../ir/fileEditDiff'
import { parseUnifiedDiff } from './unifiedDiffParser'

describe('parseUnifiedDiff', () => {
  it('returns null for text that holds no hunk', () => {
    expect(parseUnifiedDiff('')).toBeNull()
    expect(parseUnifiedDiff('not a diff\nat all')).toBeNull()
  })

  it('reconstructs both sides of a hunk', () => {
    const parsed = parseUnifiedDiff('@@ -1,2 +1,2 @@\n context\n-old\n+new')
    expect(parsed?.oldText).toBe('context\nold')
    expect(parsed?.newText).toBe('context\nnew')
    expect(parsed?.hunks).toEqual([
      { oldStart: 1, oldLines: 2, newStart: 1, newLines: 2, lines: [' context', '-old', '+new'] },
    ])
  })

  // The header's counts are reported as they were stated, never recomputed from the
  // lines that survived the loop. A consumer checks the body against them, and the
  // mismatch is what tells `copilot/readResult.ts` that a `detailedContent` diff was
  // truncated -- so the row keeps the native content instead of stating a fraction of
  // a file as the whole of it. `normalizeStructuredPatchHunks` drops such a hunk for
  // the same reason.
  it('reports the counts the header stated, not the lines it kept', () => {
    const parsed = parseUnifiedDiff('@@ -1,9 +1,9 @@\n-old\nsomething unrecognized\n+new')
    expect(parsed?.hunks).toEqual([
      { oldStart: 1, oldLines: 9, newStart: 1, newLines: 9, lines: ['-old', '+new'] },
    ])
    expect(normalizeStructuredPatchHunks(parsed?.hunks)).toBeNull()
  })

  it('keeps the start positions the header stated', () => {
    const parsed = parseUnifiedDiff('@@ -40 +120 @@\n-old\n+new')
    const hunk = parsed?.hunks[0]
    expect(hunk?.oldStart).toBe(40)
    expect(hunk?.newStart).toBe(120)
  })

  it('drops the no-newline marker without counting it', () => {
    const parsed = parseUnifiedDiff('@@ -1,1 +1,1 @@\n-old\n\\ No newline at end of file\n+new')
    expect(parsed?.hunks).toEqual([
      { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] },
    ])
  })
})
