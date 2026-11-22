import type { DiffInput } from '../../../diff/diffTypes'
import { describe, expect, it } from 'vitest'
import { fileEditDrawnHunks } from '~/test-support/fileEditHunks'
import { computeDiff } from '../../../diff/diffSearch'
import { fileEditCopyableText } from '../../../model/fileEditDiff'
import { antigravityLandedChanges } from './fileEdit'
import { antigravityStep } from './toolCommon'

function landed(lines: unknown, startLine?: unknown) {
  return antigravityLandedChanges(antigravityStep({
    type: 'CORTEX_STEP_TYPE_CODE_ACTION',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: {
      toolCall: { name: 'edit_file', argumentsJson: '{"Path":"/p/a.ts","Target":"alpha","Replacement":"proposed"}' },
    },
    codeAction: {
      actionSpec: {
        command: {
          file: { absoluteUri: 'file:///p/a.ts' },
        },
      },
      actionResult: {
        edit: {
          diff: {
            startLine,
            unifiedDiff: { lines },
          },
        },
      },
    },
  })!)
}

describe('antigravityLandedChanges', () => {
  it('uses committed native lines rather than the proposed replacement', () => {
    const changes = landed([
      { text: 'context', type: 'UNIFIED_DIFF_LINE_TYPE_UNCHANGED' },
      { text: 'alpha', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
      { text: 'committed', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
    ])
    expect(changes).not.toBeNull()
    const change = changes![0]!
    expect(fileEditDrawnHunks(change)).toMatchObject([
      {
        oldLines: 2,
        newLines: 2,
        lines: [' context', '-alpha', '+committed'],
      },
    ])
    expect(change.showLineNumbers).not.toBe(false)
    const hunksOf = (input: DiffInput) => computeDiff(input).hunks
    expect(fileEditCopyableText(change, hunksOf)).toContain('+committed')
    expect(fileEditCopyableText(change, hunksOf)).not.toContain('+proposed')
  })

  it('keeps the file coordinates and collapses distant unchanged lines', () => {
    const context = (start: number, count: number) => Array.from({ length: count }, (_, offset) => ({
      text: `line ${start + offset}`,
      type: 'UNIFIED_DIFF_LINE_TYPE_UNCHANGED',
    }))
    const change = landed([
      ...context(1, 40),
      { text: 'alpha', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
      { text: 'committed', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
      { text: 'extra', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
      ...context(42, 50),
      { text: 'old', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
      { text: 'new', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
      ...context(93, 40),
    ])![0]!
    const hunks = fileEditDrawnHunks(change)
    expect(hunks).toHaveLength(2)
    expect(hunks[0]!.oldStart + hunks[0]!.lines.indexOf('-alpha')).toBe(41)
    expect(hunks[1]!.oldStart + hunks[1]!.lines.indexOf('-old')).toBe(92)
    expect(hunks[1]!.newStart).toBe(hunks[1]!.oldStart + 1)
    expect(change.originalFile).toMatchObject({ startLineNumber: 1 })
    const original = change.originalFile
    expect(original && typeof original !== 'string' && original.lines[20]).toBe('line 21')
  })

  it('uses the native start offset for partial blocks and splits multiline entries', () => {
    const change = landed([
      { text: 'context', type: 'UNIFIED_DIFF_LINE_TYPE_UNCHANGED' },
      { text: 'alpha\nbeta', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
      { text: 'committed', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
    ], 120)![0]!
    expect(fileEditDrawnHunks(change)).toEqual([
      {
        oldStart: 121,
        oldLines: 3,
        newStart: 121,
        newLines: 2,
        lines: [
          ' context',
          '-alpha',
          '-beta',
          '+committed',
        ],
      },
    ])
    expect(change).toMatchObject({
      originalFile: {
        startLineNumber: 121,
        lines: ['context', 'alpha', 'beta'],
      },
    })
  })

  it.each([-1, 0.5, Number.MAX_SAFE_INTEGER, '40'])('rejects an invalid native start offset %j', (startLine) => {
    expect(landed([
      { text: 'new', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
    ], startLine)).toBeNull()
  })

  it.each([
    [
      [
        { text: 'new', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
      ],
      0,
      1,
    ],
    [
      [
        { text: 'old', type: 'UNIFIED_DIFF_LINE_TYPE_DELETE' },
      ],
      1,
      0,
    ],
  ])('retains insertion and deletion counts for %j', (lines, oldLines, newLines) => {
    expect(fileEditDrawnHunks(landed(lines)![0]!)).toMatchObject([
      { oldLines, newLines },
    ])
  })

  it.each([
    undefined,
    null,
    {},
    [],
    [null],
    [
      { type: 'UNIFIED_DIFF_LINE_TYPE_INSERT', text: 0 },
    ],
    [
      { text: 'x', type: 'future' },
    ],
  ])('rejects unusable native lines %j', (lines) => {
    expect(landed(lines)).toBeNull()
  })
  it.each([undefined, null, ''])('retains the native empty-string default for text %j', (text) => {
    const line = {
      type: 'UNIFIED_DIFF_LINE_TYPE_INSERT',
      ...(text === undefined
        ? {}
        : { text }),
    }
    const changes = landed([line])
    expect(changes).not.toBeNull()
    expect(fileEditDrawnHunks(changes![0]!)).toMatchObject([
      {
        oldLines: 0,
        newLines: 1,
        lines: ['+'],
      },
    ])
  })

  it('retains the empty final line from a captured native write result', () => {
    const changes = landed([
      { text: 'hello', type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
      { type: 'UNIFIED_DIFF_LINE_TYPE_INSERT' },
    ])
    expect(changes).not.toBeNull()
    expect(fileEditDrawnHunks(changes![0]!)).toMatchObject([
      {
        oldLines: 0,
        newLines: 2,
        lines: ['+hello', '+'],
      },
    ])
  })
})
