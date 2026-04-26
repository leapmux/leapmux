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

describe('claude WebFetch tool_result rendering', () => {
  it('renders HTTP status and body via the shared body', () => {
    const parsed = makeResult({
      tool_name: 'WebFetch',
      code: 200,
      codeText: 'OK',
      bytes: 1024,
      durationMs: 200,
      result: '# Hello WebFetchBody',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'WebFetch' })
    const text = container.textContent ?? ''
    expect(text).toContain('200')
    expect(text).toContain('OK')
    expect(text).toContain('Hello WebFetchBody')
  })

  it('falls through to text rendering when tool_use_result lacks code', () => {
    const parsed = makeResult({ tool_name: 'WebFetch' }, 'plain webfetch fallback')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'WebFetch' })
    expect(container.textContent ?? '').toContain('plain webfetch fallback')
  })
})

describe('opencode fetch tool_call_update rendering', () => {
  it('renders the shared WebFetchResultBody when rawOutput carries code', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'fetch',
      status: 'completed',
      title: 'Fetch https://example.com',
      rawOutput: {
        code: 200,
        codeText: 'OK',
        bytes: 1024,
        durationMs: 200,
        result: '# Hello AcpWebFetchBody',
      },
    })
    const text = container.textContent ?? ''
    expect(text).toContain('200')
    expect(text).toContain('OK')
    expect(text).toContain('Hello AcpWebFetchBody')
  })

  it('falls back to the generic text branch when no recognizable HTTP shape', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'fetch',
      status: 'completed',
      title: 'Fetch https://example.com',
      content: [
        { type: 'content', content: { text: 'opaque acp fetch output' } },
      ],
    })
    expect(container.textContent ?? '').toContain('opaque acp fetch output')
  })
})
