import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from './registry'
import { input, toolMessageInput } from './testUtils'
import './index'
import './testMocks'

function claudeResult(content: unknown[], toolUseResult?: Record<string, unknown>) {
  return {
    type: 'user',
    message: { content: [{ type: 'tool_result', tool_use_id: 'call', content }] },
    tool_use_result: toolUseResult,
  }
}

describe('tool metadata matches rendered output', () => {
  it('measures the displayed Claude file list without its raw summary line', () => {
    const meta = providerFor(AgentProvider.CLAUDE_CODE)!.toolResultMeta?.({ kind: 'tool_result' }, toolMessageInput(
      claudeResult([{ type: 'text', text: 'Found 3 files\n/project/a\n/project/b\n/project/c' }]),
      'Glob',
    ))
    expect(meta?.collapsible).toBe(false)
  })
  it('keeps valid Codex file changes when an array contains malformed entries', () => {
    const diff = '@@ -1 +1 @@\n-before\n+after'
    const meta = providerFor(AgentProvider.CODEX)!.toolResultMeta?.({ kind: 'tool_use', toolName: 'fileChange', toolUse: {}, content: [] }, toolMessageInput({
      item: { type: 'fileChange', status: 'completed', changes: [null, 'invalid', { path: '/project/file.ts', diff }] },
    }, 'fileChange'))
    expect(meta).toMatchObject({ hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(diff)
  })

  it('allows expansion of long Claude MCP arguments from the linked request', () => {
    const args = Object.fromEntries(Array.from({ length: 8 }, (_, index) => [`field${index}`, 'long argument value '.repeat(4)]))
    const request = input({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call', name: 'mcp__docs__read', input: args }] } })
    const meta = providerFor(AgentProvider.CLAUDE_CODE)!.toolResultMeta?.(
      { kind: 'tool_result' },
      toolMessageInput(claudeResult([]), 'mcp__docs__read', request),
    )
    expect(meta?.collapsible).toBe(true)
  })

  it('copies Claude MCP resource text and structured zero values', () => {
    const meta = providerFor(AgentProvider.CLAUDE_CODE)!.toolResultMeta?.({ kind: 'tool_result' }, toolMessageInput(
      claudeResult([{ type: 'resource', resource: { uri: 'probe://note', text: 'Resource contents' } }], { structuredContent: { count: 0 } }),
      'mcp__docs__read',
    ))
    expect(meta?.hasCopyable).toBe(true)
    expect(meta?.copyableContent()).toContain('Resource contents')
    expect(meta?.copyableContent()).toMatch(/"count"\s*:\s*0/)
  })

  it('offers a copy action for a structured-only Codex MCP result', () => {
    const meta = providerFor(AgentProvider.CODEX)!.toolResultMeta?.({ kind: 'tool_use', toolName: 'mcpToolCall', toolUse: {}, content: [] }, toolMessageInput({
      item: { type: 'mcpToolCall', id: 'call', status: 'completed', server: 'docs', tool: 'read', result: { content: [], structuredContent: { count: 0 } } },
    }, 'mcpToolCall'))
    expect(meta?.hasCopyable).toBe(true)
    expect(meta?.copyableContent()).toMatch(/"count"\s*:\s*0/)
  })

  it('uses ZCode task display output for expansion and copying', () => {
    const output = 'first\nsecond\nthird\nfourth'
    const meta = providerFor(AgentProvider.ZCODE)!.toolResultMeta?.({ kind: 'tool_result' }, toolMessageInput({
      type: 'tool.updated',
      payload: { kind: 'result', toolCallId: 'call', result: { success: true, content: '', display: { kind: 'task_output', output, taskStatus: 'completed' } } },
    }, 'TaskOutput'))
    expect(meta).toMatchObject({ collapsible: true, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(output)
  })
})
