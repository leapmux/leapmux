import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the Claude plugin.
import './plugin'

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

  it('defaults to the "auto" effort sentinel so Claude Code picks its own default', () => {
    expect(plugin.defaultEffort).toBe('auto')
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
})
