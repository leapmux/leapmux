import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../messageRenderers'
import { toolUseHeader } from '../toolStyles.css'
import { providerFor } from './registry'
import { input } from './testUtils'
import './index'
import './testMocks'

function mcpMessages(provider: AgentProvider) {
  const args = { query: 'rendering' }
  const content = [{ type: 'text', text: '**Result:** Tool rendering.' }]
  if (provider === AgentProvider.CLAUDE_CODE) {
    return {
      spanType: 'mcp__Docs__lookup',
      request: { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call', name: 'mcp__Docs__lookup', input: args }] } },
      result: { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'call', content }] } },
    }
  }
  if (provider === AgentProvider.CODEX) {
    const item = { id: 'call', type: 'mcpToolCall', server: 'Docs', tool: 'lookup', arguments: args }
    return { spanType: 'mcpToolCall', request: { item: { ...item, status: 'inProgress' } }, result: { item: { ...item, status: 'completed', result: { content } } } }
  }
  if (provider === AgentProvider.PI) {
    return {
      spanType: 'lookup',
      request: { type: 'tool_execution_start', toolCallId: 'call', toolName: 'lookup', args },
      result: { type: 'tool_execution_end', toolCallId: 'call', toolName: 'lookup', result: { content }, isError: false },
    }
  }
  if (provider === AgentProvider.ZCODE) {
    return {
      spanType: 'mcp__Docs__lookup',
      request: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'call', toolName: 'mcp__Docs__lookup', input: args } },
      result: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success: true, content: content[0].text, display: { kind: 'mcp_tool', serverName: 'Docs', toolName: 'lookup' } } } },
    }
  }
  return {
    spanType: 'other',
    request: {
      sessionUpdate: 'tool_call',
      toolCallId: 'call',
      kind: 'other',
      status: 'pending',
      title: provider === AgentProvider.REASONIX ? 'mcp__Docs__lookup' : 'lookup',
      rawInput: args,
      ...(provider === AgentProvider.GOOSE ? { _meta: { goose: { toolCall: { toolName: 'Docs__lookup', extensionName: 'Docs' } } } } : {}),
    },
    result: { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', content: content.map(content => ({ type: 'content', content })) },
  }
}

describe.each([
  AgentProvider.CLAUDE_CODE,
  AgentProvider.CODEX,
  AgentProvider.OPENCODE,
  AgentProvider.KILO,
  AgentProvider.GOOSE,
  AgentProvider.REASONIX,
  AgentProvider.CURSOR,
  AgentProvider.GITHUB_COPILOT,
  AgentProvider.PI,
  AgentProvider.ZCODE,
])('paired MCP requests and results (%s)', (provider) => {
  it('renders one header and one argument section for the pair', () => {
    const { request, result, spanType } = mcpMessages(provider)
    const plugin = providerFor(provider)!
    const sources = (current: Record<string, unknown>) => testMessageSources({ current: () => input(current), request: () => input(request), result: () => input(result) })
    const { container } = render(() => [
      renderMessageContent(request, { premeasureMode: true, spanType, sources: sources(request) }, plugin.classify(input(request)), provider),
      renderMessageContent(result, { premeasureMode: true, spanType, sources: sources(result) }, plugin.classify(input(result)), provider),
    ])
    expect(container.querySelectorAll(`.${toolUseHeader}`)).toHaveLength(1)
    expect(container.textContent?.match(/Arguments/g)).toHaveLength(1)
    expect(container.textContent?.match(/"query"/g)).toHaveLength(1)
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('"rendering"')
    expect(container.querySelector('strong')?.textContent).toBe('Result:')
  })
})

it('identifies Codex request and result roles before either counterpart loads', () => {
  const { request, result } = mcpMessages(AgentProvider.CODEX)
  const plugin = providerFor(AgentProvider.CODEX)!
  expect(plugin.spanRole?.(input(request))).toBe('opener')
  expect(plugin.spanRole?.(input(result))).toBe('result')
  expect(plugin.relatedMessages?.(input(result))).toEqual(['request'])
})

it('does not use arguments from a different Pi tool call', () => {
  const { request, result, spanType } = mcpMessages(AgentProvider.PI)
  const other = { ...request, toolCallId: 'other-call', args: { query: 'foreign argument' } }
  const plugin = providerFor(AgentProvider.PI)!
  const { container } = render(() => renderMessageContent(result, {
    premeasureMode: true,
    spanType,
    sources: testMessageSources({ request: () => input(other) }),
  }, plugin.classify(input(result)), AgentProvider.PI))
  expect(container.textContent).not.toContain('foreign argument')
  expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('lookup')
})

it('does not use a Claude request from another call', () => {
  const { result, spanType } = mcpMessages(AgentProvider.CLAUDE_CODE)
  const other = { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'other-call', name: spanType, input: { query: 'foreign argument' } }] } }
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
  const { container } = render(() => renderMessageContent(result, {
    premeasureMode: true,
    spanType,
    sources: testMessageSources({ request: () => input(other) }),
  }, plugin.classify(input(result)), AgentProvider.CLAUDE_CODE))
  expect(container.textContent).not.toContain('foreign argument')
  expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('lookup')
})

it.each([AgentProvider.PI, AgentProvider.ZCODE])('does not repeat a synthetic failure message (%s)', (provider) => {
  const { request, spanType } = mcpMessages(provider)
  const result = provider === AgentProvider.PI
    ? { type: 'tool_execution_end', toolCallId: 'call', toolName: 'lookup', isError: true, result: { content: [{ type: 'text', text: 'Access denied' }] } }
    : { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success: false, content: 'Access denied', display: { kind: 'mcp_tool', serverName: 'Docs', toolName: 'lookup' } } } }
  const plugin = providerFor(provider)!
  const { container } = render(() => renderMessageContent(result, { premeasureMode: true, spanType, sources: testMessageSources({ request: () => input(request) }) }, plugin.classify(input(result)), provider))
  expect(container.textContent).toContain('Failed')
  expect(container.textContent).toContain('Access denied')
  expect(container.textContent).not.toContain('Tool call failed')
})

describe('retained MCP completion', () => {
  it.each([AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.CURSOR, AgentProvider.GITHUB_COPILOT, AgentProvider.PI, AgentProvider.ZCODE])('renders one interruption header for provider %s', (provider) => {
    const { request, result, spanType } = mcpMessages(provider)
    const original = JSON.stringify(result)
    const parsed = { ...input(result, null, provider), completion: MessageCompletion.INTERRUPTED }
    const plugin = providerFor(provider)!
    const { container } = render(() => renderMessageContent(result, {
      premeasureMode: true,
      spanType,
      sources: testMessageSources({ current: () => parsed, request: () => input(request) }),
    }, plugin.classify(parsed), provider, MessageCompletion.INTERRUPTED))
    expect(container.textContent?.match(/Interrupted/g)).toHaveLength(1)
    expect(container.textContent).not.toContain('Text truncated')
    expect(container.textContent).not.toContain('Failed')
    expect(container.textContent).toContain('Tool rendering.')
    expect(JSON.stringify(result)).toBe(original)
  })
})
