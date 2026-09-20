import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderDivider } from '~/test-support/messageRenderProbes'
import { providerQuotableText, providerRow, providerToolMeta } from '~/test-support/toolCallFixture'
import { createControlAnswerState } from '../../controls/types'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
// Side-effect imports. The first two let the sweep below read every provider out of
// the registry; the third REGISTERS the Pi plugin, which the metadata cases read back
// out of it.
import '../claude/plugin'
import '../codex/plugin'
import './plugin'

describe('pi plugin metadata', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('exposes attachment capabilities (text + image only)', () => {
    expect(plugin?.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('treats the session id as a file path (Pi sessions are .jsonl files)', () => {
    expect(plugin?.session?.sessionIdIsFilePath).toBe(true)
  })

  it('does not advertise a permission mode for Pi', () => {
    expect(plugin?.controls?.permissionPresets).toBeUndefined()
  })

  it('declares no trigger mode segment (Pi has no mode axis)', () => {
    expect(plugin?.configuration?.triggerModeGroupKey).toBeUndefined()
  })
})

describe('pi result divider', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('maps agent_end (stop) to a "Turn ended" divider model', () => {
    expect(plugin!.transcript.extractDivider({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'stop' }] }))
      .toEqual({ label: 'Turn ended' })
  })

  // An aborted turn is not an error: the reader asked for it, and every provider
  // states an interruption in the same words.
  it('maps an aborted stopReason to the shared interruption label', () => {
    expect(plugin!.transcript.extractDivider({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'aborted' }] }))
      .toEqual({ label: 'Turn interrupted' })
  })

  it('maps an error stopReason to a danger "Turn failed — <msg>" model', () => {
    expect(plugin!.transcript.extractDivider({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'rate limit' }] }))
      .toEqual({ label: 'Turn failed — rate limit', isError: true })
  })

  // A turn the USER stopped, reported as an ERROR. Pi spells one stop two ways: a
  // turn it aborts cleanly carries `stopReason: 'aborted'`, and a turn whose tool
  // was still running carries `stopReason: 'error'` with `This operation was
  // aborted`. The second read as "Turn failed" in the danger color for a stop the
  // reader asked for -- the defect CC-001 records for Claude Code, on a second
  // provider. LeapMux knows which it is, because it sent the abort, and that
  // knowledge reaches the divider in the completion column.
  it('reads an interrupted completion before the frame own error stopReason', () => {
    expect(plugin!.transcript.extractDivider(
      {
        type: 'agent_end',
        duration_ms: 15_000,
        messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'This operation was aborted' }],
      },
      MessageCompletion.INTERRUPTED,
    )).toEqual({ label: 'Turn interrupted (15s)' })
  })

  // The correction is limited to a stop LeapMux asked for. A real failure keeps its
  // own words and its danger color.
  it('keeps a genuine failure when no interruption was recorded', () => {
    expect(plugin!.transcript.extractDivider(
      { type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'rate limit' }] },
      MessageCompletion.ERROR,
    )).toEqual({ label: 'Turn failed — rate limit', isError: true })
  })

  it('returns null when the message is not agent_end', () => {
    expect(plugin!.transcript.extractDivider({ type: 'message_end' })).toBeNull()
  })

  describe('turn duration on the divider', () => {
    // Pi's own agent_end carries no duration; the worker measures the turn and
    // injects duration_ms under the same name Claude Code emits.
    const ended = (extra: Record<string, unknown>, stopReason = 'stop'): string =>
      plugin!.transcript.extractDivider({
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
      expect(ended({ duration_ms: 3200 }, 'length')).toBe('Turn ended (3.2s, length limit)')
    })

    it('appends the duration to an aborted turn', () => {
      expect(ended({ duration_ms: 45_000 }, 'aborted')).toBe('Turn interrupted (45s)')
    })

    it('appends the duration after the error message', () => {
      expect(ended({ duration_ms: 1500 }, 'error')).toBe('Turn failed (1.5s) — WebSocket error')
    })
  })

  describe('a run Pi will retry', () => {
    const retrying = plugin!.transcript.extractDivider({
      type: 'agent_end',
      willRetry: true,
      duration_ms: 2100,
      messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }],
    })!

    it('marks the divider auto-retry after the duration', () => {
      expect(retrying.label).toBe('Turn failed (2.1s, auto-retry) — overloaded')
    })

    it('adds no meta part when Pi does not retry', () => {
      const model = plugin!.transcript.extractDivider({
        type: 'agent_end',
        duration_ms: 2100,
        messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }],
      })!
      expect(model.label).toBe('Turn failed (2.1s) — overloaded')
    })
  })

  it('renders a danger divider through the shared renderer end-to-end', () => {
    // MessageBubble routes result_divider through the shared row extraction, which draws
    // the shared ResultDivider. A FAILED turn takes the inline danger color; an
    // interrupted one does not, because the reader asked for it.
    const { text, isError } = renderDivider(
      { type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'rate limit' }] },
      AgentProvider.PI,
    )
    expect(text).toBe('Turn failed — rate limit')
    expect(isError).toBe(true)
    expect(renderDivider(
      { type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'aborted' }] },
      AgentProvider.PI,
    )).toEqual({ text: 'Turn interrupted', isError: false })
  })
})

describe('pi tool row toolbar metadata', () => {
  it('marks Bash results collapsible using the rendered command output', () => {
    const resultText = 'one\ntwo\nthree\nfour'
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      result: { content: [{ type: 'text', text: resultText }] },
    }
    const meta = providerToolMeta(AgentProvider.PI, end, { category: { kind: 'tool_result' }, spanType: 'bash' })
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
    const meta = providerToolMeta(AgentProvider.PI, end, { category: { kind: 'tool_result' }, spanType: 'read', request: input(start) })
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
    const meta = providerToolMeta(AgentProvider.PI, end, { category: { kind: 'tool_result' }, spanType: 'write', request: input(start) })
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
    const meta = providerToolMeta(AgentProvider.PI, end, { category: { kind: 'tool_result' }, spanType: 'edit', request: input(start) })
    expect(meta).toMatchObject({ collapsible: false, hasDiff: false, hasCopyable: true })
    expect(meta?.copyableContent()).toBe('Found 2 occurrences.')
  })

  // A plan-complete row leaves the tool path: it draws through the shared plan
  // layout, which carries its own Copy and Reply. So these cases state the row
  // rather than a toolbar the row does not use.
  //
  // The REQUEST row is the plan. It carries the plan from the moment the call opens,
  // which is when a reader has to read it, and it is the only row that draws it.
  it('reads a plan-complete request as the plan it proposes', () => {
    const planText = '# Plan\n\n1. Read the code.\n2. Write the test.'
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'plan_mode_complete',
      args: { plan: planText },
    }
    expect(providerRow(AgentProvider.PI, start, { spanType: 'plan_mode_complete' }))
      .toEqual({ kind: 'assistant-plan', text: planText })
  })

  // The closing row states that the proposal ended, and NOT the plan again. Pi
  // repeats the whole plan in both `result.details` and its result text, so a row
  // that drew either printed the plan a second time under the request's own card.
  it('states the notice on a plan-complete result rather than the plan again', () => {
    const planText = '# Plan\n\n1. Read the code.\n2. Write the test.'
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'plan_mode_complete',
      result: { content: [{ type: 'text', text: `**Proposed Plan**\n\n${planText}` }], details: { plan: planText } },
    }
    expect(providerRow(AgentProvider.PI, end, { spanType: 'plan_mode_complete' }))
      .toEqual({ kind: 'assistant-text', text: 'Plan ready for review.' })
  })

  // The answer must not depend on whether the store has resolved the paired request
  // yet. The reader it replaced consulted that pair, so the same row drew a plan
  // card before the pair landed and this notice afterwards.
  it('states the same notice whether or not the paired request resolved', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'plan_mode_complete',
      result: { content: [{ type: 'text', text: 'Plan ready.' }], details: { plan: '# Plan' } },
    }
    const withRequest = providerRow(AgentProvider.PI, end, {
      spanType: 'plan_mode_complete',
      request: input({ type: 'tool_execution_start', toolCallId: 'call-1', toolName: 'plan_mode_complete', args: { plan: '# Plan' } }, undefined, AgentProvider.PI),
    })
    const alone = providerRow(AgentProvider.PI, end, { spanType: 'plan_mode_complete' })
    expect(withRequest).toEqual(alone)
    expect(alone).toEqual({ kind: 'assistant-text', text: 'Plan ready for review.' })
  })

  it('falls back to the result text when a plan-complete result carries no plan', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'plan_mode_complete',
      result: { content: [{ type: 'text', text: 'Plan ready for review.' }] },
    }
    expect(providerRow(AgentProvider.PI, end, { spanType: 'plan_mode_complete' }))
      .toEqual({ kind: 'assistant-text', text: 'Plan ready for review.' })
  })

  it('states the refusal of a failed plan-complete row, not the plan it ignored', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'plan_mode_complete',
      isError: true,
      result: { content: [{ type: 'text', text: 'The plan tool refused.' }], details: { plan: '# Ignored' } },
    }
    expect(providerRow(AgentProvider.PI, end, { spanType: 'plan_mode_complete' }))
      .toEqual({ kind: 'assistant-text', text: 'The plan tool refused.' })
  })
})

describe('pi quotable text', () => {
  it('joins assistant text content blocks as paragraphs (≥2 newlines between blocks)', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'text', text: 'Hello' },
        { type: 'text', text: 'world' },
      ] },
    }
    expect(providerQuotableText(AgentProvider.PI, parent, { category: { kind: 'assistant_text' } })).toBe('Hello\n\nworld')
  })

  it('joins thinking blocks for assistant_thinking', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'reasoning' }] },
    }
    expect(providerQuotableText(AgentProvider.PI, parent, { category: { kind: 'assistant_thinking' } })).toBe('reasoning')
  })

  it('returns user content string', () => {
    expect(providerQuotableText(AgentProvider.PI, { role: 'user', content: ' hi ' }, { category: { kind: 'user_content' } })).toBe('hi')
  })

  it('returns null for unrelated categories', () => {
    expect(providerQuotableText(AgentProvider.PI, { type: 'message_end' }, { category: { kind: 'hidden' } })).toBeNull()
  })
})

describe('pi extension UI integration', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('treats Pi input/select extension UI requests as ask-user-question', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({ type: 'extension_ui_request', method: 'input' })).toBe(true)
    expect(plugin?.controls?.askUserQuestion!.isRequest({ type: 'extension_ui_request', method: 'select' })).toBe(true)
  })

  it('rejects non-question dialog methods from the ask-user-question shortcut', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({ type: 'extension_ui_request', method: 'confirm' })).toBe(false)
    expect(plugin?.controls?.askUserQuestion!.isRequest({ type: 'extension_ui_request', method: 'editor' })).toBe(false)
  })

  it('maps Pi select options into shared AskUserQuestion options', () => {
    expect(plugin?.controls?.askUserQuestion!.extractQuestions({
      type: 'extension_ui_request',
      id: 'req-1',
      method: 'select',
      title: 'Pick one',
      options: ['Allow', 'Block'],
    })).toEqual([{ id: 'req-1', question: 'Pick one', options: [{ label: 'Allow' }, { label: 'Block' }] }])
  })

  it('sends Pi select AskUserQuestion responses as extension_ui_response values', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await plugin?.controls?.askUserQuestion!.sendAnswer(
      { requestId: 'req-1', agentId: 'agent-1', payload: { type: 'extension_ui_request', method: 'select' } },
      onRespond,
      [{ id: 'req-1', question: 'Pick one', options: [{ label: 'Allow' }, { label: 'Block' }] }],
      createControlAnswerState({ selections: { 0: ['Block'] } }),
    )

    expect(onRespond).toHaveBeenCalledOnce()
    const respondCall = onRespond.mock.calls[0]
    expect(respondCall).toBeDefined()
    const [bytes] = respondCall ?? []
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Block',
    })
  })

  it('builds confirm responses with confirmed=true on empty content', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'confirm' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', confirmed: true })
  })

  it('builds confirm responses with confirmed=false when the user typed feedback', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'confirm' }, 'this looks wrong', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', confirmed: false })
  })

  it('builds select responses with the typed value', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'select', options: ['Allow', 'Block'] }, 'Allow', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: 'Allow' })
  })

  it('cancels select responses with empty content', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'select' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', cancelled: true })
  })

  it('builds input responses preserving the exact value', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'input' }, ' typed text  ', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: ' typed text  ' })
  })

  it('builds empty input responses as value rather than cancellation', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'input' }, '', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: '' })
  })

  it('builds editor responses preserving the exact value', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'editor' }, 'multiline\ntext\n', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', value: 'multiline\ntext\n' })
  })

  it('cancels unknown methods to keep Pi unblocked', () => {
    const resp = plugin?.controls?.buildControlResponse!({ type: 'extension_ui_request', method: 'futureMethod' }, 'whatever', 'req-1')
    expect(resp).toMatchObject({ type: 'extension_ui_response', id: 'req-1', cancelled: true })
  })
})

describe('pi spanRole', () => {
  const plugin = providerFor(AgentProvider.PI)!

  function parsedWithType(type: string): ResolvedMessageContent {
    return resolveMessageForRendering({ rawText: '', topLevel: null, parentObject: { type }, wrapper: null }, AgentProvider.PI)
  }

  it('routes tool_execution_start to request and _end to result by envelope type', () => {
    expect(plugin?.transcript.spanRole!(parsedWithType('tool_execution_start'))).toBe('request')
    expect(plugin?.transcript.spanRole!(parsedWithType('tool_execution_end'))).toBe('result')
  })

  it('returns other for an unrelated pi envelope type', () => {
    expect(plugin?.transcript.spanRole!(parsedWithType('agent_message'))).toBe('other')
  })
})

describe('pi contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.PI)!

  // Pi reads message.usage off the parsed message (getInnerMessage(parsed).message.usage).
  const withUsage = (usage: Record<string, unknown>): ParsedMessageContent =>
    ({ rawText: '', topLevel: null, parentObject: { message: { usage } }, wrapper: null })

  it('extracts raw Pi usage (input/output/cacheRead/cacheWrite/totalTokens)', () => {
    expect(plugin?.session?.contextUsageFromMessage!(withUsage({ input: 100, output: 10, cacheRead: 20, cacheWrite: 5, totalTokens: 130 })))
      .toEqual({ inputTokens: 100, cacheCreationInputTokens: 5, cacheReadInputTokens: 20, outputTokens: 10, contextTokens: 130 })
  })

  it('returns null when there is no token data', () => {
    expect(plugin?.session?.contextUsageFromMessage!(withUsage({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0 }))).toBeNull()
  })

  it('returns null for a non-Pi usage shape (no input field)', () => {
    expect(plugin?.session?.contextUsageFromMessage!(withUsage({ input_tokens: 100 }))).toBeNull()
  })

  it('returns null when the message carries no message.usage', () => {
    expect(plugin?.session?.contextUsageFromMessage!({ rawText: '', topLevel: null, parentObject: { type: 'message_end', message: {} }, wrapper: null })).toBeNull()
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
    expect(providerFor(id)?.session?.validateResumeHandle).toBeUndefined()
  })
})

// Each half of a Pi tool call wants the other: the start row carries the name and the
// arguments, the end row carries the result.
describe('pi relatedMessages', () => {
  const plugin = providerFor(AgentProvider.PI)!

  it('each half of a tool call wants the other', () => {
    expect(plugin?.transcript.relatedMessages!(input({ type: 'tool_execution_start', toolCallId: 'call', toolName: 'bash', args: { command: 'ls' } }))).toEqual(['result'])
    expect(plugin?.transcript.relatedMessages!(input({ type: 'tool_execution_end', toolCallId: 'call', toolName: 'bash', result: { content: [] }, isError: false }))).toEqual(['request'])
  })
})
