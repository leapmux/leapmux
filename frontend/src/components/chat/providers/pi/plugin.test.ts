import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the Pi plugin.
import './plugin'

describe('pi plugin metadata', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('registers the Pi provider', () => {
    expect(plugin).toBeDefined()
  })

  it('exposes attachment capabilities (text + image only)', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('defaults to the gpt-5.5 model', () => {
    expect(plugin.defaultModel).toBe('gpt-5.5')
  })

  it('defaults to the auto thinking sentinel so Pi keeps its configured level', () => {
    expect(plugin.defaultEffort).toBe('auto')
  })

  it('does not advertise a permission mode for Pi', () => {
    expect(plugin.defaultPermissionMode).toBeUndefined()
    expect(plugin.bypassPermissionMode).toBeUndefined()
  })

  it('builds a Pi abort RPC for interrupt', () => {
    expect(plugin.buildInterruptContent?.('any-session', 'turn-1')).toBe(JSON.stringify({ type: 'abort' }))
  })
})

describe('pi classify', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('hides lifecycle markers without chat UI', () => {
    for (const t of ['agent_start', 'turn_start', 'turn_end', 'message_start', 'tool_execution_update']) {
      expect(plugin.classify(input({ type: t }))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies agent_end as result_divider', () => {
    expect(plugin.classify(input({ type: 'agent_end', messages: [] }))).toEqual({ kind: 'result_divider' })
  })

  it('renders agent_end result_divider via renderMessage', () => {
    const result = plugin.renderMessage!(
      { kind: 'result_divider' },
      { type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'stop' }] },
    )
    expect(result).not.toBeNull()
  })

  it('classifies message_end with text content as assistant_text', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'text', text: 'hello' }] },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('classifies message_end with only thinking content as assistant_thinking', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'reasoning' }] },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_thinking' })
  })

  it('hides signature-only thinking blocks so tool-call message_end rows do not render empty thinking bubbles', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'thinking', thinking: '', thinkingSignature: '{"id":"rs_1"}' },
        { type: 'toolCall', id: 'call-1', name: 'read', arguments: { path: '/tmp/a.ts' } },
      ] },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end with only empty thinking content', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: '', thinkingSignature: 'sig' }] },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end echoes for user prompts (LeapMux already persists the user_content row)', () => {
    const parent = {
      type: 'message_end',
      message: {
        role: 'user',
        content: [{ type: 'text', text: 'Hi. Who are you?' }],
      },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end echoes for tool results (rendered via tool_execution_end span)', () => {
    const parent = {
      type: 'message_end',
      message: {
        role: 'toolResult',
        toolCallId: 'call-1',
        toolName: 'bash',
        content: [{ type: 'text', text: 'output' }],
      },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end echoes for bash executions (host-driven, never enters chat)', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'bashExecution', command: 'ls', output: 'a\nb' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies message_end with both thinking and text as assistant_text', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'thinking', thinking: 'first' },
        { type: 'text', text: 'second' },
      ] },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('classifies tool_execution_start as tool_use with the tool name', () => {
    const parent = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'ls' },
    }
    const result = plugin.classify(input(parent))
    expect(result.kind).toBe('tool_use')
    if (result.kind === 'tool_use') {
      expect(result.toolName).toBe('bash')
      expect(result.toolUse).toBe(parent)
    }
  })

  it('classifies tool_execution_end as tool_result', () => {
    const parent = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      result: { content: [{ type: 'text', text: 'done' }], details: {} },
    }
    const result = plugin.classify(input(parent))
    expect(result.kind).toBe('tool_result')
  })

  it('classifies compaction events as notification', () => {
    expect(plugin.classify(input({ type: 'compaction_start', reason: 'threshold' })).kind).toBe('notification')
    expect(plugin.classify(input({ type: 'compaction_end', reason: 'threshold' })).kind).toBe('notification')
  })

  it('classifies auto_retry events as notification', () => {
    expect(plugin.classify(input({ type: 'auto_retry_start' })).kind).toBe('notification')
    expect(plugin.classify(input({ type: 'auto_retry_end' })).kind).toBe('notification')
  })

  it('classifies extension_error as notification', () => {
    expect(plugin.classify(input({ type: 'extension_error', error: 'boom' })).kind).toBe('notification')
  })

  it('classifies extension_ui_request as notification (frontend dialog goes via control flow)', () => {
    expect(plugin.classify(input({ type: 'extension_ui_request', method: 'select' })).kind).toBe('notification')
  })

  it('classifies user echo content as user_content', () => {
    expect(plugin.classify(input({ role: 'user', content: 'hello' })).kind).toBe('user_content')
  })

  it('falls back to unknown for unrecognized shapes', () => {
    expect(plugin.classify(input({ type: 'something_else' })).kind).toBe('unknown')
  })
})

describe('pi toolResultMeta', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('marks Bash results collapsible using the rendered command output', () => {
    const resultText = 'one\ntwo\nthree\nfour'
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      result: { content: [{ type: 'text', text: resultText }] },
    }
    const meta = plugin.toolResultMeta!({ kind: 'tool_result' }, end, 'bash', undefined)
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(resultText)
  })

  it('marks Read results collapsible using the shared line-numbered source', () => {
    const resultText = 'one\ntwo\nthree\nfour'
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'read',
      result: { content: [{ type: 'text', text: resultText }] },
    }
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'read',
      args: { path: '/tmp/a.ts', offset: 10 },
    }
    const meta = plugin.toolResultMeta!({ kind: 'tool_result' }, end, 'read', input(start))
    expect(meta).toMatchObject({ collapsible: true, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe(resultText)
  })

  it('exposes Write fallback diffs from the linked tool_use for the result toolbar', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'write',
      result: { content: [{ type: 'text', text: 'Created.' }] },
    }
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'write',
      args: { path: '/tmp/new.ts', content: 'piMetaWriteBody\n' },
    }
    const meta = plugin.toolResultMeta!({ kind: 'tool_result' }, end, 'write', input(start))
    expect(meta).toMatchObject({ collapsible: false, hasDiff: true, hasCopyable: true })
    expect(meta?.copyableContent()).toContain('piMetaWriteBody')
  })

  it('does not expose attempted Edit/Write fallback diffs when isError is true', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'edit',
      result: { content: [{ type: 'text', text: 'Found 2 occurrences.' }] },
      isError: true,
    }
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'edit',
      args: { path: '/tmp/a.ts', edits: [{ oldText: 'oldMetaMarker', newText: 'newMetaMarker' }] },
    }
    const meta = plugin.toolResultMeta!({ kind: 'tool_result' }, end, 'edit', input(start))
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('Found 2 occurrences.')
  })
})

describe('pi extractQuotableText', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('joins assistant text content blocks as paragraphs (≥2 newlines between blocks)', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'text', text: 'Hello' },
        { type: 'text', text: 'world' },
      ] },
    }
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, input(parent))).toBe('Hello\n\nworld')
  })

  it('joins thinking blocks for assistant_thinking', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'reasoning' }] },
    }
    expect(plugin.extractQuotableText!({ kind: 'assistant_thinking' }, input(parent))).toBe('reasoning')
  })

  it('returns user content string', () => {
    expect(plugin.extractQuotableText!({ kind: 'user_content' }, input({ role: 'user', content: ' hi ' }))).toBe('hi')
  })

  it('returns null for unrelated categories', () => {
    expect(plugin.extractQuotableText!({ kind: 'hidden' }, input({ type: 'message_end' }))).toBeNull()
  })
})

describe('pi extension UI integration', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('treats Pi input/select extension UI requests as ask-user-question', () => {
    expect(plugin.isAskUserQuestion!({ type: 'extension_ui_request', method: 'input' })).toBe(true)
    expect(plugin.isAskUserQuestion!({ type: 'extension_ui_request', method: 'select' })).toBe(true)
  })

  it('rejects non-question dialog methods from the ask-user-question shortcut', () => {
    expect(plugin.isAskUserQuestion!({ type: 'extension_ui_request', method: 'confirm' })).toBe(false)
    expect(plugin.isAskUserQuestion!({ type: 'extension_ui_request', method: 'editor' })).toBe(false)
  })

  it('maps Pi select options into shared AskUserQuestion options', () => {
    expect(plugin.extractAskUserQuestions!({
      type: 'extension_ui_request',
      id: 'req-1',
      method: 'select',
      title: 'Pick one',
      options: ['Allow', 'Block'],
    })).toEqual([{ id: 'req-1', question: 'Pick one', options: [{ label: 'Allow' }, { label: 'Block' }] }])
  })

  it('sends Pi select AskUserQuestion responses as extension_ui_response values', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await plugin.sendAskUserQuestionResponse!(
      'agent-1',
      onRespond,
      'req-1',
      [{ id: 'req-1', question: 'Pick one', options: [{ label: 'Allow' }, { label: 'Block' }] }],
      {
        selections: () => ({ 0: ['Block'] }),
        setSelections: vi.fn(),
        customTexts: () => ({}),
        setCustomTexts: vi.fn(),
        currentPage: () => 0,
        setCurrentPage: vi.fn(),
      },
      { type: 'extension_ui_request', method: 'select' },
    )

    expect(onRespond).toHaveBeenCalledOnce()
    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Block',
    })
  })

  it('builds confirm responses with confirmed=true on empty content', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'confirm' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', confirmed: true })
  })

  it('builds confirm responses with confirmed=false when the user typed feedback', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'confirm' }, 'this looks wrong', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', confirmed: false })
  })

  it('builds select responses with the typed value', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'select', options: ['Allow', 'Block'] }, 'Allow', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: 'Allow' })
  })

  it('cancels select responses with empty content', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'select' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', cancelled: true })
  })

  it('builds input responses preserving the exact value', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'input' }, ' typed text  ', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: ' typed text  ' })
  })

  it('builds empty input responses as value rather than cancellation', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'input' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: '' })
  })

  it('builds editor responses preserving the exact value', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'editor' }, 'multiline\ntext\n', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: 'multiline\ntext\n' })
  })

  it('cancels unknown methods to keep Pi unblocked', () => {
    const resp = plugin.buildControlResponse!({ type: 'extension_ui_request', method: 'futureMethod' }, 'whatever', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', cancelled: true })
  })
})
