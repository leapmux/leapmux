import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
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
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
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
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
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
  const result = renderMessageContent(payload, {
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

  it('renders a PARTIAL Read (leading <system-reminder>) via the collapsible cat-n body, not plain <pre>', () => {
    // Claude Code prepends a truncation notice to a partial Read. Before the
    // leading-reminder strip the cat-n parse failed and the renderer fell back to an
    // uncollapsible <pre> of the whole file -- so the row rendered full-height while
    // the estimator (correctly) assumed a collapsed 3-row body, a huge delta.
    const content
      = '<system-reminder>[Truncated: PARTIAL view -- showing lines 1-2 of 9 total. Call Read with offset=3 for the next page.]</system-reminder>\n\n1\tpartialLineA\n2\tpartialLineB\n'
    const parsed = makeReadResult(undefined, content)
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read' })
    // The cat-n (collapsible) body is chosen, not the uncollapsible <pre> fallback.
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    const text = container.textContent ?? ''
    expect(text).toContain('partialLineA')
    expect(text).toContain('partialLineB')
    // Collapsed (default): the reminder is captured but not rendered, so the body
    // keeps the height the estimator assumes.
    expect(text).not.toContain('Truncated: PARTIAL view')
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('renders leading + trailing reminders as alerts (with variant) when expanded', () => {
    const content
      = '<system-reminder>[Truncated: PARTIAL view]</system-reminder>\n\n1\tfoo\n2\tbar\n\n<read-error>disk full</read-error>\n'
    const parsed = makeReadResult(undefined, content)
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read', getMessageUiState: () => true })
    const alerts = container.querySelectorAll('[role="alert"]')
    expect(alerts.length).toBe(2)
    // Leading system-reminder -> default info (no data-variant), title-cased label.
    expect(alerts[0].hasAttribute('data-variant')).toBe(false)
    expect(alerts[0].querySelector('strong')?.textContent).toBe('System Reminder')
    expect(alerts[0].textContent).toContain('[Truncated: PARTIAL view]')
    // Trailing read-error -> data-variant="error", "Read Error" label.
    expect(alerts[1].getAttribute('data-variant')).toBe('error')
    expect(alerts[1].querySelector('strong')?.textContent).toBe('Read Error')
    // The file body still renders.
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
  })

  it('escapes HTML in reminder text so markup cannot be injected', () => {
    const content = '<system-reminder>danger <script>alert(1)</script></system-reminder>\n\n1\tfoo'
    const parsed = makeReadResult(undefined, content)
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Read', getMessageUiState: () => true })
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('<script>alert(1)</script>')
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

describe('claude Write/create tool_result rendering', () => {
  it('renders a no-sibling create as the new-file diff, not a bare success line', () => {
    // type:"create" with the whole new file in `content` and no paired tool_use:
    // claudeCreateResultDiff makes the renderer show the diff instead of a one-line
    // success, which was the previous rendering bug.
    const parsed = {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'w1', content: 'File created successfully at: /tmp/new.ts' }] },
      tool_use_result: { type: 'create', filePath: '/tmp/new.ts', content: 'const a = 1\nconst b = 2\nconst c = 3' },
    }
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Write' })
    const text = container.textContent ?? ''
    expect(text).toContain('const a = 1') // the new file body renders as a diff
    expect(text).not.toContain('File created successfully') // not the bare success line
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
