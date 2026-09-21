import { describe, expect, it } from 'vitest'
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

  it('hides a finished compaction status (status=null, compact_result=success) standalone', () => {
    // The user-facing "Context compacted (...)" line comes from the separate
    // compact_boundary message; this finished status carries nothing to show.
    const parent = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    expect(classifyClaudeCodeMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a finished compaction status when Hub consolidates it into a notification thread', () => {
    // Regression: the consolidated-thread branch must apply the same per-message
    // hidden rules as the standalone classifier. Before the shared predicate, a
    // status message that is hidden on its own leaked through the wrapper path as
    // a `notification` and rendered as raw JSON.
    const statusMsg = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    const wrapper = { old_seqs: [305], messages: [statusMsg] }
    expect(classifyClaudeCodeMessage(input(statusMsg, wrapper))).toEqual({ kind: 'hidden' })
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

  it('keeps the in-progress compacting status visible in a consolidated thread', () => {
    // status === 'compacting' is the live "Compacting context..." row; only the
    // finished (non-compacting) status is hidden.
    const compactingMsg = { type: 'system', subtype: 'status', status: 'compacting' }
    const wrapper = { old_seqs: [305], messages: [compactingMsg] }
    expect(classifyClaudeCodeMessage(input(compactingMsg, wrapper)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'compaction', phase: 'start' }] })
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
      .toEqual({ kind: 'user_text' })
  })
})

// A user message stamped with the spawning tool_use id means two different
// things depending on which transcript it is in, and only the transcript can
// tell them apart -- Claude forwards a subagent's own messages into the child
// carrying that same id.
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

  it('is the prompt sent to a subagent in the PARENT transcript', () => {
    expect(classifyClaudeCodeMessage(input(interrupted), { isChildTranscript: false }))
      .toEqual({ kind: 'agent_prompt' })
  })

  it('is an ordinary user message in the SUBAGENT\'s own transcript', () => {
    expect(classifyClaudeCodeMessage(input(interrupted), { isChildTranscript: true }))
      .toEqual({ kind: 'user_text' })
  })

  // No context at all keeps the parent reading, which is the one every
  // non-subagent transcript uses.
  it('defaults to the parent reading when the caller supplies no context', () => {
    expect(classifyClaudeCodeMessage(input(interrupted))).toEqual({ kind: 'agent_prompt' })
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
