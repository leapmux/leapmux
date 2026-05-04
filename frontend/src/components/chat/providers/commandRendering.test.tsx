import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './codex'
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
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function makeBashResult(toolUseResult: Record<string, unknown> | undefined, content: string, isError = false) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content, is_error: isError }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderCodexItem(item: Record<string, unknown>, context?: RenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const toolName = String(item.type ?? 'codex')
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName,
    toolUse: parsed,
    content: [],
  }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderOpenCodeUpdate(toolUse: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: (toolUse.kind as string) || 'tool_call_update',
    toolUse,
    content: [],
  }
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

describe('canonical command status label across providers', () => {
  it('claude Bash with isError but no exit code renders "Error"', () => {
    const parsed = makeBashResult({ tool_name: 'Bash', stdout: '', stderr: 'oh no' }, '', true)
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('Error')
    expect(container.textContent ?? '').not.toContain('exit')
  })

  it('codex commandExecution with exit 5 renders "Error (exit 5)"', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'false',
      aggregatedOutput: 'failed',
      exitCode: 5,
      status: 'completed',
    })
    expect(container.textContent ?? '').toContain('Error (exit 5)')
  })

  it('openCode execute with exit 5 renders "Error (exit 5)"', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'execute',
      status: 'completed',
      title: 'Run something',
      rawInput: { command: 'false' },
      rawOutput: { metadata: { exit: 5 } },
      content: [{ type: 'content', content: { text: 'failed' } }],
    })
    expect(container.textContent ?? '').toContain('Error (exit 5)')
  })
})

describe('claude Bash interrupted renders "Interrupted"', () => {
  it('renders the Interrupted status when tool_use_result.interrupted is true', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'partial',
      interrupted: true,
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('Interrupted')
  })
})

describe('claude Bash success hides the success status row', () => {
  it('renders stdout without a Success label', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'all good',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('all good')
    expect(container.textContent ?? '').not.toContain('Success')
  })
})

describe('command result with empty output shows a hint with duration/exit', () => {
  it('codex commandExecution: empty aggregatedOutput renders [no output] · duration · exit', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'bun eslint src',
      aggregatedOutput: null,
      exitCode: 0,
      durationMs: 994,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('[no output]')
    expect(text).toContain('994ms')
    expect(text).toContain('exit 0')
  })

  it('codex commandExecution: empty output without duration omits the duration part', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'true',
      aggregatedOutput: '',
      exitCode: 0,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('[no output]')
    expect(text).toContain('exit 0')
    expect(text).not.toContain('ms')
  })

  it('codex commandExecution: non-empty output does not render [no output]', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'echo hi',
      aggregatedOutput: 'hi',
      exitCode: 0,
      durationMs: 12,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).not.toContain('[no output]')
    expect(text).toContain('hi')
  })

  it('claude Bash: empty stdout renders [no output] hint', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: '',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('[no output]')
  })
})
