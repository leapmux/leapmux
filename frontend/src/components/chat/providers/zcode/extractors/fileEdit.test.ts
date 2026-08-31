import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { ZCODE_DISPLAY } from '../protocol'
import { extractZCodeFileDiff, extractZCodeRead, zcodeFilePath } from './fileEdit'
import { zcodeRow } from './toolCommon'

function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: ZCODE_EVENT.ToolUpdated, payload: { kind, toolCallId: 'call-1', ...payload } }
}

function scheduled(toolName: string, input: Record<string, unknown>): ParsedMessageContent {
  const parent = toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName, input })
  return { rawText: '', topLevel: parent, parentObject: parent, wrapper: null }
}

const HUNK = {
  oldStart: 1,
  oldLines: 1,
  newStart: 1,
  newLines: 1,
  lines: ['-before', '+after'],
}

function fileDiffDisplay(extra: Record<string, unknown> = {}) {
  return {
    kind: ZCODE_DISPLAY.FileDiff,
    filePath: '/tmp/a.ts',
    structuredPatch: [HUNK],
    ...extra,
  }
}

describe('extractZCodeFileDiff', () => {
  // The app-server hands over a READY structured patch in the same hunk shape the
  // shared diff view consumes, so there is no diff text to parse.
  it('takes the structured patch the display carries', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'Edited.', display: fileDiffDisplay() } }), ZCODE_TOOL.Edit, undefined))).toEqual({ filePath: '/tmp/a.ts', structuredPatch: [HUNK], oldStr: '', newStr: '' })
  })

  it('falls back to the input path when the display states none', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, {
      result: { display: { kind: ZCODE_DISPLAY.FileDiff, structuredPatch: [HUNK] } },
    }), undefined, scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/from-input.ts' })))).toMatchObject({ filePath: '/tmp/from-input.ts' })
  })

  it('ignores a display whose kind is not file_diff', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, {
      result: { display: { kind: 'something_else', structuredPatch: [HUNK] } },
    }), ZCODE_TOOL.Edit, undefined))).toBeNull()
  })

  it('ignores a file_diff display whose patch is empty or malformed', () => {
    for (const structuredPatch of [[], 'not an array', [{ lines: [] }]]) {
      expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, {
        result: { display: { kind: ZCODE_DISPLAY.FileDiff, structuredPatch } },
      }), ZCODE_TOOL.Edit, undefined))).toBeNull()
    }
  })

  // A Write of a new file reports no patch, because there is no old side to diff
  // against -- so the input body is the all-added diff.
  it('builds an all-added diff from a Write input when no display arrived', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'Created.' } }), undefined, scheduled(ZCODE_TOOL.Write, { file_path: '/tmp/new.ts', content: 'const a = 1\n' })))).toEqual({
      filePath: '/tmp/new.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'const a = 1\n',
    })
  })

  it('builds the substitution an Edit input asked for', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'Edited.' } }), undefined, scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/a.ts', old_string: 'before', new_string: 'after' })))).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'before',
      newStr: 'after',
    })
  })

  // The tool_use row is rendered BEFORE the result exists, and its input is the only
  // diff available then.
  it('reads the input diff off the scheduled row itself', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Edit,
      input: { file_path: '/tmp/a.ts', old_string: 'x', new_string: 'y' },
    }), undefined, undefined))).toMatchObject({ oldStr: 'x', newStr: 'y' })
  })

  it('returns null when the input implies no diff at all', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'Edited.' } }), undefined, scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/a.ts' })))).toBeNull()
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'Edited.' } }), undefined, scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/a.ts', old_string: 'same', new_string: 'same' })))).toBeNull()
  })

  // A failed call renders its error text; drawing the attempted edit would claim it
  // landed.
  it('returns null for a failed call, even with a display', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Error, {
      error: { message: 'old_string not found' },
      result: { display: fileDiffDisplay() },
    }), ZCODE_TOOL.Edit, undefined))).toBeNull()
  })

  it('returns null for a tool that has no diff of its own', () => {
    expect(extractZCodeFileDiff(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { display: fileDiffDisplay() } }), ZCODE_TOOL.Bash, undefined))).toBeNull()
    expect(extractZCodeFileDiff(zcodeRow({ type: ZCODE_EVENT.TurnCompleted, payload: {} }, ZCODE_TOOL.Edit, undefined))).toBeNull()
  })
})

describe('zcodeFilePath', () => {
  it('prefers the display path', () => {
    expect(zcodeFilePath(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { display: fileDiffDisplay() } }), undefined, scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/ignored.ts' })))).toBe('/tmp/a.ts')
  })

  // ZCode's own tools spell the key `file_path`; `filePath` is accepted so a build
  // that switches spelling does not blank every title.
  it('accepts either spelling of the input key', () => {
    expect(zcodeFilePath(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Scheduled, { input: { file_path: '/a' } }), undefined, undefined))).toBe('/a')
    expect(zcodeFilePath(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Scheduled, { input: { filePath: '/b' } }), undefined, undefined))).toBe('/b')
  })

  it('reports an empty string when nothing names a path', () => {
    expect(zcodeFilePath(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: {} }), undefined, undefined))).toBe('')
    expect(zcodeFilePath(zcodeRow(null, undefined, undefined))).toBe('')
  })
})

describe('extractZCodeRead', () => {
  // ZCode returns the file already NUMBERED, in `cat -n` form, so the shared cat-n
  // parser owns the body.
  it('parses the numbered body into lines', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: '1\talpha\n2\tbeta' } }), undefined, scheduled(ZCODE_TOOL.Read, { file_path: '/tmp/a.ts' })))
    expect(read?.source.filePath).toBe('/tmp/a.ts')
    expect(read?.source.numLines).toBe(2)
    expect(read?.source.lines?.map(l => l.text)).toEqual(['alpha', 'beta'])
  })

  it('carries the requested offset and limit through', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: '10\tten' } }), undefined, scheduled(ZCODE_TOOL.Read, { file_path: '/tmp/a.ts', offset: 10, limit: 5 })))
    expect(read).toMatchObject({ offset: 10, limit: 5 })
  })

  it('reports null offset and limit when the input asked for neither', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: '1\ta' } }), ZCODE_TOOL.Read, undefined))
    expect(read).toMatchObject({ offset: null, limit: null })
  })

  // A binary read, or a build that stops numbering, falls back to plain text starting
  // at the requested offset.
  it('falls back to plain text when the body is not numbered', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'plain body\nsecond line' } }), undefined, scheduled(ZCODE_TOOL.Read, { file_path: '/tmp/a.bin', offset: 7 })))
    expect(read?.source.lines?.[0]).toMatchObject({ num: 7, text: 'plain body' })
  })

  it('starts the fallback at line 1 when no offset was requested', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'plain body' } }), ZCODE_TOOL.Read, undefined))
    expect(read?.source.lines?.[0]).toMatchObject({ num: 1 })
  })

  it('handles an empty result body without throwing', () => {
    const read = extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: '' } }), ZCODE_TOOL.Read, undefined))
    expect(read).not.toBeNull()
  })

  it('returns null for another tool and for a row that is not a tool.updated', () => {
    expect(extractZCodeRead(zcodeRow(toolEvent(ZCODE_TOOL_KIND.Result, { result: {} }), ZCODE_TOOL.Bash, undefined))).toBeNull()
    expect(extractZCodeRead(zcodeRow({ type: ZCODE_EVENT.TurnCompleted, payload: {} }, ZCODE_TOOL.Read, undefined))).toBeNull()
    expect(extractZCodeRead(zcodeRow(null, ZCODE_TOOL.Read, undefined))).toBeNull()
  })
})
