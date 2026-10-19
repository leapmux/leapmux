import { describe, expect, it } from 'vitest'
import { CLAUDE_HIDDEN_SYSTEM_SUBTYPE } from '~/generated/contracts/claude-protocol'
import { input } from '../testUtils'
import { classifyClaudeCodeMessage } from './classification'

describe('classifyClaudeCodeMessage', () => {
  it('classifies result divider', () => {
    const parent = {
      type: 'result',
      subtype: 'success',
      result: 'Done',
      duration_ms: 1234,
      num_turns: 1,
      stop_reason: 'end_turn',
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('classifies error result divider', () => {
    const parent = {
      type: 'result',
      is_error: true,
      errors: ['something went wrong'],
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('classifies the /context local-command result as a divider, not hidden', () => {
    // The redundant-with-the-assistant-bubble and danger-styling concerns are
    // both handled by the result_divider renderer (claudeResultDivider), so the
    // classifier keeps every result a turn-end divider (see ./notifications.test.tsx).
    const parent = {
      type: 'result',
      subtype: 'success',
      is_error: false,
      num_turns: 0,
      stop_reason: null,
      result: '## Context Usage\n\n**Model:** claude-opus-4-8[1m]\n',
      duration_ms: 2062,
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('hides EnterPlanMode tool_result wrappers persisted as user messages', () => {
    const parent = {
      role: 'user',
      span_type: 'EnterPlanMode',
      type: 'user',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            content: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
            tool_use_id: 'toolu_01U3MQbUE7bmTs1SnJx4SPU3',
          },
        ],
      },
      tool_use_result: {
        message: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
      },
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('keeps non-plan tool_result user messages visible', () => {
    const parent = {
      type: 'user',
      span_type: 'Read',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            content: 'file contents',
            tool_use_id: 'toolu_read_1',
          },
        ],
      },
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'tool_result' })
  })

  it('classifies assistant thinking with visible text', () => {
    const parent = {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Let me consider...', signature: 'sig' },
        ],
      },
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'assistant_thinking' })
  })

  it('hides task_updated system messages', () => {
    const parent = {
      type: 'system',
      subtype: 'task_updated',
      task_id: 'bi3vq0jmx',
      patch: { is_backgrounded: true },
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides every system subtype the contract table lists', () => {
    // A stored row of each entry draws nothing. The Worker never writes one, so
    // these are rows an older build stored.
    for (const subtype of Object.values(CLAUDE_HIDDEN_SYSTEM_SUBTYPE)) {
      expect(classifyClaudeCodeMessage(input({ type: 'system', subtype, session_id: 's-1' })), subtype)
        .toEqual({ kind: 'hidden' })
    }
  })

  it('hides a stored permission_check_status row and a stored vcs_state_changed row', () => {
    // The two shapes an audit found stored and drawn as no-display cards.
    expect(classifyClaudeCodeMessage(input({
      type: 'system',
      subtype: CLAUDE_HIDDEN_SYSTEM_SUBTYPE.PermissionCheckStatus,
      tool_use_id: 'toolu_1',
      status: 'checking',
      uuid: 'u-1',
      session_id: 's-1',
    }))).toEqual({ kind: 'hidden' })
    expect(classifyClaudeCodeMessage(input({
      type: 'system',
      subtype: CLAUDE_HIDDEN_SYSTEM_SUBTYPE.VcsStateChanged,
      kind: 'push',
      branch: 'main',
      cwd: '/w',
      uuid: 'u-1',
      session_id: 's-1',
    }))).toEqual({ kind: 'hidden' })
  })

  it('reads a stored informational row without an obsolete level filter', () => {
    expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'informational', content: 'verbose only', level: 'info' })))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'verbose only' }] })
    for (const level of ['notice', 'suggestion', 'warning']) {
      expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'informational', content: 'Stop hook failed: boom', level })), level)
        .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Stop hook failed: boom' }] })
    }
    // A frame that states no level draws its content.
    expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'informational', content: 'Stop hook failed: boom' })))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Stop hook failed: boom' }] })
  })

  it('hides a row of a known subtype whose reader finds no words', () => {
    // A known frame that states nothing draws nothing. The no-display card is
    // for a frame that no reader knows.
    for (const frame of [
      { type: 'system', subtype: 'notification', key: 'compact-error', text: '', priority: 'high', color: 'error' },
      { type: 'system', subtype: 'memory_recall', mode: 'select', memories: [] },
      { type: 'system', subtype: 'informational', content: '', level: 'warning' },
      { type: 'system', subtype: 'ui_log', plugin: 'my-plugin', text: '' },
      { type: 'system', subtype: 'model_fallback', content: '' },
    ]) {
      expect(classifyClaudeCodeMessage(input(frame)), frame.subtype).toEqual({ kind: 'hidden' })
    }
  })

  it('keeps a row of an unknown subtype as a notification with no entries', () => {
    // The no-display card that this category draws is the signal that a new
    // frame exists, so an unknown subtype must not hide.
    expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'some_future_event', session_id: 's-1' })))
      .toEqual({ kind: 'notification', entries: [] })
  })

  it('hides a hook_response row unless the hook failed', () => {
    // The neutral reader has no outcome for a successful hook.
    expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'hook_response', hook_id: 'h1', hook_name: 'setup.sh', hook_event: 'SessionStart', outcome: 'success', exit_code: 0 })))
      .toEqual({ kind: 'hidden' })
    expect(classifyClaudeCodeMessage(input({ type: 'system', subtype: 'hook_response', hook_id: 'h1', hook_name: 'setup.sh', hook_event: 'SessionStart', outcome: 'error', exit_code: 2 })))
      .toEqual({ kind: 'notification', entries: [{ kind: 'hook', outcome: 'failed', event: 'SessionStart', name: 'setup.sh', exitCode: 2 }] })
  })

  it('reads a stored informational row in a consolidated thread', () => {
    const info = { type: 'system', subtype: 'informational', content: 'verbose only', level: 'info' }
    const warning = { type: 'system', subtype: 'informational', content: 'Stop hook failed: boom', level: 'warning' }
    const wrapper = { old_seqs: [301, 302], messages: [info, warning] }
    expect(classifyClaudeCodeMessage(input(warning, wrapper)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'verbose only' }, { kind: 'text', text: 'Stop hook failed: boom' }] })
  })

  it('keeps the default empty policy for a stored thread of non-error hook responses', () => {
    const passed = { type: 'system', subtype: 'hook_response', hook_id: 'h1', hook_name: 'setup.sh', hook_event: 'SessionStart', outcome: 'success', exit_code: 0 }
    const wrapper = { old_seqs: [301], messages: [passed] }
    expect(classifyClaudeCodeMessage(input(passed, wrapper))).toEqual({ kind: 'notification', entries: [] })
  })

  it('hides assistant thinking with empty text', () => {
    const parent = {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: '', signature: 'sig' },
        ],
      },
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides TaskList tool_use (chat surface is already covered by the todo sidebar)', () => {
    const parent = {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'tool_use', id: 'toolu_tasklist_1', name: 'TaskList', input: {} },
        ],
      },
    }
    expect(classifyClaudeCodeMessage({ ...input(parent), spanType: 'TaskList' }))
      .toEqual({ kind: 'hidden' })
  })

  it('leaves a stored final status unknown when it is not a failure', () => {
    // The user-facing "Context compacted (...)" line comes from the separate
    // compact_boundary message; this finished status carries nothing to show.
    const parent = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('keeps the default empty policy for a stored thread of successful statuses', () => {
    const statusMsg = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    const wrapper = { old_seqs: [305], messages: [statusMsg] }
    expect(classifyClaudeCodeMessage(input(statusMsg, wrapper))).toEqual({ kind: 'notification', entries: [] })
  })

  it('drops a hidden status from a consolidated thread but keeps the visible notification', () => {
    const settingsMsg = {
      type: 'settings_changed',
      changes: { model: { old: 'a', new: 'b' } },
    }
    const statusMsg = { type: 'system', subtype: 'status', status: null }
    const wrapper = { old_seqs: [301, 302], messages: [settingsMsg, statusMsg] }
    expect(classifyClaudeCodeMessage(input(settingsMsg, wrapper)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'settings-changed', changes: [{ label: 'Model', old: 'a', new: 'b' }] }] })
  })

  it('reads no compaction start from a stored status in a thread', () => {
    const compactingMsg = { type: 'system', subtype: 'status', status: 'compacting' }
    const wrapper = { old_seqs: [305], messages: [compactingMsg] }
    expect(classifyClaudeCodeMessage(input(compactingMsg, wrapper)))
      .toEqual({ kind: 'notification', entries: [] })
  })

  it('draws a failed final compaction status as a failed compaction end', () => {
    const frame = { type: 'system', subtype: 'status', status: null, compact_result: 'failed', compact_error: 'too_few_groups' }
    const expected = { kind: 'notification', entries: [{ kind: 'compaction', phase: 'end', outcome: 'failed', error: 'too_few_groups' }] }
    expect(classifyClaudeCodeMessage(input(frame))).toEqual(expected)
    expect(classifyClaudeCodeMessage(input(frame, { old_seqs: [1], messages: [frame] }))).toEqual(expected)
  })

  it('leaves a stored conversation_reset row unknown', () => {
    expect(classifyClaudeCodeMessage(input({ type: 'conversation_reset' }))).toEqual({ kind: 'unknown' })
  })

  it('drops an allowed rate_limit_event from a consolidated thread (regression guard)', () => {
    const allowed = { type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } }
    const throttled = { type: 'rate_limit_event', rate_limit_info: { status: 'throttled', rateLimitType: 'primary' } }
    const wrapper = { old_seqs: [310, 311], messages: [throttled, allowed] }
    const result = classifyClaudeCodeMessage(input(throttled, wrapper))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(1)
  })

  // The envelope `type` comes straight off the wire, and the two classifier tables are
  // plain objects. A value that identifies an `Object.prototype` member answered with a
  // FUNCTION, which the dispatch below then called.
  it.each(['constructor', 'toString', 'valueOf', 'hasOwnProperty'])('classifies an envelope typed %s as unknown', (type) => {
    expect(classifyClaudeCodeMessage(input({ type }))).toEqual({ kind: 'unknown' })
  })

  it('still classifies each type the tables do hold', () => {
    expect(classifyClaudeCodeMessage(input({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } })))
      .toEqual({ kind: 'assistant_text' })
    expect(classifyClaudeCodeMessage(input({ type: 'user', message: { content: 'hi' } })))
      .toEqual({ kind: 'unknown' })
  })
})

// The current worker stores tool results from a child but drops its other native user frames.
describe('classifyClaudeCodeMessage on a user message carrying parent_tool_use_id', () => {
  // The real payload from a stopped subagent's transcript.
  const interrupted = {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'text', text: '[Request interrupted by user]' }],
    },
    parent_tool_use_id: 'toolu_017mp825HZEDTn7h565GkKr1',
    session_id: '54b79798-6a44-45b1-9bb9-27364aaf83e4',
    subagent_type: 'general-purpose',
    task_description: 'FOOTGUNS angle',
  }

  it('leaves a stored forwarded user frame unknown in the parent transcript', () => {
    expect(classifyClaudeCodeMessage(input(interrupted), { isChildTranscript: false }))
      .toEqual({ kind: 'unknown' })
  })

  it('leaves a stored forwarded user frame unknown in the child transcript', () => {
    expect(classifyClaudeCodeMessage(input(interrupted), { isChildTranscript: true }))
      .toEqual({ kind: 'unknown' })
  })

  it('leaves the stored forwarded frame unknown without transcript context', () => {
    expect(classifyClaudeCodeMessage(input(interrupted))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored user frame with string content unknown', () => {
    expect(classifyClaudeCodeMessage(input({ type: 'user', message: { role: 'user', content: 'old echo' } }))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored compact summary unknown', () => {
    expect(classifyClaudeCodeMessage(input({ type: 'user', isCompactSummary: true, message: { content: [{ type: 'text', text: 'old summary' }] } }))).toEqual({ kind: 'unknown' })
  })

  // A subagent's tool RESULTS carry the same id and must stay tool results on
  // both sides -- the array check runs before the prompt check.
  it('stays a tool result on both sides', () => {
    const toolResult = {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_9', content: 'ok' }] },
      parent_tool_use_id: 'toolu_017mp825HZEDTn7h565GkKr1',
    }
    expect(classifyClaudeCodeMessage(input(toolResult), { isChildTranscript: true })).toEqual({ kind: 'tool_result' })
    expect(classifyClaudeCodeMessage(input(toolResult), { isChildTranscript: false })).toEqual({ kind: 'tool_result' })
  })
})
