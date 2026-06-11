import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the Claude plugin.
import './plugin'

describe('claude clearsThinkingTokensForMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('always clears, even for a non-empty parentSpanId (telemetry-driven counter)', () => {
    // Claude's parentSpanId is not a clean main-vs-subagent signal (a
    // system-injected tool_use_id yields a non-empty parentSpanId on a main-agent
    // message), so it must not gate on it like the estimator providers do.
    expect(plugin.clearsThinkingTokensForMessage!({ parentSpanId: '' })).toBe(true)
    expect(plugin.clearsThinkingTokensForMessage!({ parentSpanId: 'sys-tu-999' })).toBe(true)
  })
})

describe('claude extractQuotableText', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('joins text + thinking blocks as paragraphs (preserves order, ≥2 newlines between blocks)', () => {
    const parent = {
      type: 'assistant',
      message: {
        content: [
          { type: 'text', text: 'Hello' },
          { type: 'thinking', thinking: 'pondering' },
          { type: 'tool_use', name: 'Read' },
        ],
      },
    }
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, input(parent))).toBe('Hello\n\npondering')
  })

  it('returns null when assistant message has no quotable content', () => {
    const parent = {
      type: 'assistant',
      message: { content: [{ type: 'tool_use', name: 'Read' }] },
    }
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, input(parent))).toBeNull()
  })

  it('reads message.content string for user_text', () => {
    const parent = { type: 'user', message: { content: '  hello  ' } }
    expect(plugin.extractQuotableText!({ kind: 'user_text' }, input(parent))).toBe('hello')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(plugin.extractQuotableText!({ kind: 'user_content' }, input({ content: ' hi ' }))).toBe('hi')
    expect(plugin.extractQuotableText!({ kind: 'plan_execution' }, input({ content: 'plan' }))).toBe('plan')
  })

  it('returns null for non-quotable categories', () => {
    expect(plugin.extractQuotableText!({ kind: 'hidden' }, input({ type: 'assistant', message: { content: [{ type: 'text', text: 'x' }] } }))).toBeNull()
  })
})

describe('claude classify', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      binary: false,
    })
  })

  it('renders the permissionMode group as the trigger mode segment', () => {
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
  })

  it('classifies result divider', () => {
    const parent = {
      type: 'result',
      subtype: 'success',
      result: 'Done',
      duration_ms: 1234,
      num_turns: 1,
      stop_reason: 'end_turn',
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('classifies error result divider', () => {
    const parent = {
      type: 'result',
      is_error: true,
      errors: ['something went wrong'],
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'result_divider' })
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'result_divider' })
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'tool_result' })
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_thinking' })
  })

  it('hides task_updated system messages', () => {
    const parent = {
      type: 'system',
      subtype: 'task_updated',
      task_id: 'bi3vq0jmx',
      patch: { is_backgrounded: true },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
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
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
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
    expect(plugin.classify({ ...input(parent), spanType: 'TaskList' }))
      .toEqual({ kind: 'hidden' })
  })

  it('hides a terminal compaction status (status=null, compact_result=success) standalone', () => {
    // The user-facing "Context compacted (...)" line comes from the separate
    // compact_boundary message; this terminal status carries nothing to show.
    const parent = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a terminal compaction status when Hub consolidates it into a notification thread', () => {
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
    expect(plugin.classify(input(statusMsg, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('drops a hidden status from a consolidated thread but keeps the visible notification', () => {
    const settingsMsg = {
      type: 'settings_changed',
      changes: { model: { old: 'a', new: 'b' } },
    }
    const statusMsg = { type: 'system', subtype: 'status', status: null }
    const wrapper = { old_seqs: [301, 302], messages: [settingsMsg, statusMsg] }
    expect(plugin.classify(input(settingsMsg, wrapper)))
      .toEqual({ kind: 'notification', messages: [settingsMsg] })
  })

  it('keeps the in-progress compacting status visible in a consolidated thread', () => {
    // status === 'compacting' is the live "Compacting context..." row; only the
    // terminal (non-compacting) status is hidden.
    const compactingMsg = { type: 'system', subtype: 'status', status: 'compacting' }
    const wrapper = { old_seqs: [305], messages: [compactingMsg] }
    expect(plugin.classify(input(compactingMsg, wrapper)))
      .toEqual({ kind: 'notification', messages: [compactingMsg] })
  })

  it('drops an allowed rate_limit_event from a consolidated thread (regression guard)', () => {
    const allowed = { type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } }
    const throttled = { type: 'rate_limit_event', rate_limit_info: { status: 'throttled', rateLimitType: 'primary' } }
    const wrapper = { old_seqs: [310, 311], messages: [throttled, allowed] }
    expect(plugin.classify(input(throttled, wrapper)))
      .toEqual({ kind: 'notification', messages: [throttled] })
  })
})

describe('claude planMode', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('wires plan mode to the permissionMode group', () => {
    // Claude's plan axis IS its permission mode (permissionMode=plan), so the
    // trigger naturally reads "Plan Mode" while in plan.
    expect(plugin.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: 'plan',
      defaultValue: 'default',
    })
  })

  it('reads the current permission mode from optionValues, defaulting when unset', () => {
    expect(plugin.planMode!.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin.planMode!.currentMode({})).toBe('default')
  })
})
