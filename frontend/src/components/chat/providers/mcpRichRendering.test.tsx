import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '~/lib/jsonPick'
import { copilotToolComplete, copilotToolStart } from '~/test-support/copilotFixtures'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { openingFrame, toolFrame } from '~/test-support/mimoFixtures'
import { pngBase64 } from '~/test-support/pngFixture'
import { providerRowImages } from '~/test-support/toolCallFixture'
import { renderMessageContent } from '../messageContentRenderer'
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

/** The output MiMo writes for a Model Context Protocol result: its text blocks, joined. */
function mimoOutput(blocks: Record<string, unknown>[]): string {
  return blocks.flatMap(block => block.type === 'text' && typeof block.text === 'string' ? [block.text] : []).join('\n\n')
}

/** The file attachments MiMo writes for the images and binary resources of a result. */
function mimoAttachments(blocks: Record<string, unknown>[]): Record<string, unknown>[] {
  return blocks.flatMap((block): Record<string, unknown>[] => {
    if (block.type === 'image' && typeof block.data === 'string' && typeof block.mimeType === 'string')
      return [{ type: 'file', mime: block.mimeType, url: `data:${block.mimeType};base64,${block.data}` }]
    const resource = pickObject(block, 'resource')
    const mime = pickString(resource, 'mimeType')
    const blob = pickString(resource, 'blob')
    if (block.type === 'resource' && mime && blob)
      return [{ type: 'file', mime, url: `data:${mime};base64,${blob}`, filename: pickString(resource, 'uri') }]
    return []
  })
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
    ...[AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.GOOSE, AgentProvider.REASONIX].map(provider => ({
      provider,
      request: { kind: 'other', title: provider === AgentProvider.REASONIX ? 'mcp__probe__echo' : 'probe_echo', rawInput: { query: 'native args' }, ...(provider === AgentProvider.GOOSE ? { _meta: { goose: { toolCall: { toolName: 'probe__echo', extensionName: 'probe' } } } } : {}) },
      result: { content: blocks.map(content => ({ type: 'content', content })) },
    })),
    // Grok states an MCP tool in its identity, and Qwen in `_meta.toolName`.
    {
      provider: AgentProvider.GROK_BUILD,
      request: { title: 'probe__echo', rawInput: { query: 'native args' }, _meta: { 'x.ai/tool': { version: 1, name: 'probe__echo', namespace: 'mcp' } } },
      result: { content: blocks.map(content => ({ type: 'content', content })) },
    },
    // Kiro states an MCP tool as `@server/tool` in its title.
    {
      provider: AgentProvider.KIRO,
      request: { kind: 'other', title: '@probe/echo', rawInput: { query: 'native args' }, _meta: { kiro: { serverName: 'probe', toolOrigin: 'client' } } },
      result: { content: blocks.map(content => ({ type: 'content', content })) },
    },
    {
      provider: AgentProvider.QWEN_CODE,
      request: { kind: 'other', title: 'probe echo', rawInput: { query: 'native args' }, _meta: { toolName: 'mcp__probe__echo' } },
      result: { content: blocks.map(content => ({ type: 'content', content })), _meta: { toolName: 'mcp__probe__echo' } },
    },
  ]
  return [
    ...acpCases.map(entry => ({ ...entry, request: { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...entry.request }, result: { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...entry.result } })),
    {
      // Copilot speaks its own native protocol: the pair is a start event and a
      // completion event, and rich content rides `result.contents`.
      provider: AgentProvider.GITHUB_COPILOT,
      request: copilotToolStart('call', 'probe-echo', { query: 'native args' }),
      result: copilotToolComplete('call', { result: { contents: blocks, structuredContent: { count: 0 } } }),
    },
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
      // MiMo folds the result the way `src/mcp/tool-result.ts` does: the text blocks
      // become the output, and each image or binary resource becomes a file attachment.
      provider: AgentProvider.MIMO_CODE,
      request: openingFrame('probe_echo', { query: 'native args' }, 'call'),
      result: toolFrame('probe_echo', { input: { query: 'native args' }, output: mimoOutput(blocks), attachments: mimoAttachments(blocks), metadata: { mcp: { isError: false } } }, 'call'),
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
    const parsed = input(end)
    parsed.supplementalContent = supplemental ? { sessionUpdate: end.sessionUpdate, status: end.status, toolCallId: end.toolCallId, ...supplemental } : undefined
    const category = plugin?.transcript.classify(input(end))
    const { container } = render(() => [
      renderMessageContent(start.parentObject, { sources: testMessageSources({ current: () => start, result: () => parsed }) }, plugin?.transcript.classify(start), provider),
      renderMessageContent(end, { sources: testMessageSources({ current: () => parsed, request: () => start }) }, category, provider),
    ])
    expect(container.querySelector('strong')?.textContent).toBe('body')
    expect(container.textContent).toContain('native args')
    expect(container.querySelector('img')?.getAttribute('src')).toContain(imageData)
    expect(providerRowImages(provider, end, { category, request: start, span: { request: start, result: parsed, role: 'result', visibleRows: { request: true, result: true } } }).map(item => item.data)).toEqual([imageData])
    if (provider === AgentProvider.CURSOR)
      expect(container.textContent).toContain('probe / echo')
    if (provider === AgentProvider.GITHUB_COPILOT)
      expect(container.textContent).toMatch(/"count"\s*:\s*0/)
  })
})
