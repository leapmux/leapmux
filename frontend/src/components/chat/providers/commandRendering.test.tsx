import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { claudeToolResultMeta } from './claude/toolResult'
import { codexToolResultMeta } from './codex/toolResult'
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

describe('command result \\r progress normalization', () => {
  it('claude Bash: 4 \\r-separated progress segments render verbatim with no ellipsis', () => {
    // Default 3-row collapse would hide the tail; the threshold widening for
    // hadCarriageReturns must keep all 4 lines visible.
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'Rebasing (1/4)\rRebasing (2/4)\rRebasing (3/4)\rDone',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    const text = container.textContent ?? ''
    expect(text).toContain('Rebasing (1/4)')
    expect(text).toContain('Rebasing (2/4)')
    expect(text).toContain('Rebasing (3/4)')
    expect(text).toContain('Done')
    expect(text).not.toContain('…')
  })

  it('claude Bash: 8 \\r-separated progress segments collapse to head 3 + … + tail 3', () => {
    const stdout = ['s1', 's2', 's3', 's4', 's5', 's6', 's7', 's8'].join('\r')
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    const text = container.textContent ?? ''
    expect(text).toContain('s1')
    expect(text).toContain('s2')
    expect(text).toContain('s3')
    expect(text).toContain('…')
    expect(text).toContain('s6')
    expect(text).toContain('s7')
    expect(text).toContain('s8')
    expect(text).not.toContain('s4')
    expect(text).not.toContain('s5')
  })
})

// Regression for "no expand/collapse button on rebase output". The body
// normalizes \r-overwrites into separate lines (so a 3-raw-line stdout can
// render as 9 lines), but `meta.collapsible` was counting raw \n only — so
// the toolbar's expand button never appeared over output the body actually
// clipped. These tests pin the post-normalize line count into the meta so
// the toolbar and body agree.
describe('command result collapsibility accounts for \\r-normalized line count', () => {
  it('claude Bash: rebase progress (3 raw lines, 9 normalized lines) is reported as collapsible', () => {
    const stdout = 'From github.com:leapmux/leapmux\n * branch              main       -> FETCH_HEAD\nRebasing (1/6)\rRebasing (2/6)\rRebasing (3/6)\rRebasing (4/6)\rRebasing (5/6)\rRebasing (6/6)\rSuccessfully rebased and updated refs/heads/grid-layout.'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(true)
  })

  it('claude Bash: short \\r progress (no clip) is NOT reported as collapsible', () => {
    // 4 \r-segments → 4 normalized lines. Threshold widens to 7 because of
    // the \r, so 4 ≤ 7 means the body shows everything; meta should agree.
    const stdout = 'Rebasing (1/4)\rRebasing (2/4)\rRebasing (3/4)\rDone'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(false)
  })

  it('claude Bash: plain output preserves the standard 3-row collapse threshold', () => {
    const stdout = 'a\nb\nc\nd'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(true)
  })

  it('claude Bash: plain output at the threshold (3 lines, no \\r) is NOT collapsible', () => {
    const stdout = 'a\nb\nc'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(false)
  })

  it('codex commandExecution: rebase-style \\r progress is reported as collapsible', () => {
    const aggregatedOutput = 'From origin\n * branch    main       -> FETCH_HEAD\nRebasing (1/6)\rRebasing (2/6)\rRebasing (3/6)\rRebasing (4/6)\rRebasing (5/6)\rRebasing (6/6)\rDone.'
    const meta = codexToolResultMeta(
      { kind: 'tool_use', toolName: 'commandExecution', toolUse: {}, content: [] },
      { item: { type: 'commandExecution', status: 'completed', aggregatedOutput, exitCode: 0 } },
      'commandExecution',
      undefined,
    )
    expect(meta?.collapsible).toBe(true)
  })
})
