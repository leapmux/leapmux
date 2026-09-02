import type { ParsedMessageContent } from '~/lib/messageParser'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderDivider } from '../../messageRenderTestUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'
// Importing the module also REGISTERS the Pi plugin, which the metadata cases
// below read back out of the registry.
import { piValidateResumeHandle } from './plugin'
// Side-effect imports: the sweep below reads every provider out of the registry.
import '../claude/plugin'
import '../codex/plugin'

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
    // agent_settled says only that Pi will not continue on its own after the
    // agent_end that already drew the divider, so it has nothing to render.
    for (const t of ['agent_start', 'agent_settled', 'turn_start', 'turn_end', 'message_start', 'tool_execution_update']) {
      expect(plugin.classify(input({ type: t }))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies agent_end as result_divider', () => {
    expect(plugin.classify(input({ type: 'agent_end', messages: [] }))).toEqual({ kind: 'result_divider' })
  })

  it('maps agent_end (stop) to a "Turn ended" divider model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'stop' }] }))
      .toEqual({ label: 'Turn ended', turnContinues: false })
  })

  it('maps an aborted stopReason to a danger "Turn aborted" model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'aborted' }] }))
      .toEqual({ label: 'Turn aborted', isError: true, turnContinues: false })
  })

  it('maps an error stopReason to a danger "Turn failed — <msg>" model', () => {
    expect(plugin.resultDivider!({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'rate limit' }] }))
      .toEqual({ label: 'Turn failed — rate limit', isError: true, turnContinues: false })
  })

  it('returns null when the message is not agent_end', () => {
    expect(plugin.resultDivider!({ type: 'message_end' })).toBeNull()
  })

  describe('turn duration on the divider', () => {
    // Pi's own agent_end carries no duration; the worker measures the turn and
    // injects duration_ms under the same name Claude Code emits.
    const ended = (extra: Record<string, unknown>, stopReason = 'stop'): string =>
      plugin.resultDivider!({
        type: 'agent_end',
        messages: [{ role: 'assistant', stopReason, errorMessage: 'WebSocket error' }],
        ...extra,
      })!.label

    it('appends the formatted duration to a completed turn', () => {
      expect(ended({ duration_ms: 3200 })).toBe('Turn ended (3.2s)')
    })

    it('keeps the plain label when the worker measured nothing', () => {
      expect(ended({})).toBe('Turn ended')
    })

    it('shows a real zero rather than dropping the suffix', () => {
      expect(ended({ duration_ms: 0 })).toBe('Turn ended (0ms)')
    })

    it('ignores a non-numeric duration_ms', () => {
      expect(ended({ duration_ms: 'soon' })).toBe('Turn ended')
    })

    it('appends the duration to the length-limit label', () => {
      expect(ended({ duration_ms: 3200 }, 'length')).toBe('Turn ended (length limit) (3.2s)')
    })

    it('appends the duration to an aborted turn', () => {
      expect(ended({ duration_ms: 45_000 }, 'aborted')).toBe('Turn aborted (45s)')
    })

    it('appends the duration after the error message', () => {
      expect(ended({ duration_ms: 1500 }, 'error')).toBe('Turn failed — WebSocket error (1.5s)')
    })
  })

  describe('a run Pi will retry', () => {
    const retrying = plugin.resultDivider!({
      type: 'agent_end',
      willRetry: true,
      duration_ms: 2100,
      messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }],
    })!

    it('marks the divider auto-retry after the duration', () => {
      expect(retrying.label).toBe('Turn failed — overloaded (2.1s) · auto-retry')
    })

    it('reports that the turn continues, so the thinking indicator stays up', () => {
      expect(retrying.turnContinues).toBe(true)
    })

    it('adds no meta part when Pi does not retry', () => {
      const model = plugin.resultDivider!({
        type: 'agent_end',
        duration_ms: 2100,
        messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }],
      })!
      expect(model.label).toBe('Turn failed — overloaded (2.1s)')
      expect(model.turnContinues).toBe(false)
    })
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

  // The neutral {isSynthetic, controlResponse} row -> control_response classification is provider-
  // agnostic and lives in classifyMessage (see messageClassification.test.ts), not this plugin.

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

describe('pi spanRole', () => {
  const plugin = providerFor(AgentProvider.PI)!

  function parsedWithType(type: string): ParsedMessageContent {
    return { rawText: '', topLevel: null, parentObject: { type }, wrapper: null }
  }

  it('routes tool_execution_start to opener and _end to result by envelope type', () => {
    expect(plugin.spanRole!(parsedWithType('tool_execution_start'))).toBe('opener')
    expect(plugin.spanRole!(parsedWithType('tool_execution_end'))).toBe('result')
  })

  it('returns other for an unrelated pi envelope type', () => {
    expect(plugin.spanRole!(parsedWithType('agent_message'))).toBe('other')
  })
})

describe('pi contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.PI)!

  // Pi reads message.usage off the parsed message (getInnerMessage(parsed).message.usage).
  const withUsage = (usage: Record<string, unknown>): ParsedMessageContent =>
    ({ rawText: '', topLevel: null, parentObject: { message: { usage } }, wrapper: null })

  it('extracts raw Pi usage (input/output/cacheRead/cacheWrite/totalTokens)', () => {
    expect(plugin.contextUsageFromMessage!(withUsage({ input: 100, output: 10, cacheRead: 20, cacheWrite: 5, totalTokens: 130 })))
      .toEqual({ inputTokens: 100, cacheCreationInputTokens: 5, cacheReadInputTokens: 20, outputTokens: 10, contextTokens: 130 })
  })

  it('returns null when there is no token data', () => {
    expect(plugin.contextUsageFromMessage!(withUsage({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0 }))).toBeNull()
  })

  it('returns null for a non-Pi usage shape (no input field)', () => {
    expect(plugin.contextUsageFromMessage!(withUsage({ input_tokens: 100 }))).toBeNull()
  })

  it('returns null when the message carries no message.usage', () => {
    expect(plugin.contextUsageFromMessage!({ rawText: '', topLevel: null, parentObject: { type: 'message_end', message: {} }, wrapper: null })).toBeNull()
  })
})

/**
 * The browser half of `testdata/pi_resume_handle_conformance.json`. The worker
 * suite (`TestPiResumeHandleConformance`) reads the same file, so a one-sided
 * edit to either implementation turns that side red. See the file's own
 * `_readme` for the contract and for the one asymmetry it allows.
 */
describe('piValidateResumeHandle conformance', () => {
  interface HandleSpec {
    head?: string
    text: string
    repeat?: number
    tail?: string
  }

  interface Verdict {
    valid: boolean
    refusal: string
  }

  interface HandleCase {
    input: HandleSpec
    browser: Verdict
    worker: { posix: Verdict, windows: Verdict }
    why: string
  }

  function buildHandle(spec: HandleSpec): string {
    return (spec.head ?? '') + spec.text.repeat(spec.repeat ?? 1) + (spec.tail ?? '')
  }

  // Each token maps to a substring of THIS side's message. The worker's
  // wording differs (it reports `path traversal not allowed` where this
  // reports the `..` it refuses), so each suite carries its own map and the
  // fixture stays language-neutral.
  const refusalMarkers: Record<string, string> = {
    too_long: 'must be at most',
    not_absolute: 'must be absolute',
    traversal: 'must not contain ".."',
    leading_hyphen: 'must not start with a hyphen',
    forbidden_character: 'contains invalid characters',
    invisible_character: 'contains invisible characters',
    whitespace_at_edge: 'must not start or end with whitespace',
  }

  const fixturePath = resolve(
    dirname(fileURLToPath(import.meta.url)),
    '../../../../../../testdata/pi_resume_handle_conformance.json',
  )

  const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: HandleCase[] }

  // A fixture that silently loads zero cases would make this block pass while
  // asserting nothing -- the one failure mode a shared fixture must not have.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    const got = piValidateResumeHandle(buildHandle(c.input))
    if (c.browser.valid) {
      expect(c.browser.refusal, `case "${c.why}" is valid, so its refusal must be empty`).toBe('')
      expect(got).toBeNull()
      return
    }
    expect(got, `case "${c.why}" must be refused`).not.toBeNull()
    const marker = refusalMarkers[c.browser.refusal]
    expect(marker, `case "${c.why}" carries an unknown refusal token "${c.browser.refusal}"`).toBeDefined()
    expect(got).toContain(marker)
  })

  // The asymmetry this rule is allowed to have, in the ONE direction that is
  // safe. The browser may accept what a worker refuses -- it does not know the
  // worker's host, so it leaves the reserved device names and the spelling of
  // "absolute" to the side that does. The reverse removes a legitimate resume
  // with no way to reach it: a value this field refuses never reaches a worker
  // to be judged. The Go suite asserts the same invariant.
  it.each(fixture.cases.filter(c => !c.browser.valid))('no worker accepts what the browser refuses: $why', (c) => {
    expect(c.worker.posix.valid).toBe(false)
    expect(c.worker.windows.valid).toBe(false)
  })
})

// The dispatch this rule exists to be reachable by. A provider's resume rule is
// its own -- the worker asks `Provider.ResolveResumeHandle` per provider -- so
// the browser must ask the plugin rather than branch on a flag in a shared lib.
describe('piValidateResumeHandle picks the rule by shape', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('is exposed on the plugin, so the resume field reaches it through the registry', () => {
    expect(plugin.validateResumeHandle).toBe(piValidateResumeHandle)
  })

  it('takes a session ID under the token rule', () => {
    expect(piValidateResumeHandle('018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b')).toBeNull()
    // The token rule's own guards still apply to that half.
    expect(piValidateResumeHandle('-dangerous')).toContain('hyphen')
  })

  it('takes a session file path under the path rule', () => {
    expect(piValidateResumeHandle('/Users/dev/.pi/agent/sessions/p/s.jsonl')).toBeNull()
    expect(piValidateResumeHandle('C:\\Users\\pi\\sessions\\s.jsonl')).toBeNull()
    // A backslash and a length past 128 bytes are exactly what the TOKEN rule
    // bans, and a real Pi session path carries both -- which is why one rule
    // could not serve both shapes.
    const realPath = `/Users/dev/.pi/agent/sessions/${'a'.repeat(140)}.jsonl`
    expect(realPath.length).toBeGreaterThan(128)
    expect(piValidateResumeHandle(realPath)).toBeNull()
    // The path shape keeps its OWN, larger cap.
    expect(piValidateResumeHandle(`/tmp/${'a'.repeat(1024)}.jsonl`)).toContain('at most')
  })

  it('reads the `.jsonl` suffix and a separator as Pi\'s resolver does', () => {
    // A bare file name is a PATH to Pi, and a relative one, so it is refused as
    // a path rather than accepted as a token.
    expect(piValidateResumeHandle('session.jsonl')).toContain('absolute')
    expect(piValidateResumeHandle('sessions/s.jsonl')).toContain('absolute')
    // A bare tilde holds no separator, so BOTH sides read it as an ID.
    expect(piValidateResumeHandle('~')).toBeNull()
  })

  it('accepts the empty handle, which means no resume', () => {
    expect(piValidateResumeHandle('')).toBeNull()
  })
})

// Every other provider says nothing and takes the shared token rule, so a
// provider added later is covered by default and only one whose handle is a
// different shape has to override. This fails the day a second provider does,
// which is the day somebody should look at whether the split still reads. The
// worker's TestPiIsTheOnlyProviderOffTheTokenRule is the same guard in Go.
describe('pi is the only provider with a resume rule of its own', () => {
  it.each([
    AgentProvider.CLAUDE_CODE,
    AgentProvider.CODEX,
    AgentProvider.ZCODE,
    AgentProvider.OPENCODE,
    AgentProvider.CURSOR,
  ])('%s takes the shared token rule by saying nothing', (id) => {
    expect(providerFor(id)?.validateResumeHandle).toBeUndefined()
  })
})
