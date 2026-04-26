import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './opencode'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

const { renderMessageContent } = await import('../messageRenderers')

function renderClaudeToolResult(parsed: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(parsed, MessageRole.USER, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function makeResult(toolUseResult: Record<string, unknown> | undefined, content: string) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderOpenCodeUpdate(toolUse: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: (toolUse.kind as string) || 'tool_call_update',
    toolUse,
    content: [],
  }
  const result = renderMessageContent(toolUse, MessageRole.ASSISTANT, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

describe('claude Grep tool_result rendering', () => {
  it('renders structured grep summary with file list', () => {
    const parsed = makeResult({
      tool_name: 'Grep',
      numFiles: 2,
      numLines: 0,
      filenames: ['/tmp/a.ts', '/tmp/b.ts'],
      content: '',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Grep' })
    const text = container.textContent ?? ''
    expect(text).toContain('Found 2 files')
    expect(text).toContain('a.ts')
    expect(text).toContain('b.ts')
  })

  it('renders the structured summary "N matches in M files"', () => {
    const parsed = makeResult({
      tool_name: 'Grep',
      numFiles: 3,
      numLines: 5,
      filenames: ['/tmp/a.ts'],
      content: '',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Grep' })
    expect(container.textContent ?? '').toContain('5 matches in 3 files')
  })

  it('renders "No matches found" fallback when nothing matched', () => {
    const parsed = makeResult({
      tool_name: 'Grep',
      numFiles: 0,
      numLines: 0,
      filenames: [],
      content: '',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Grep' })
    expect(container.textContent ?? '').toContain('No matches found')
  })

  it('parses raw subagent output when no tool_use_result', () => {
    const parsed = makeResult(undefined, 'Found 1 file\n/sub.ts')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Grep' })
    const text = container.textContent ?? ''
    expect(text).toContain('Found 1 file')
    expect(text).toContain('sub.ts')
  })
})

describe('claude Glob tool_result rendering', () => {
  it('renders the file list', () => {
    const parsed = makeResult({
      tool_name: 'Glob',
      filenames: ['/tmp/a.ts', '/tmp/b.ts'],
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Glob' })
    const text = container.textContent ?? ''
    expect(text).toContain('Found 2 files')
    expect(text).toContain('a.ts')
  })

  it('renders "No files found" fallback', () => {
    const parsed = makeResult({ tool_name: 'Glob', filenames: [] }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Glob' })
    expect(container.textContent ?? '').toContain('No files found')
  })

  it('falls back to raw subagent output', () => {
    const parsed = makeResult(undefined, 'Found 2 files\n/x\n/y')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Glob' })
    const text = container.textContent ?? ''
    expect(text).toContain('Found 2 files')
    expect(text).toContain('/x')
    expect(text).toContain('/y')
  })
})

describe('opencode search tool_call_update rendering', () => {
  it('renders matches summary via the shared body', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'search',
      status: 'completed',
      title: 'Search foo',
      rawOutput: { metadata: { matches: 4 } },
    })
    expect(container.textContent ?? '').toContain('Found 4 matches')
  })

  it('renders "No matches found" when matches is 0', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'search',
      status: 'completed',
      title: 'Search nope',
      rawOutput: { metadata: { matches: 0 } },
    })
    expect(container.textContent ?? '').toContain('No matches found')
  })
})
