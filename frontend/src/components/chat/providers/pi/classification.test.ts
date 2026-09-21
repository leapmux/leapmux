import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { input } from '../testUtils'
import { classifyPiMessage } from './classification'

describe('classifyPiMessage', () => {
  // Every entry kind, not only the custom ones. The worker keeps `custom` and
  // `custom_message` and drops the other seven, so those seven reach a row only as
  // one an earlier build wrote -- and each of them used to draw raw JSON.
  it.each([
    'custom',
    'custom_message',
    'message',
    'thinking_level_change',
    'model_change',
    'compaction',
    'branch_summary',
    'label',
    'session_info',
  ])('hides the %s session entry', (entryType) => {
    expect(classifyPiMessage(input({ type: 'entry_appended', entry: { type: entryType, customType: 'plan-mode-state' } }))).toEqual({ kind: 'hidden' })
  })

  // The worker consumes each of these into a surface outside the transcript -- the
  // settings pipeline, the session-info channel, an RPC reply -- or drops it. A row
  // an earlier build wrote must not draw raw JSON.
  it.each([
    'message_update',
    'queue_update',
    'bash_execution_update',
    'session_info_changed',
    'thinking_level_changed',
    'extension_ui_response',
    'response',
  ])('hides the consumed %s event', (type) => {
    expect(classifyPiMessage(input({ type }))).toEqual({ kind: 'hidden' })
  })

  // The worker persists all three as notifications. Without them in the notification
  // set each one classified as unknown and drew raw JSON, and a consolidated thread
  // that held one rendered its first entry alone.
  it.each([
    'summarization_retry_scheduled',
    'summarization_retry_attempt_start',
    'summarization_retry_finished',
  ])('classifies %s as a notification', (type) => {
    const message = { type, attempt: 1, maxAttempts: 3 }
    expect(classifyPiMessage(input(message)).kind).toBe('notification')
  })

  it('reads a consolidated thread of summarization retries as one notification', () => {
    const messages = [
      { type: 'summarization_retry_scheduled', attempt: 1, delayMs: 500 },
      { type: 'summarization_retry_finished' },
    ]
    const result = classifyPiMessage(input(messages[0], { old_seqs: [1, 2], messages }))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(2)
  })

  it('hides lifecycle markers without chat UI', () => {
    // agent_settled says only that Pi will not continue on its own after the
    // agent_end that already drew the divider, so it has nothing to render.
    for (const t of ['agent_start', 'agent_settled', 'turn_start', 'turn_end', 'message_start', 'tool_execution_update']) {
      expect(classifyPiMessage(input({ type: t }))).toEqual({ kind: 'hidden' })
    }
  })

  it('classifies agent_end as result_divider', () => {
    expect(classifyPiMessage(input({ type: 'agent_end', messages: [] }))).toEqual({ kind: 'result_divider' })
  })

  it('classifies message_end with text content as assistant_text', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'text', text: 'hello' }] },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('classifies message_end with only thinking content as assistant_thinking', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'reasoning' }] },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'assistant_thinking' })
  })

  // The neutral {isSynthetic, controlResponse} row -> control_response classification is provider-
  // agnostic and lives in classifyMessage (see messageClassifier.test.ts), not this plugin.

  it('hides signature-only thinking blocks so tool-call message_end rows do not render empty thinking bubbles', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'thinking', thinking: '', thinkingSignature: '{"id":"rs_1"}' },
        { type: 'toolCall', id: 'call-1', name: 'read', arguments: { path: '/tmp/a.ts' } },
      ] },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end with only empty thinking content', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [{ type: 'thinking', thinking: '', thinkingSignature: 'sig' }] },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end echoes for user prompts (LeapMux already persists the user_content row)', () => {
    const parent = {
      type: 'message_end',
      message: {
        role: 'user',
        content: [{ type: 'text', text: 'Hi. Who are you?' }],
      },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'hidden' })
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
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides message_end echoes for bash executions (host-driven, never enters chat)', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'bashExecution', command: 'ls', output: 'a\nb' },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies message_end with both thinking and text as assistant_text', () => {
    const parent = {
      type: 'message_end',
      message: { role: 'assistant', content: [
        { type: 'thinking', thinking: 'first' },
        { type: 'text', text: 'second' },
      ] },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('classifies tool_execution_start as tool_use with the tool name', () => {
    const parent = {
      type: 'tool_execution_start',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'ls' },
    }
    expect(classifyPiMessage(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('classifies tool_execution_end as tool_result', () => {
    const parent = {
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      result: { content: [{ type: 'text', text: 'done' }], details: {} },
    }
    const result = classifyPiMessage(input(parent))
    expect(result.kind).toBe('tool_result')
  })

  it('classifies compaction events as notification', () => {
    expect(classifyPiMessage(input({ type: 'compaction_start', reason: 'threshold' })).kind).toBe('notification')
    expect(classifyPiMessage(input({ type: 'compaction_end', reason: 'threshold' })).kind).toBe('notification')
  })

  it('classifies auto_retry events as notification', () => {
    expect(classifyPiMessage(input({ type: 'auto_retry_start' })).kind).toBe('notification')
    expect(classifyPiMessage(input({ type: 'auto_retry_end' })).kind).toBe('notification')
  })

  it('classifies extension_error as notification', () => {
    expect(classifyPiMessage(input({ type: 'extension_error', error: 'boom' })).kind).toBe('notification')
  })

  it('classifies extension_ui_request as notification (frontend dialog goes via control flow)', () => {
    expect(classifyPiMessage(input({ type: 'extension_ui_request', method: 'select' })).kind).toBe('notification')
  })

  it('classifies a notify extension_ui_request with a message as a notification', () => {
    const parent = { type: 'extension_ui_request', method: 'notify', message: 'Build finished' }
    expect(classifyPiMessage(input(parent))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: 'Build finished' }],
    })
  })

  it('hides a notify extension_ui_request with an empty message (nothing to render)', () => {
    // describePiNotification yields null for an empty notify, so surfacing it as a
    // notification would render no line and fall back to a raw-JSON bubble.
    expect(classifyPiMessage(input({ type: 'extension_ui_request', method: 'notify', message: '' })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides a notify extension_ui_request with no message field', () => {
    expect(classifyPiMessage(input({ type: 'extension_ui_request', method: 'notify' })))
      .toEqual({ kind: 'hidden' })
  })

  it('hides a consolidated wrapper of only empty-notify extension requests', () => {
    const empties = [
      { type: 'extension_ui_request', method: 'notify', message: '' },
      { type: 'extension_ui_request', method: 'notify' },
    ]
    expect(classifyPiMessage(input(empties[0], { old_seqs: [], messages: empties })))
      .toEqual({ kind: 'hidden' })
  })

  it('drops empty-notify requests from a thread but keeps a renderable notification', () => {
    const empty = { type: 'extension_ui_request', method: 'notify', message: '' }
    const compaction = { type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }
    const result = classifyPiMessage(input(empty, { old_seqs: [], messages: [empty, compaction] }))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(1)
  })

  it('classifies user echo content as user_content', () => {
    expect(classifyPiMessage(input({ role: 'user', content: 'hello' })).kind).toBe('user_content')
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
    const result = classifyPiMessage(input(messages[0], { old_seqs: [], messages }))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(2)
  })

  it('classifies a wrapper of two compaction_end boundaries as a notification', () => {
    const messages = [
      { type: 'compaction_end', summary: 'first', result: { tokensBefore: 100000 } },
      { type: 'compaction_end', summary: 'second', result: { tokensBefore: 50000 } },
    ]
    expect(classifyPiMessage(input(messages[0], { old_seqs: [], messages })).kind).toBe('notification')
  })

  it('does not treat a wrapper of non-notification Pi events as a notification', () => {
    // A wrapper whose entries are not Pi notification surface types (here an
    // assistant message_end) must not be hijacked into the notification path --
    // only the per-message classification applies (assistant_text here).
    const messages = [{ type: 'message_end', message: { role: 'assistant' } }]
    expect(classifyPiMessage(input(messages[0], { old_seqs: [], messages })).kind).not.toBe('notification')
  })

  // The LeapMux plain-row types, read from the shared list rather than copied here.
  // Pi owns no branch for them, so the shared rule is the only one that answers a
  // standalone row of one. Without it the row reaches the `unknown` fallback and draws
  // the raw-JSON card. A type added to the shared list reaches Pi's suite too, so no
  // copy here can fall behind it.
  const plainRows = [
    { type: NOTIFICATION_TYPE.Interrupted },
    { type: NOTIFICATION_TYPE.SettingsChanged, changes: { model: { old: 'a', new: 'b' } } },
    { type: NOTIFICATION_TYPE.ContextCleared },
    { type: NOTIFICATION_TYPE.AgentError, error: 'failed' },
    { type: NOTIFICATION_TYPE.PlanUpdated, plan_title: 'Plan' },
    { type: NOTIFICATION_TYPE.Compacting },
  ]

  it('reads its plain-row cases from the shared list', () => {
    // The guard for the cases below. An empty array registers no case at all, and the
    // suite then passes while it proves nothing.
    expect(plainRows.length).toBeGreaterThan(0)
  })

  it.each(plainRows)('classifies a standalone $type row as a notification', (parent) => {
    expect(classifyPiMessage(input(parent)).kind).toBe('notification')
  })

  it('falls back to unknown for unrecognized shapes', () => {
    expect(classifyPiMessage(input({ type: 'something_else' })).kind).toBe('unknown')
  })
})
