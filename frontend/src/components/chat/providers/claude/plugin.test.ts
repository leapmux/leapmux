import type { ParsedMessageContent } from '~/lib/messageParser'
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

describe('claude preview text (scroll-rail mark preview)', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('extracts a self-displaying control-response tool_result body (ExitPlanMode / AskUserQuestion answer)', () => {
    // The user's answer/feedback lives inside a tool_result block; is_error is irrelevant.
    const parent = {
      type: 'user',
      message: {
        role: 'user',
        content: [{ type: 'tool_result', content: '> **A stored** column: needs a migration.\n\nWe should go with this option.', is_error: true, tool_use_id: 'toolu_1' }],
      },
      parent_tool_use_id: null,
    }
    // Newlines survive so the tooltip renders the blockquote + paragraph structure.
    expect(plugin.previewText!({ kind: 'tool_result' }, input(parent)))
      .toBe('> **A stored** column: needs a migration.\n\nWe should go with this option.')
  })

  it('extracts a tool_result whose content is itself a block array', () => {
    const parent = {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: [{ type: 'text', text: 'answer text' }] }] },
    }
    expect(plugin.previewText!({ kind: 'tool_result' }, input(parent))).toBe('answer text')
  })

  it('joins multiple tool_result bodies (parallel tool calls) with a blank line', () => {
    const parent = {
      type: 'user',
      message: {
        role: 'user',
        content: [
          { type: 'tool_result', content: 'first', tool_use_id: 'a' },
          { type: 'tool_result', content: 'second', tool_use_id: 'b' },
        ],
      },
    }
    expect(plugin.previewText!({ kind: 'tool_result' }, input(parent))).toBe('first\n\nsecond')
  })

  it('reads the Claude {message:{content}} transcript envelope (a Claude-specific shape, not the shared default)', () => {
    // A transcript user row nests its text under message.content as a string; this Anthropic
    // shape is read here, not by the provider-neutral defaultMarkPreview.
    expect(plugin.previewText!({ kind: 'user_text' }, input({ message: { content: 'typed text' } }))).toBe('typed text')
  })

  it('falls back to the shared neutral extractor for a plain {content} user send', () => {
    expect(plugin.previewText!({ kind: 'user_content' }, input({ content: 'hello world' }))).toBe('hello world')
  })

  it('returns null for an assistant content-block array (no neutral string field)', () => {
    expect(plugin.previewText!({ kind: 'assistant_text' }, input({ message: { content: [{ type: 'text', text: 'hi' }] } }))).toBeNull()
  })

  it('derives the control-response display from the native response envelope (not previewText)', () => {
    // Control-response rows resolve through controlResponseDisplay, not previewText -- Claude's
    // derivation IS the neutral behavior envelope: allow -> Approved, deny+message -> feedback.
    const allow = { type: 'control_response', response: { request_id: 'r', response: { behavior: 'allow' } } }
    expect(plugin.controlResponseDisplay!({ provider: 'CLAUDE_CODE', requestId: 'r', request: undefined, response: allow }))
      .toEqual({ kind: 'label', text: 'Approved' })
    const deny = { type: 'control_response', response: { request_id: 'r', response: { behavior: 'deny', message: 'use ripgrep' } } }
    expect(plugin.controlResponseDisplay!({ provider: 'CLAUDE_CODE', requestId: 'r', request: undefined, response: deny }))
      .toEqual({ kind: 'feedback', message: 'use ripgrep' })
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

describe('claude spanRole', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  // spanRole only reads `parsed.parentObject`; build a minimal parsed shape whose
  // `message.content` holds the Anthropic-style content blocks getMessageContent reads.
  function parsedWithBlocks(blocks: unknown[]): ParsedMessageContent {
    return { rawText: '', topLevel: null, parentObject: { message: { content: blocks } }, wrapper: null }
  }

  it('classifies a tool_use block as the opener', () => {
    expect(plugin.spanRole!(parsedWithBlocks([{ type: 'tool_use' }]))).toBe('opener')
  })

  it('classifies a tool_result block as the result', () => {
    expect(plugin.spanRole!(parsedWithBlocks([{ type: 'tool_result' }]))).toBe('result')
  })

  it('lets the tool_use opener win when a message carries BOTH block types, regardless of order', () => {
    expect(plugin.spanRole!(parsedWithBlocks([{ type: 'tool_result' }, { type: 'tool_use' }]))).toBe('opener')
    expect(plugin.spanRole!(parsedWithBlocks([{ type: 'tool_use' }, { type: 'tool_result' }]))).toBe('opener')
  })

  it('skips non-object blocks and classifies text-only content as other', () => {
    expect(plugin.spanRole!(parsedWithBlocks([null, 'str', { type: 'text' }]))).toBe('other')
  })

  it('returns other when there is no content array', () => {
    expect(plugin.spanRole!({ rawText: '', topLevel: null, parentObject: undefined, wrapper: null })).toBe('other')
  })
})

// Build a plain ParsedMessageContent whose inner message is `inner`;
// rateLimitsFromMessage reads it through getInnerMessage (parentObject ?? topLevel).
function parsed(inner: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: inner, parentObject: inner, wrapper: null }
}

describe('claude rateLimitsFromMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('extracts from a raw rate_limit_event', () => {
    expect(plugin.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
    }))).toEqual([{ key: 'five_hour', info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 } }])
  })

  it('defaults the key to unknown when rateLimitType is missing', () => {
    const result = plugin.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { status: 'exceeded' },
    }))
    expect(result![0].key).toBe('unknown')
  })

  it('returns empty array when rate_limit_info is missing', () => {
    expect(plugin.rateLimitsFromMessage!(parsed({ type: 'rate_limit_event' }))).toEqual([])
  })

  it('returns null for a non-rate_limit_event', () => {
    expect(plugin.rateLimitsFromMessage!(parsed({ type: 'settings_changed' }))).toBeNull()
  })
})

describe('claude contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('reads the Claude message.usage input_tokens + cache_* shape', () => {
    expect(plugin.contextUsageFromMessage!(parsed({ message: { usage: { input_tokens: 1000, cache_creation_input_tokens: 200, cache_read_input_tokens: 300 } } })))
      .toEqual({ inputTokens: 1000, cacheCreationInputTokens: 200, cacheReadInputTokens: 300 })
  })

  it('returns null for a non-Claude usage shape (Pi input, no input_tokens)', () => {
    expect(plugin.contextUsageFromMessage!(parsed({ message: { usage: { input: 100 } } }))).toBeNull()
  })

  it('returns null when the message carries no message.usage', () => {
    expect(plugin.contextUsageFromMessage!(parsed({ type: 'assistant', message: {} }))).toBeNull()
  })
})
