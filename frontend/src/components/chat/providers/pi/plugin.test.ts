import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { renderDivider } from '../../messageRenderTestUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the Pi plugin.
import './plugin'

describe('pi plugin metadata', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('exposes attachment capabilities (text + image only)', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('treats the session id as a file path (Pi sessions are .jsonl files)', () => {
    expect(plugin.sessionIdIsFilePath).toBe(true)
  })

  it('does not advertise a permission mode for Pi', () => {
    expect(plugin.bypassPermissionMode).toBeUndefined()
  })

  it('builds a Pi abort RPC for interrupt', () => {
    expect(plugin.buildInterruptContent?.('any-session', 'turn-1')).toBe(JSON.stringify({ type: 'abort' }))
  })
})

describe('pi classify', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('declares no trigger mode segment (Pi has no mode axis)', () => {
    expect(plugin.triggerModeGroupKey).toBeUndefined()
  })

  it('hides lifecycle markers without chat UI', () => {
    for (const t of ['agent_start', 'turn_start', 'turn_end', 'message_start', 'tool_execution_update']) {
      expect(plugin.classify(input({ type: t }))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies agent_end as result_divider', () => {
    expect(plugin.classify(input({ type: 'agent_end', messages: [] }))).toEqual({ kind: 'result_divider' })
  })

  it('maps agent_end (stop) to a "Turn ended" divider model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'stop' }] }))
      .toEqual({ label: 'Turn ended' })
  })

  it('maps an aborted stopReason to a danger "Turn aborted" model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'aborted' }] }))
      .toEqual({ label: 'Turn aborted', isError: true })
  })

  it('maps an error stopReason to a danger "Turn failed — <msg>" model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'rate limit' }] }))
      .toEqual({ label: 'Turn failed — rate limit', isError: true })
  })

  it('returns null when the message is not agent_end', () => {
    expect(plugin.resultDivider!({ type: 'message_end' })).toBeNull()
  })

  it('renders a danger divider through the shared renderer end-to-end', () => {
    // MessageBubble routes result_divider through renderResultDivider, which draws
    // the shared ResultDivider with the inline danger color for a failed turn.
    const { text, isError } = renderDivider(
      { type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'aborted' }] },
      AgentProvider.PI,
    )
    expect(text).toBe('Turn aborted')
    expect(isError).toBe(true)
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

  it('classifies a notify extension_ui_request with a message as a notification', () => {
    const parent = { type: 'extension_ui_request', method: 'notify', message: 'Build finished' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('hides a notify extension_ui_request with an empty message (nothing to render)', () => {
    // describePiNotification yields null for an empty notify, so surfacing it as a
    // notification would render no line and fall back to a raw-JSON bubble.
    expect(plugin.classify(input({ type: 'extension_ui_request', method: 'notify', message: '' })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides a notify extension_ui_request with no message field', () => {
    expect(plugin.classify(input({ type: 'extension_ui_request', method: 'notify' })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides a consolidated wrapper of only empty-notify extension requests', () => {
    const empties = [
      { type: 'extension_ui_request', method: 'notify', message: '' },
      { type: 'extension_ui_request', method: 'notify' },
    ]
    expect(plugin.classify(input(empties[0], { old_seqs: [], messages: empties })))
      .toEqual({ kind: 'hidden' })
  })

  it('drops empty-notify requests from a thread but keeps a renderable notification', () => {
    const empty = { type: 'extension_ui_request', method: 'notify', message: '' }
    const compaction = { type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }
    expect(plugin.classify(input(empty, { old_seqs: [], messages: [empty, compaction] })))
      .toEqual({ kind: 'notification', messages: [compaction] })
  })

  it('classifies user echo content as user_content', () => {
    expect(plugin.classify(input({ role: 'user', content: 'hello' })).kind).toBe('user_content')
  })

  it('classifies a consolidated multi-event Pi wrapper as a notification carrying every message', () => {
    // The backend consolidates consecutive AGENT-source Pi notifications into one
    // `notification_thread` wrapper. Without Pi extraTypes the wrapper was not
    // recognized as a thread, so it fell to the per-message branch and
    // MessageBubble rendered only messages[0] -- dropping the rest.
    const messages = [
      { type: 'auto_retry_start', attempt: 1, maxAttempts: 3, delayMs: 2000 },
      { type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } },
    ]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })))
      .toEqual({ kind: 'notification', messages })
  })

  it('classifies a wrapper of two compaction_end boundaries as a notification', () => {
    const messages = [
      { type: 'compaction_end', summary: 'first', result: { tokensBefore: 100000 } },
      { type: 'compaction_end', summary: 'second', result: { tokensBefore: 50000 } },
    ]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })).kind).toBe('notification')
  })

  it('does not treat a wrapper of non-notification Pi events as a notification', () => {
    // A wrapper whose entries are not Pi notification surface types (here an
    // assistant message_end) must not be hijacked into the notification path --
    // only the per-message classification applies (assistant_text here).
    const messages = [{ type: 'message_end', message: { role: 'assistant' } }]
    expect(plugin.classify(input(messages[0], { old_seqs: [], messages })).kind).not.toBe('notification')
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
