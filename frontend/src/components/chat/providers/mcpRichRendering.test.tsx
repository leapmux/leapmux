import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { toolMessageInput } from '~/components/chat/providers/testUtils'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { pngBase64 } from '~/test-support/pngFixture'
import { renderMessageContent } from '../messageRenderers'
import { providerFor } from './registry'
import { input } from './testUtils'
import './index'
import './testMocks'

const imageData = pngBase64(12, 8)

interface RichToolCase {
  provider: AgentProvider
  request: Record<string, unknown>
  result: Record<string, unknown>
  supplemental?: Record<string, unknown>
}

function richToolCases(blocks: Record<string, unknown>[]): RichToolCase[] {
  const acpCases: RichToolCase[] = [
    {
      provider: AgentProvider.CURSOR,
      request: { kind: 'other', title: 'probe: echo', rawInput: { providerIdentifier: 'probe', toolName: 'echo', args: { query: 'native args' } } },
      result: {},
      supplemental: { rawOutput: {
        content: [{ type: 'tool-result', toolCallId: 'call', toolName: 'mcp_probe_echo', result: 'Rich **body**', experimental_content: blocks }],
        toolArguments: { query: 'native args' },
        providerOptions: { cursor: { highLevelToolCallResult: { output: { success: {} } } } },
      } },
    },
    {
      provider: AgentProvider.GITHUB_COPILOT,
      request: { kind: 'other', title: 'probe-echo', rawInput: { query: 'native args' } },
      result: { rawOutput: { contents: blocks, structuredContent: { count: 0 } } },
    },
    ...[AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX].map(provider => ({
      provider,
      request: { kind: 'other', title: provider === AgentProvider.REASONIX ? 'mcp__probe__echo' : 'probe_echo', rawInput: { query: 'native args' }, ...(provider === AgentProvider.GOOSE ? { _meta: { goose: { toolCall: { toolName: 'probe__echo', extensionName: 'probe' } } } } : {}) },
      result: { content: blocks.map(content => ({ type: 'content', content })) },
    })),
  ]
  return [
    ...acpCases.map(entry => ({ ...entry, request: { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...entry.request }, result: { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...entry.result } })),
    {
      provider: AgentProvider.CLAUDE_CODE,
      request: { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call', name: 'mcp__probe__echo', input: { query: 'native args' } }] } },
      result: { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'call', content: blocks }] } },
    },
    {
      provider: AgentProvider.CODEX,
      request: { item: { id: 'call', type: 'mcpToolCall', status: 'inProgress', server: 'probe', tool: 'echo', arguments: { query: 'native args' } } },
      result: { item: { id: 'call', type: 'mcpToolCall', status: 'completed', server: 'probe', tool: 'echo', arguments: { query: 'native args' }, result: { content: blocks } } },
    },
    {
      provider: AgentProvider.PI,
      request: { type: 'tool_execution_start', toolCallId: 'call', toolName: 'mcp', args: { tool: 'probe_echo', args: { query: 'native args' } } },
      result: { type: 'tool_execution_end', toolCallId: 'call', toolName: 'mcp', result: { content: [], details: { server: 'probe', tool: 'echo', mcpResult: { content: blocks } } } },
    },
  ]
}

describe.each([
  { kind: 'image', image: { type: 'image', data: imageData, mimeType: 'image/png' } },
  { kind: 'resource', image: { type: 'resource', resource: { uri: 'probe://image', blob: imageData, mimeType: 'image/png' } } },
])('rich $kind results across providers', ({ image }) => {
  const cases = richToolCases([{ type: 'text', text: 'Rich **body**' }, image])
  it.each(cases)('renders provider $provider text and images through the shared components', ({ provider, request, result, supplemental }) => {
    const start = input(request)
    const end = result
    const plugin = providerFor(provider)!
    const row = toolMessageInput(end, undefined, start)
    row.parsed.supplementalContent = supplemental ? { sessionUpdate: end.sessionUpdate, status: end.status, toolCallId: end.toolCallId, ...supplemental } : undefined
    const category = plugin.classify(input(end))
    const { container } = render(() => [
      renderMessageContent(start.parentObject, { sources: testMessageSources({ current: () => start, result: () => row.parsed }) }, plugin.classify(start), provider),
      renderMessageContent(end, { sources: testMessageSources({ current: () => row.parsed, request: () => start }) }, category, provider),
    ])
    expect(container.querySelector('strong')?.textContent).toBe('body')
    expect(container.textContent).toContain('native args')
    expect(container.querySelector('img')?.getAttribute('src')).toContain(imageData)
    expect(plugin.toolResultImages?.(row)?.map(image => image.data)).toEqual([imageData])
    if (provider === AgentProvider.CURSOR)
      expect(container.textContent).toContain('probe / echo')
    if (provider === AgentProvider.GITHUB_COPILOT)
      expect(container.textContent).toMatch(/"count"\s*:\s*0/)
  })
})
