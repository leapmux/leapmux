import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { describe, expect, it } from 'vitest'
import { acpToolPresentation } from '../acp/toolPresentation'
import { cursorToolAdapter } from './toolPresentation'

const CALL = 'cursor-tool'

function model(tool: Record<string, unknown>, supplemental?: Record<string, unknown>): ToolPresentation {
  const call = { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...tool }
  const extra = supplemental
    ? { sessionUpdate: call.sessionUpdate, toolCallId: call.toolCallId, status: call.status, ...supplemental }
    : undefined
  return acpToolPresentation(call, cursorToolAdapter, extra)
}

function searchVariant(presentation: ToolPresentation): string | undefined {
  return presentation.body.type === 'search' ? presentation.body.source.variant : undefined
}

describe('cursorToolAdapter search identity', () => {
  // The rendered title and the result counters both describe the search, and they
  // disagree for a file search whose result also states a match total. The title is
  // the stronger statement, so the counters no longer overrule it.
  it.each([
    ['Find', { totalMatches: 4 }],
    ['Find `*.ts`', { totalMatches: 4 }],
    ['Find', { resultCount: 4 }],
  ])('keeps a file search a file search when the title states one (%s)', (title, raw) => {
    const presentation = model({ kind: 'search', title, rawInput: { pattern: '*.ts' }, rawOutput: raw })
    expect(presentation.kind).toBe('glob')
    expect(searchVariant(presentation)).toBe('glob')
  })

  it.each([
    ['grep', { totalFiles: 3 }],
    ['grep "needle"', { totalFiles: 3, totalMatches: 9 }],
  ])('keeps a content search a content search when the title states one (%s)', (title, raw) => {
    const presentation = model({ kind: 'search', title, rawInput: { pattern: 'needle' }, rawOutput: raw })
    expect(presentation.kind).toBe('grep')
    expect(searchVariant(presentation)).toBe('search')
  })

  // Cursor builds the grep title from the arguments: `grep`, then one flag for each
  // argument, then the quoted pattern last. A flag moves the pattern away from the front,
  // so a match on `grep "` alone misses every grep call that carries one. Each case here
  // also states the file-search counter, so only the title can produce a content search.
  it.each([
    'grep -i "needle"',
    'grep -n -A 3 "needle"',
    'grep -l',
    'grep --include="*.ts" "needle"',
    'grep | head -20 "needle"',
  ])('reads a content search from a grep title that carries a flag (%s)', (title) => {
    const presentation = model({ kind: 'search', title, rawInput: { pattern: 'needle' }, rawOutput: { totalFiles: 3 } })
    expect(presentation.kind).toBe('grep')
    expect(searchVariant(presentation)).toBe('search')
  })

  // A call that has not finished carries no counter, and a failed one reports its error
  // alone. The title is the only statement left in both states.
  it('reads a content search from a flagged grep title before the call finishes', () => {
    const presentation = model({ sessionUpdate: 'tool_call', kind: 'search', status: 'pending', title: 'grep -l "needle"', rawInput: { pattern: 'needle' } })
    expect(presentation.kind).toBe('grep')
  })

  it('reads a content search from a flagged grep title when the call failed', () => {
    const presentation = model({ kind: 'search', status: 'failed', title: 'grep -i "needle"', rawInput: { pattern: 'needle' }, rawOutput: { error: 'no such path' } })
    expect(presentation.kind).toBe('grep')
    expect(presentation.output).toBe('no such path')
  })

  // A file-name search states its path, its pattern, both, or neither.
  it.each([
    'Find',
    'Find `src`',
    'Find `*.ts`',
    'Find `src` `*.ts`',
  ])('reads a file search from every title the runtime composes (%s)', (title) => {
    const presentation = model({ kind: 'search', title, rawInput: { pattern: '*.ts' }, rawOutput: { totalMatches: 9 } })
    expect(presentation.kind).toBe('glob')
    expect(searchVariant(presentation)).toBe('glob')
  })

  it('falls back to the counters when the title states neither shape', () => {
    const glob = model({ kind: 'search', title: 'Search', rawInput: { pattern: '*.ts' }, rawOutput: { totalFiles: 3 } })
    expect(glob.kind).toBe('glob')
    expect(searchVariant(glob)).toBe('glob')
    const grep = model({ kind: 'search', title: 'Search', rawInput: { pattern: 'needle' }, rawOutput: { totalMatches: 9 } })
    expect(grep.kind).toBe('grep')
    expect(searchVariant(grep)).toBe('search')
  })

  it('leaves the kind alone when neither the title nor a counter states a shape', () => {
    expect(model({ kind: 'search', title: 'Search', rawInput: { pattern: 'needle' }, rawOutput: {} }).kind).toBe('search')
  })

  it('reads no counter from a call that has not finished', () => {
    const presentation = model({ sessionUpdate: 'tool_call', kind: 'search', status: 'pending', title: 'Search', rawInput: { pattern: 'needle' }, rawOutput: { totalFiles: 3 } })
    expect(presentation.kind).toBe('search')
  })
})

describe('cursorToolAdapter protocol errors', () => {
  it('states the protocol error when a saved result restores no output', () => {
    const presentation = model({
      status: 'failed',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
      rawOutput: { error: 'server unavailable' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: '' }] },
    })
    expect(presentation.body.type).toBe('mcp')
    expect(presentation.output).toBe('server unavailable')
  })

  it('keeps the restored output when the saved result carries one', () => {
    const presentation = model({
      status: 'failed',
      kind: 'other',
      title: 'mcp_server_lookup',
      rawInput: { toolName: 'lookup', providerIdentifier: 'server' },
      rawOutput: { error: 'server unavailable' },
    }, {
      rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName: 'mcp_server_lookup', result: 'partial answer' }] },
    })
    expect(presentation.output).toBe('partial answer')
  })
})
