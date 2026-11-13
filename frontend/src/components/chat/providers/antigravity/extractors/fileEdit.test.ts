import type { DiffInput } from '../../../diff/diffTypes'
import { describe, expect, it } from 'vitest'
import { fileEditDrawnHunks } from '~/test-support/fileEditHunks'
import { computeDiff } from '../../../diff/diffSearch'
import { fileEditCopyableText } from '../../../model/fileEditDiff'
import { antigravityLandedChanges } from './fileEdit'
import { antigravityStep } from './toolCommon'

function landed(lines: unknown) {
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
    expect(change.showLineNumbers).toBe(false)
    const hunksOf = (input: DiffInput) => computeDiff(input).hunks
    expect(fileEditCopyableText(change, hunksOf)).toContain('+committed')
    expect(fileEditCopyableText(change, hunksOf)).not.toContain('+proposed')
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
