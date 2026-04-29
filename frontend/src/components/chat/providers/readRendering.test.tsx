import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './opencode'
import './pi'
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

function makeReadResult(toolUseResult: Record<string, unknown> | undefined, content: string) {
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

function parsed(parentObject: Record<string, unknown>) {
  return {
    rawText: JSON.stringify(parentObject),
    topLevel: parentObject,
    parentObject,
    wrapper: null,
  }
}

function renderPiReadResult(content: string, startArgs: Record<string, unknown> = {}) {
  const payload = {
    type: 'tool_execution_end',
    toolCallId: 'call-1',
    toolName: 'read',
    result: { content: [{ type: 'text', text: content }] },
  }
  const start = {
    type: 'tool_execution_start',
    toolCallId: 'call-1',
    toolName: 'read',
    args: startArgs,
  }
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(payload, MessageRole.ASSISTANT, {
    spanType: 'read',
    toolUseParsed: parsed(start),
  }, category, AgentProvider.PI)
  return render(() => result)
}

describe('claude Read tool_result rendering', () => {
  it('renders structured file payload via the shared body', () => {
    const parsed = makeReadResult({
      tool_name: 'Read',
      type: 'text',
      file: {
        filePath: '/tmp/a.ts',
        content: 'foo\nbar',
        startLine: 1,
        totalLines: 2,
        numLines: 2,
      },
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read' })
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('foo')
    expect(text).toContain('bar')
  })

  it('parses raw cat-n content when no structured payload (subagent fallback)', () => {
    const parsed = makeReadResult(undefined, '1\tsubagentLineA\n2\tsubagentLineB\n')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read' })
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('subagentLineA')
    expect(text).toContain('subagentLineB')
  })

  it('falls back to plain text when raw content is not cat-n', () => {
    const parsed = makeReadResult(undefined, 'not parseable as cat-n')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read' })
    expect(container.querySelector('[class*="codeView"]')).toBeNull()
    expect(container.textContent ?? '').toContain('not parseable as cat-n')
  })

  it('skips the structured Read body for non-text variants (image)', () => {
    // Image variant: extractor returns null, fallback path renders content as-is.
    const parsed = makeReadResult({
      tool_name: 'Read',
      type: 'image',
    }, '[image data]')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read' })
    expect(container.querySelector('[class*="codeView"]')).toBeNull()
    expect(container.textContent ?? '').toContain('[image data]')
  })
})

describe('pi Read tool_result rendering', () => {
  it('renders plain Pi read content through the shared line-numbered body', () => {
    const { container } = renderPiReadResult('piLineA\npiLineB', { path: '/tmp/a.ts', offset: 10 })
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    expect(container.querySelector('[data-line-num="10"]')).not.toBeNull()
    expect(container.querySelector('[data-line-num="11"]')).not.toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('piLineA')
    expect(text).toContain('piLineB')
  })
})

describe('opencode Read tool_call_update rendering', () => {
  it('renders cat-n output via the shared syntax-highlighted body', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'read',
      status: 'completed',
      title: 'Read /tmp/a.ts',
      rawInput: { filePath: '/tmp/a.ts' },
      content: [
        { type: 'content', content: { text: '1\tacpReadLineA\n2\tacpReadLineB\n' } },
      ],
    })
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('acpReadLineA')
    expect(text).toContain('acpReadLineB')
  })

  it('falls back to plain text when output is not cat-n', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'read',
      status: 'completed',
      title: 'Read /tmp/a.ts',
      rawInput: { filePath: '/tmp/a.ts' },
      content: [
        { type: 'content', content: { text: 'plain non-cat-n text' } },
      ],
    })
    expect(container.querySelector('[class*="codeView"]')).toBeNull()
    expect(container.textContent ?? '').toContain('plain non-cat-n text')
  })
})
