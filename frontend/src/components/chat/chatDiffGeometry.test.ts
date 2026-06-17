import type { StructuredPatchHunk } from './diff'
import { describe, expect, it } from 'vitest'
import { diffHeightFields, mergeDiffHeightFields } from './chatDiffGeometry'
import { parseUnifiedDiffCached, rawDiffToHunks } from './diff'

describe('chatdiffgeometry diffHeightFields', () => {
  it('returns null for no hunks', () => {
    expect(diffHeightFields([])).toBeNull()
  })

  it('returns null when every hunk is malformed (filtered out)', () => {
    // Missing the numeric coords the geometry math reads -> filtered by the guard.
    const malformed = [{ lines: ['+x'] } as unknown as StructuredPatchHunk]
    expect(diffHeightFields(malformed)).toBeNull()
  })

  it('drops only the malformed hunks, keeping the valid ones', () => {
    const valid: StructuredPatchHunk = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }
    const malformed = { lines: ['+x'] } as unknown as StructuredPatchHunk
    const fields = diffHeightFields([valid, malformed])!
    expect(fields).not.toBeNull()
    expect(fields.diffHunkCount).toBe(1) // malformed entry dropped, valid kept
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
  })

  it('drops a hunk with a NEGATIVE line count (would under-count split rows / suppress a gap)', () => {
    const valid: StructuredPatchHunk = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }
    // A negative count survives Number.isFinite but Math.max(neg, neg) under-counts
    // split rows and oldStart+oldLines can fall below the next hunk, dropping a gap.
    const negative = { oldStart: 1, oldLines: -5, newStart: 1, newLines: 1, lines: ['+x'] } as unknown as StructuredPatchHunk
    const fields = diffHeightFields([valid, negative])!
    expect(fields.diffHunkCount).toBe(1) // negative-coord hunk dropped
    expect(fields.diffSplitRows).toBeGreaterThanOrEqual(0)
  })

  it('clamps split rows to the real line count for a hunk claiming far more lines than it carries', () => {
    // A malformed hunk whose coords claim a billion lines but carries two lines
    // must not feed a multi-billion-px row count into the estimate (isUsableHeight
    // has no upper bound). Split rows clamp to lines.length.
    const absurd: StructuredPatchHunk = { oldStart: 1, oldLines: 1_000_000_000, newStart: 1, newLines: 1_000_000_000, lines: ['-x', '+y'] }
    const fields = diffHeightFields([absurd])!
    expect(fields.diffUnifiedRows).toBe(2)
    expect(fields.diffSplitRows).toBe(2) // clamped to lines.length, not 1e9
  })

  it('drops a hunk whose coords are non-finite (NaN/Infinity would poison the offset map)', () => {
    const valid: StructuredPatchHunk = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }
    // typeof NaN === 'number', so the prior shape check let these through. A NaN
    // oldLines flows through Math.max(...) into diffSplitRows and turns the row
    // estimate -- and every cumulative offset past it -- into NaN.
    const nanHunk = { oldStart: 1, oldLines: Number.NaN, newStart: 1, newLines: 1, lines: ['+x'] } as unknown as StructuredPatchHunk
    const infHunk = { oldStart: 1, oldLines: 1, newStart: 1, newLines: Number.POSITIVE_INFINITY, lines: ['+y'] } as unknown as StructuredPatchHunk
    const fields = diffHeightFields([valid, nanHunk, infHunk])!
    expect(fields.diffHunkCount).toBe(1) // only the finite-coord hunk survives
    expect(Number.isFinite(fields.diffSplitRows)).toBe(true)
    expect(Number.isFinite(fields.diffUnifiedRows)).toBe(true)
  })

  it('drops a hunk whose lines hold a non-string element (would throw inside the memo)', () => {
    const valid: StructuredPatchHunk = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }
    // A non-string lines element survives the array check but breaks the geometry
    // math: diffRowsFromHunks calls `line.startsWith(...)` ((42).startsWith is not
    // a function -> throws) and lineContentLength reads `line.length` (undefined ->
    // NaN). The element-type guard must filter the whole hunk out.
    const numberLine = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [42] } as unknown as StructuredPatchHunk
    const objectLine = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [{ text: '+x' }] } as unknown as StructuredPatchHunk
    // Must not throw, and must drop the malformed hunks while keeping the valid one.
    const fields = diffHeightFields([valid, numberLine, objectLine])!
    expect(fields.diffHunkCount).toBe(1)
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
    expect(fields.diffLineLengths?.every(Number.isFinite)).toBe(true)
  })

  it('returns null (no throw) when the only hunk has non-string lines', () => {
    const numberLine = { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [42, 7] } as unknown as StructuredPatchHunk
    expect(diffHeightFields([numberLine])).toBeNull()
  })

  it('computes unified rows / added / removed / hunk count for a single hunk', () => {
    const hunks = rawDiffToHunks('a\nb\nc', 'a\nB\nc')
    const fields = diffHeightFields(hunks)!
    expect(fields).not.toBeNull()
    expect(fields.diffHunkCount).toBe(1)
    expect((fields.diffAdded ?? 0)).toBeGreaterThan(0)
    expect((fields.diffRemoved ?? 0)).toBeGreaterThan(0)
    expect((fields.diffUnifiedRows ?? 0)).toBeGreaterThan(0)
    // Per-line arrays exist and have one entry per unified/split rendered line.
    expect((fields.diffLineLengths ?? []).length).toBe(fields.diffUnifiedRows)
  })

  it('keeps per-hunk boundaries: a between-hunk gap is counted as a separator', () => {
    const diff = [
      '@@ -1,3 +1,3 @@',
      ' a',
      '-b',
      '+B',
      '@@ -10,3 +10,3 @@',
      ' x',
      '-y',
      '+Y',
    ].join('\n')
    const fields = diffHeightFields(parseUnifiedDiffCached(diff)!.hunks)!
    expect(fields.diffHunkCount).toBe(2)
    expect(fields.diffSeparatorRows).toBe(1) // lines 4-9 hidden between the two hunks
    expect(fields.diffAdded).toBe(2)
    expect(fields.diffRemoved).toBe(2)
  })

  it('counts leading/trailing context gaps only when originalFile is supplied', () => {
    // One hunk starting at line 5 of a 20-line file: a leading gap (1-4) and a
    // trailing gap (after the hunk to EOF) both render once originalFile is known.
    const hunks: StructuredPatchHunk[] = [{
      oldStart: 5,
      oldLines: 2,
      newStart: 5,
      newLines: 2,
      lines: [' a', '-b', '+B'],
    }]
    const withoutFile = diffHeightFields(hunks)!
    expect(withoutFile.diffSeparatorRows).toBe(0)
    const originalFile = `${Array.from({ length: 20 }, (_, i) => `line${i + 1}`).join('\n')}\n`
    const withFile = diffHeightFields(hunks, originalFile)!
    expect(withFile.diffSeparatorRows).toBe(2)
  })
})

describe('chatdiffgeometry mergeDiffHeightFields', () => {
  it('returns null for no slices', () => {
    expect(mergeDiffHeightFields([])).toBeNull()
  })

  it('tags a single slice with diffBlockCount 1 and preserves its fields', () => {
    const slice = diffHeightFields(rawDiffToHunks('a\nb\nc', 'a\nB\nc'))!
    const merged = mergeDiffHeightFields([slice])!
    expect(merged.diffBlockCount).toBe(1)
    expect(merged.diffHunkCount).toBe(slice.diffHunkCount)
    expect(merged.diffAdded).toBe(slice.diffAdded)
  })

  it('sums rows/added/removed/hunks/separators per block and records the block count', () => {
    // Two distinct files. Sizing them as ONE concatenated hunk set would
    // under-count container chrome and could let the cross-file hunk boundary
    // trip a spurious between-hunk separator; per-block summing avoids both.
    const a = diffHeightFields(rawDiffToHunks('a\nb', 'a\nB'))!
    const b = diffHeightFields(rawDiffToHunks('x\ny\nz', 'X\nY\nZ'))!
    const merged = mergeDiffHeightFields([a, b])!
    expect(merged.diffBlockCount).toBe(2)
    expect(merged.diffHunkCount).toBe((a.diffHunkCount ?? 0) + (b.diffHunkCount ?? 0))
    expect(merged.diffAdded).toBe((a.diffAdded ?? 0) + (b.diffAdded ?? 0))
    expect(merged.diffRemoved).toBe((a.diffRemoved ?? 0) + (b.diffRemoved ?? 0))
    expect(merged.diffSeparatorRows).toBe((a.diffSeparatorRows ?? 0) + (b.diffSeparatorRows ?? 0))
    expect(merged.diffUnifiedRows).toBe((a.diffUnifiedRows ?? 0) + (b.diffUnifiedRows ?? 0))
    expect(merged.diffLineLengths!.length).toBe((a.diffLineLengths?.length ?? 0) + (b.diffLineLengths?.length ?? 0))
  })
})
