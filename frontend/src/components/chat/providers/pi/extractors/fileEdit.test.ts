import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { extractPiEdit, extractPiRead, extractPiWrite, piResolveDiffSources } from './fileEdit'

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

describe('piResolveDiffSources', () => {
  // A tool_execution_start sibling with the original edit substitutions.
  const startEdit: Record<string, unknown> = {
    type: 'tool_execution_start',
    toolCallId: 't1',
    toolName: 'edit',
    args: { path: '/repo/x.ts', edits: [{ oldText: 'a', newText: 'b' }] },
  }
  const toolUseParsed = (parentObject: Record<string, unknown>): ParsedMessageContent =>
    ({ rawText: '', topLevel: parentObject, parentObject, wrapper: null })
  const end = (over: Record<string, unknown>): Record<string, unknown> =>
    ({ type: 'tool_execution_end', toolCallId: 't1', toolName: 'edit', isError: false, ...over })

  it('returns no diff sources for a PRESENT-but-unparseable result diff (renderer shows raw text)', () => {
    // The renderer (PiDiffToolResult) draws the raw diff text in a single <pre> when
    // it can't parse, NOT a structured diff -- so the height/meta path must NOT
    // synthesize a fallback diff from the start args, or the estimate over-sizes the row.
    const payload = end({ result: { details: { diff: 'GARBAGE not-a-numbered-diff' } } })
    expect(piResolveDiffSources(payload, toolUseParsed(startEdit))).toEqual([])
  })

  it('falls back to the start-args diff when the result carries NO diff at all', () => {
    const payload = end({ result: { details: {} } })
    expect(piResolveDiffSources(payload, toolUseParsed(startEdit))).toEqual([
      { filePath: '/repo/x.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' },
    ])
  })

  it('uses the parsed result diff when it is well-formed', () => {
    const diff = [' 1 first', '-2 old', '+2 new', ' 3 third'].join('\n')
    const payload = end({ result: { details: { diff } } })
    const out = piResolveDiffSources(payload, toolUseParsed(startEdit))
    expect(out).toHaveLength(1)
    expect(out[0].filePath).toBe('/repo/x.ts')
    expect(out[0].structuredPatch).not.toBeNull()
  })

  it('returns no diff sources for a failed execution (renders error text)', () => {
    const payload = end({ isError: true, result: { details: { diff: 'GARBAGE' } } })
    expect(piResolveDiffSources(payload, toolUseParsed(startEdit))).toEqual([])
  })
})
