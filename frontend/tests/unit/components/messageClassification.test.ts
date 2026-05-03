import { describe, expect, it } from 'vitest'
import { classifyMessage, messageBubbleClass, messageRowClass } from '~/components/chat/messageClassification'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { input } from '~/components/chat/providers/testUtils'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'

// ---------------------------------------------------------------------------
// Helper to build assistant message payloads
// ---------------------------------------------------------------------------

function assistantMsg(content: unknown[]) {
  return { type: 'assistant', message: { content } }
}

function userMsg(content: unknown) {
  return { type: 'user', message: { content } }
}

function wrapper(firstMessage: Record<string, unknown>) {
  return { old_seqs: [], messages: [firstMessage] }
}

// ---------------------------------------------------------------------------
// classifyMessage
// ---------------------------------------------------------------------------

describe('classifyMessage', () => {
  // -- notification_thread --------------------------------------------------

  describe('notification_thread', () => {
    it('classifies wrapper with settings_changed first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'settings_changed' })))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with context_cleared first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'context_cleared' })))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper when a notification appears after a hidden lifecycle message', () => {
      const result = classifyMessage(input(undefined, {
        old_seqs: [],
        messages: [
          { type: 'system', subtype: 'init' },
          { type: 'context_cleared' },
        ],
      }))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with interrupted first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'interrupted' })))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with non-allowed rate_limit as notification_thread', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'rate_limit_event', rate_limit_info: { status: 'rate_limited' } })))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with only allowed rate_limit as hidden', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } })))
      expect(result.kind).toBe('hidden')
    })

    it('filters allowed rate_limit from mixed notification thread', () => {
      const msgs = [
        { type: 'settings_changed', changes: {} },
        { type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } },
      ]
      const result = classifyMessage(input(undefined, { old_seqs: [], messages: msgs }))
      expect(result.kind).toBe('notification_thread')
      if (result.kind === 'notification_thread') {
        expect(result.messages).toHaveLength(1)
        expect((result.messages[0] as Record<string, unknown>).type).toBe('settings_changed')
      }
    })

    it('classifies wrapper with system (non-init, non-task_notification) first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'system', subtype: 'compact_boundary' })))
      expect(result.kind).toBe('notification_thread')
    })

    it('does not classify wrapper with system init as notification_thread', () => {
      const parent = { type: 'system', subtype: 'init' }
      const result = classifyMessage(input(parent, wrapper(parent)))
      expect(result.kind).not.toBe('notification_thread')
    })

    it('does not classify wrapper with system task_notification as notification_thread', () => {
      const parent = { type: 'system', subtype: 'task_notification' }
      const result = classifyMessage(input(parent, wrapper(parent)))
      expect(result.kind).not.toBe('notification_thread')
    })

    it('does not classify null wrapper as notification_thread', () => {
      const result = classifyMessage(input({ type: 'assistant' }))
      expect(result.kind).not.toBe('notification_thread')
    })

    it('classifies empty messages array as hidden (consolidated no-op)', () => {
      const result = classifyMessage(input(undefined, { old_seqs: [], messages: [] }))
      // Empty wrapper (all notifications consolidated to no-ops) is hidden
      expect(result.kind).toBe('hidden')
    })

    it('returns messages array in notification_thread category', () => {
      const msgs = [{ type: 'settings_changed' }, { type: 'other' }]
      const result = classifyMessage(input(undefined, { old_seqs: [], messages: msgs }))
      expect(result.kind).toBe('notification_thread')
      if (result.kind === 'notification_thread') {
        expect(result.messages).toStrictEqual(msgs)
      }
    })
  })

  // -- unknown (null parent) ------------------------------------------------

  it('returns unknown when parentObject is undefined and wrapper is null', () => {
    expect(classifyMessage(input()).kind).toBe('unknown')
  })

  // -- hidden ---------------------------------------------------------------

  describe('hidden', () => {
    it('classifies system init as hidden', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'init' }))
      expect(result.kind).toBe('hidden')
    })

    it('classifies system status (non-compacting) as hidden', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'status', status: 'running' }))
      expect(result.kind).toBe('hidden')
    })

    it('does not classify system status compacting as hidden', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'status', status: 'compacting' }))
      expect(result.kind).not.toBe('hidden')
    })
  })

  // -- task_notification ----------------------------------------------------

  it('classifies system task_notification as hidden', () => {
    const result = classifyMessage(input({ type: 'system', subtype: 'task_notification' }))
    expect(result.kind).toBe('hidden')
  })

  // -- notification (system fallback) ---------------------------------------

  describe('notification', () => {
    it('classifies system compact_boundary as notification', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'compact_boundary' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies system microcompact_boundary as notification', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'microcompact_boundary' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies system with unknown subtype as notification', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'something_else' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies system status compacting as notification', () => {
      const result = classifyMessage(input({ type: 'system', subtype: 'status', status: 'compacting' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies interrupted as notification', () => {
      const result = classifyMessage(input({ type: 'interrupted' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies context_cleared as notification', () => {
      const result = classifyMessage(input({ type: 'context_cleared' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies settings_changed as notification', () => {
      const result = classifyMessage(input({ type: 'settings_changed' }))
      expect(result.kind).toBe('notification')
    })

    it('classifies non-allowed rate_limit as notification', () => {
      const result = classifyMessage(input({ type: 'rate_limit_event', rate_limit_info: { rateLimitType: 'five_hour', status: 'rate_limited' } }))
      expect(result.kind).toBe('notification')
    })

    it('classifies allowed rate_limit as hidden', () => {
      const result = classifyMessage(input({ type: 'rate_limit_event', rate_limit_info: { status: 'allowed' } }))
      expect(result.kind).toBe('hidden')
    })
  })

  // -- result_divider -------------------------------------------------------

  it('classifies result as result_divider', () => {
    const result = classifyMessage(input({ type: 'result' }))
    expect(result.kind).toBe('result_divider')
  })

  // -- compact_summary ------------------------------------------------------

  it('classifies isCompactSummary as compact_summary', () => {
    const result = classifyMessage(input({ isCompactSummary: true }))
    expect(result.kind).toBe('compact_summary')
  })

  it('does not classify isCompactSummary=false as compact_summary', () => {
    const result = classifyMessage(input({ isCompactSummary: false }))
    expect(result.kind).not.toBe('compact_summary')
  })

  // -- control_response -----------------------------------------------------

  it('classifies synthetic message with controlResponse', () => {
    const result = classifyMessage(input({ isSynthetic: true, controlResponse: { action: 'approve' } }))
    expect(result.kind).toBe('control_response')
  })

  it('does not classify non-synthetic with controlResponse', () => {
    const result = classifyMessage(input({ controlResponse: { action: 'approve' } }))
    expect(result.kind).not.toBe('control_response')
  })

  it('does not classify synthetic without controlResponse object', () => {
    const result = classifyMessage(input({ isSynthetic: true, controlResponse: 'string' }))
    expect(result.kind).not.toBe('control_response')
  })

  // -- assistant messages ---------------------------------------------------

  describe('assistant messages', () => {
    it('classifies tool_use', () => {
      const toolUseBlock = { type: 'tool_use', name: 'Bash', input: { command: 'ls' } }
      const result = classifyMessage(input(assistantMsg([toolUseBlock])))
      expect(result.kind).toBe('tool_use')
      if (result.kind === 'tool_use') {
        expect(result.toolName).toBe('Bash')
        expect(result.toolUse).toBe(toolUseBlock)
        expect(result.content).toEqual([toolUseBlock])
      }
    })

    it('tool_use takes priority over text', () => {
      const toolUseBlock = { type: 'tool_use', name: 'Read', input: {} }
      const textBlock = { type: 'text', text: 'hello' }
      const result = classifyMessage(input(assistantMsg([textBlock, toolUseBlock])))
      expect(result.kind).toBe('tool_use')
    })

    it('tool_use takes priority over thinking', () => {
      const toolUseBlock = { type: 'tool_use', name: 'Read', input: {} }
      const thinkingBlock = { type: 'thinking', thinking: 'some reasoning' }
      const result = classifyMessage(input(assistantMsg([thinkingBlock, toolUseBlock])))
      expect(result.kind).toBe('tool_use')
    })

    it('classifies assistant_text', () => {
      const result = classifyMessage(input(assistantMsg([{ type: 'text', text: 'hello' }])))
      expect(result.kind).toBe('assistant_text')
    })

    it('text takes priority over thinking', () => {
      const textBlock = { type: 'text', text: 'hello' }
      const thinkingBlock = { type: 'thinking', thinking: 'some reasoning' }
      const result = classifyMessage(input(assistantMsg([thinkingBlock, textBlock])))
      expect(result.kind).toBe('assistant_text')
    })

    it('classifies assistant_thinking', () => {
      const result = classifyMessage(input(
        assistantMsg([{ type: 'thinking', thinking: 'reasoning here', signature: 'sig' }]),
      ))
      expect(result.kind).toBe('assistant_thinking')
    })

    it('returns unknown for assistant with no message field', () => {
      const result = classifyMessage(input({ type: 'assistant' }))
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with empty content array', () => {
      const result = classifyMessage(input(assistantMsg([])))
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with non-array content', () => {
      const result = classifyMessage(input({ type: 'assistant', message: { content: 'string' } }))
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with non-object message', () => {
      const result = classifyMessage(input({ type: 'assistant', message: 'not an object' }))
      expect(result.kind).toBe('unknown')
    })

    it('tool_use with empty name defaults to empty string', () => {
      const result = classifyMessage(input(assistantMsg([{ type: 'tool_use' }])))
      expect(result.kind).toBe('tool_use')
      if (result.kind === 'tool_use') {
        expect(result.toolName).toBe('')
      }
    })
  })

  // -- user messages --------------------------------------------------------

  describe('user messages', () => {
    it('classifies user_text (string content)', () => {
      const result = classifyMessage(input(userMsg('hello world')))
      expect(result.kind).toBe('user_text')
    })

    it('classifies tool_result', () => {
      const result = classifyMessage(input(
        userMsg([{ type: 'tool_result', content: 'result' }]),
      ))
      expect(result.kind).toBe('tool_result')
    })

    it('returns unknown for user with no message field', () => {
      const result = classifyMessage(input({ type: 'user' }))
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for user with non-object message', () => {
      const result = classifyMessage(input({ type: 'user', message: 'string' }))
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for user with array content but no tool_result', () => {
      const result = classifyMessage(input(userMsg([{ type: 'text', text: 'hi' }])))
      expect(result.kind).toBe('unknown')
    })
  })

  // -- user_content ---------------------------------------------------------

  it('classifies plain object with string content and no type as user_content', () => {
    const result = classifyMessage(input({ content: 'hello' }))
    expect(result.kind).toBe('user_content')
  })

  it('does not classify object with type and content as user_content', () => {
    const result = classifyMessage(input({ type: 'something', content: 'hello' }))
    expect(result.kind).not.toBe('user_content')
  })

  // -- unknown (fallback) ---------------------------------------------------

  it('returns unknown for unrecognized type', () => {
    const result = classifyMessage(input({ type: 'something_unknown' }))
    expect(result.kind).toBe('unknown')
  })

  it('returns unknown for empty object', () => {
    const result = classifyMessage(input({}))
    expect(result.kind).toBe('unknown')
  })
})

// ---------------------------------------------------------------------------
// messageRowClass
// ---------------------------------------------------------------------------

describe('messageRowClass', () => {
  it('returns messageRowCenter for notification', () => {
    expect(messageRowClass('notification', MessageSource.AGENT)).toBe(chatStyles.messageRowCenter)
  })

  it('returns messageRowCenter for notification_thread', () => {
    expect(messageRowClass('notification_thread', MessageSource.AGENT)).toBe(chatStyles.messageRowCenter)
  })

  it('returns messageRowEnd for non-meta user messages', () => {
    expect(messageRowClass('user_text', MessageSource.USER)).toBe(chatStyles.messageRowEnd)
    expect(messageRowClass('user_content', MessageSource.USER)).toBe(chatStyles.messageRowEnd)
  })

  it('returns messageRow for assistant_text', () => {
    expect(messageRowClass('assistant_text', MessageSource.AGENT)).toBe(chatStyles.messageRow)
  })

  it('returns messageRow for assistant_thinking', () => {
    expect(messageRowClass('assistant_thinking', MessageSource.AGENT)).toBe(chatStyles.messageRow)
  })

  it('returns messageRow for meta kinds even with USER source', () => {
    expect(messageRowClass('tool_use', MessageSource.USER)).toBe(chatStyles.messageRow)
    expect(messageRowClass('tool_result', MessageSource.USER)).toBe(chatStyles.messageRow)
    expect(messageRowClass('hidden', MessageSource.USER)).toBe(chatStyles.messageRow)
  })
})

// ---------------------------------------------------------------------------
// messageBubbleClass
// ---------------------------------------------------------------------------

describe('messageBubbleClass', () => {
  it('returns systemMessage for notification', () => {
    expect(messageBubbleClass('notification', MessageSource.AGENT)).toBe(chatStyles.systemMessage)
  })

  it('returns systemMessage for notification_thread', () => {
    expect(messageBubbleClass('notification_thread', MessageSource.AGENT)).toBe(chatStyles.systemMessage)
  })

  it('returns thinkingMessage for assistant_thinking', () => {
    expect(messageBubbleClass('assistant_thinking', MessageSource.AGENT)).toBe(chatStyles.thinkingMessage)
  })

  it('returns metaMessage for meta kinds', () => {
    expect(messageBubbleClass('tool_use', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('tool_result', MessageSource.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('hidden', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('result_divider', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('control_response', MessageSource.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('compact_summary', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('task_notification', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
  })

  it('returns assistantMessage for assistant_text with AGENT source', () => {
    expect(messageBubbleClass('assistant_text', MessageSource.AGENT)).toBe(chatStyles.assistantMessage)
  })

  it('returns userMessage for user_text with USER source', () => {
    expect(messageBubbleClass('user_text', MessageSource.USER)).toBe(chatStyles.userMessage)
  })

  it('returns userMessage for user_content with USER source', () => {
    expect(messageBubbleClass('user_content', MessageSource.USER)).toBe(chatStyles.userMessage)
  })

  it('returns systemMessage for unknown kind with LEAPMUX source', () => {
    expect(messageBubbleClass('unknown', MessageSource.LEAPMUX)).toBe(chatStyles.systemMessage)
  })
})
