import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { extractPiEdit, extractPiRead, extractPiWrite, piResolveDiffSources, resolvePiResultDiff } from './fileEdit'

describe('extractPiEdit', () => {
  it.each([
    { oldText: 'before', newText: 'after' },
    { edits: { oldText: 'before', newText: 'after' } },
    { edits: JSON.stringify([{ oldText: 'before', newText: 'after' }]) },
    { edits: JSON.stringify({ oldText: 'before', newText: 'after' }) },
  ])('accepts the edit form that Pi normalizes before execution: %j', (args) => {
    const result = extractPiEdit({ type: 'tool_execution_start', toolCallId: 'call', toolName: 'edit', args: { path: '/project/file.ts', ...args } })
    expect(result?.sources).toEqual([{ filePath: '/project/file.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }])
  })

  it('combines an edits array with the legacy singleton fields', () => {
    const result = extractPiEdit({ type: 'tool_execution_start', toolCallId: 'call', toolName: 'edit', args: {
      path: '/project/file.ts',
      edits: [{ oldText: 'one', newText: 'two' }],
      oldText: 'three',
      newText: '',
    } })
    expect(result?.sources.map(source => [source.oldStr, source.newStr])).toEqual([['one', 'two'], ['three', '']])
  })

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
  it('uses a request path that becomes available after the first diff lookup', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'edit', result: { details: { diff: '-1 before\n+1 after' } } }
    expect(resolvePiResultDiff(payload, {}).source?.filePath).toBe('')
    expect(resolvePiResultDiff(payload, { path: '/project/late.ts' }).source?.filePath).toBe('/project/late.ts')
  })

  it('prefers the applied standard patch over the display-oriented numbered diff', () => {
    const payload = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'edit', result: { details: {
      diff: '-1 displayBefore\n+1 displayAfter',
      patch: '--- a/file.ts\n+++ b/file.ts\n@@ -7 +7 @@\n-actualBefore\n+actualAfter\n',
    } } }
    const source = resolvePiResultDiff(payload, { path: '/project/file.ts' }).source
    expect(source?.structuredPatch?.[0]).toMatchObject({ oldStart: 7, newStart: 7, lines: ['-actualBefore', '+actualAfter'] })
  })

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
