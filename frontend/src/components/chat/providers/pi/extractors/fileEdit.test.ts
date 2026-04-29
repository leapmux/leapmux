import { describe, expect, it } from 'vitest'
import { extractPiEdit, extractPiRead, extractPiWrite } from './fileEdit'

describe('extractPiEdit', () => {
  it('returns null for non-edit tool', () => {
    expect(extractPiEdit({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'bash' })).toBeNull()
  })

  it('extracts edits as FileEditDiffSources', () => {
    const out = extractPiEdit({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'edit',
      args: {
        path: '/repo/src/foo.ts',
        edits: [
          { oldText: 'old', newText: 'new' },
          { oldText: 'a', newText: 'b' },
        ],
      },
      result: { content: [{ type: 'text', text: 'patched' }], details: {} },
      isError: false,
    })
    expect(out).toEqual({
      path: '/repo/src/foo.ts',
      sources: [
        { filePath: '/repo/src/foo.ts', structuredPatch: null, oldStr: 'old', newStr: 'new' },
        { filePath: '/repo/src/foo.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' },
      ],
      isError: false,
    })
  })

  it('handles missing args.edits gracefully', () => {
    const out = extractPiEdit({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'edit',
      args: { path: '/repo/x' },
    })
    expect(out?.sources).toEqual([])
  })
})

describe('extractPiWrite', () => {
  it('returns an all-added diff source', () => {
    const out = extractPiWrite({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'write',
      args: { path: '/tmp/foo', content: 'data\n' },
      result: { content: [{ type: 'text', text: 'wrote 5 bytes' }], details: {} },
    })
    expect(out).toEqual({
      filePath: '/tmp/foo',
      structuredPatch: null,
      oldStr: '',
      newStr: 'data\n',
    })
  })
})

describe('extractPiRead', () => {
  it('packs the result into a ReadFileResultSource and surfaces the requested range', () => {
    const out = extractPiRead({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'read',
      args: { path: '/repo/x', offset: 10, limit: 50 },
      result: { content: [{ type: 'text', text: 'contents' }], details: {} },
    })
    expect(out).toEqual({
      source: {
        filePath: '/repo/x',
        lines: [{ num: 10, text: 'contents' }],
        totalLines: 0,
        numLines: 0,
        fallbackContent: 'contents',
      },
      offset: 10,
      limit: 50,
    })
  })

  it('uses fallback start args for tool_execution_end payloads without args', () => {
    const out = extractPiRead({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'read',
      result: { content: [{ type: 'text', text: 'line1\nline2' }], details: {} },
    }, { path: '/repo/x', offset: 20, limit: 2 })
    expect(out).toEqual({
      source: {
        filePath: '/repo/x',
        lines: [{ num: 20, text: 'line1' }, { num: 21, text: 'line2' }],
        totalLines: 0,
        numLines: 0,
        fallbackContent: 'line1\nline2',
      },
      offset: 20,
      limit: 2,
    })
  })

  it('treats missing offset/limit as null', () => {
    const out = extractPiRead({
      type: 'tool_execution_end',
      toolCallId: 'c',
      toolName: 'read',
      args: { path: '/repo/x' },
    })
    expect(out?.offset).toBeNull()
    expect(out?.limit).toBeNull()
  })
})
