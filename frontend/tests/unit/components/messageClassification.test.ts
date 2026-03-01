import { describe, expect, it } from 'vitest'
import { classifyMessage, messageBubbleClass, messageRowClass } from '~/components/chat/messageClassification'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'

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
      const result = classifyMessage(undefined, wrapper({ type: 'settings_changed' }))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with context_cleared first message', () => {
      const result = classifyMessage(undefined, wrapper({ type: 'context_cleared' }))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with interrupted first message', () => {
      const result = classifyMessage(undefined, wrapper({ type: 'interrupted' }))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with non-allowed rate_limit as notification_thread', () => {
      const result = classifyMessage(undefined, wrapper({ type: 'rate_limit', rate_limit_info: { status: 'rate_limited' } }))
      expect(result.kind).toBe('notification_thread')
    })

    it('classifies wrapper with only allowed rate_limit as hidden', () => {
      const result = classifyMessage(undefined, wrapper({ type: 'rate_limit', rate_limit_info: { status: 'allowed' } }))
      expect(result.kind).toBe('hidden')
    })

    it('filters allowed rate_limit from mixed notification thread', () => {
      const msgs = [
        { type: 'settings_changed', changes: {} },
        { type: 'rate_limit', rate_limit_info: { status: 'allowed' } },
      ]
      const result = classifyMessage(undefined, { old_seqs: [], messages: msgs })
      expect(result.kind).toBe('notification_thread')
      if (result.kind === 'notification_thread') {
        expect(result.messages).toHaveLength(1)
        expect((result.messages[0] as Record<string, unknown>).type).toBe('settings_changed')
      }
    })

    it('classifies wrapper with system (non-init, non-task_notification) first message', () => {
      const result = classifyMessage(undefined, wrapper({ type: 'system', subtype: 'compact_boundary' }))
      expect(result.kind).toBe('notification_thread')
    })

    it('does not classify wrapper with system init as notification_thread', () => {
      const parent = { type: 'system', subtype: 'init' }
      const result = classifyMessage(parent, wrapper(parent))
      expect(result.kind).not.toBe('notification_thread')
    })

    it('does not classify wrapper with system task_notification as notification_thread', () => {
      const parent = { type: 'system', subtype: 'task_notification' }
      const result = classifyMessage(parent, wrapper(parent))
      expect(result.kind).not.toBe('notification_thread')
    })

    it('does not classify null wrapper as notification_thread', () => {
      const result = classifyMessage({ type: 'assistant' }, null)
      expect(result.kind).not.toBe('notification_thread')
    })

    it('classifies empty messages array as hidden (consolidated no-op)', () => {
      const result = classifyMessage(undefined, { old_seqs: [], messages: [] })
      // Empty wrapper (all notifications consolidated to no-ops) is hidden
      expect(result.kind).toBe('hidden')
    })

    it('returns messages array in notification_thread category', () => {
      const msgs = [{ type: 'settings_changed' }, { type: 'other' }]
      const result = classifyMessage(undefined, { old_seqs: [], messages: msgs })
      expect(result.kind).toBe('notification_thread')
      if (result.kind === 'notification_thread') {
        expect(result.messages).toStrictEqual(msgs)
      }
    })
  })

  // -- unknown (null parent) ------------------------------------------------

  it('returns unknown when parentObject is undefined and wrapper is null', () => {
    expect(classifyMessage(undefined, null).kind).toBe('unknown')
  })

  // -- hidden ---------------------------------------------------------------

  describe('hidden', () => {
    it('classifies system init as hidden', () => {
      const result = classifyMessage({ type: 'system', subtype: 'init' }, null)
      expect(result.kind).toBe('hidden')
    })

    it('classifies system status (non-compacting) as hidden', () => {
      const result = classifyMessage({ type: 'system', subtype: 'status', status: 'running' }, null)
      expect(result.kind).toBe('hidden')
    })

    it('does not classify system status compacting as hidden', () => {
      const result = classifyMessage({ type: 'system', subtype: 'status', status: 'compacting' }, null)
      expect(result.kind).not.toBe('hidden')
    })
  })

  // -- task_notification ----------------------------------------------------

  it('classifies system task_notification', () => {
    const result = classifyMessage({ type: 'system', subtype: 'task_notification' }, null)
    expect(result.kind).toBe('task_notification')
  })

  // -- notification (system fallback) ---------------------------------------

  describe('notification', () => {
    it('classifies system compact_boundary as notification', () => {
      const result = classifyMessage({ type: 'system', subtype: 'compact_boundary' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies system microcompact_boundary as notification', () => {
      const result = classifyMessage({ type: 'system', subtype: 'microcompact_boundary' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies system with unknown subtype as notification', () => {
      const result = classifyMessage({ type: 'system', subtype: 'something_else' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies system status compacting as notification', () => {
      const result = classifyMessage({ type: 'system', subtype: 'status', status: 'compacting' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies interrupted as notification', () => {
      const result = classifyMessage({ type: 'interrupted' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies context_cleared as notification', () => {
      const result = classifyMessage({ type: 'context_cleared' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies settings_changed as notification', () => {
      const result = classifyMessage({ type: 'settings_changed' }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies non-allowed rate_limit as notification', () => {
      const result = classifyMessage({ type: 'rate_limit', rate_limit_info: { rateLimitType: 'five_hour', status: 'rate_limited' } }, null)
      expect(result.kind).toBe('notification')
    })

    it('classifies allowed rate_limit as hidden', () => {
      const result = classifyMessage({ type: 'rate_limit', rate_limit_info: { status: 'allowed' } }, null)
      expect(result.kind).toBe('hidden')
    })
  })

  // -- result_divider -------------------------------------------------------

  it('classifies result as result_divider', () => {
    const result = classifyMessage({ type: 'result' }, null)
    expect(result.kind).toBe('result_divider')
  })

  // -- compact_summary ------------------------------------------------------

  it('classifies isCompactSummary as compact_summary', () => {
    const result = classifyMessage({ isCompactSummary: true }, null)
    expect(result.kind).toBe('compact_summary')
  })

  it('does not classify isCompactSummary=false as compact_summary', () => {
    const result = classifyMessage({ isCompactSummary: false }, null)
    expect(result.kind).not.toBe('compact_summary')
  })

  // -- control_response -----------------------------------------------------

  it('classifies synthetic message with controlResponse', () => {
    const result = classifyMessage({ isSynthetic: true, controlResponse: { action: 'approve' } }, null)
    expect(result.kind).toBe('control_response')
  })

  it('does not classify non-synthetic with controlResponse', () => {
    const result = classifyMessage({ controlResponse: { action: 'approve' } }, null)
    expect(result.kind).not.toBe('control_response')
  })

  it('does not classify synthetic without controlResponse object', () => {
    const result = classifyMessage({ isSynthetic: true, controlResponse: 'string' }, null)
    expect(result.kind).not.toBe('control_response')
  })

  // -- assistant messages ---------------------------------------------------

  describe('assistant messages', () => {
    it('classifies tool_use', () => {
      const toolUseBlock = { type: 'tool_use', name: 'Bash', input: { command: 'ls' } }
      const result = classifyMessage(assistantMsg([toolUseBlock]), null)
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
      const result = classifyMessage(assistantMsg([textBlock, toolUseBlock]), null)
      expect(result.kind).toBe('tool_use')
    })

    it('tool_use takes priority over thinking', () => {
      const toolUseBlock = { type: 'tool_use', name: 'Read', input: {} }
      const thinkingBlock = { type: 'thinking', thinking: 'some reasoning' }
      const result = classifyMessage(assistantMsg([thinkingBlock, toolUseBlock]), null)
      expect(result.kind).toBe('tool_use')
    })

    it('classifies assistant_text', () => {
      const result = classifyMessage(assistantMsg([{ type: 'text', text: 'hello' }]), null)
      expect(result.kind).toBe('assistant_text')
    })

    it('text takes priority over thinking', () => {
      const textBlock = { type: 'text', text: 'hello' }
      const thinkingBlock = { type: 'thinking', thinking: 'some reasoning' }
      const result = classifyMessage(assistantMsg([thinkingBlock, textBlock]), null)
      expect(result.kind).toBe('assistant_text')
    })

    it('classifies assistant_thinking', () => {
      const result = classifyMessage(
        assistantMsg([{ type: 'thinking', thinking: 'reasoning here', signature: 'sig' }]),
        null,
      )
      expect(result.kind).toBe('assistant_thinking')
    })

    it('returns unknown for assistant with no message field', () => {
      const result = classifyMessage({ type: 'assistant' }, null)
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with empty content array', () => {
      const result = classifyMessage(assistantMsg([]), null)
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with non-array content', () => {
      const result = classifyMessage({ type: 'assistant', message: { content: 'string' } }, null)
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for assistant with non-object message', () => {
      const result = classifyMessage({ type: 'assistant', message: 'not an object' }, null)
      expect(result.kind).toBe('unknown')
    })

    it('tool_use with empty name defaults to empty string', () => {
      const result = classifyMessage(assistantMsg([{ type: 'tool_use' }]), null)
      expect(result.kind).toBe('tool_use')
      if (result.kind === 'tool_use') {
        expect(result.toolName).toBe('')
      }
    })
  })

  // -- user messages --------------------------------------------------------

  describe('user messages', () => {
    it('classifies user_text (string content)', () => {
      const result = classifyMessage(userMsg('hello world'), null)
      expect(result.kind).toBe('user_text')
    })

    it('classifies tool_result', () => {
      const result = classifyMessage(
        userMsg([{ type: 'tool_result', content: 'result' }]),
        null,
      )
      expect(result.kind).toBe('tool_result')
    })

    it('returns unknown for user with no message field', () => {
      const result = classifyMessage({ type: 'user' }, null)
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for user with non-object message', () => {
      const result = classifyMessage({ type: 'user', message: 'string' }, null)
      expect(result.kind).toBe('unknown')
    })

    it('returns unknown for user with array content but no tool_result', () => {
      const result = classifyMessage(userMsg([{ type: 'text', text: 'hi' }]), null)
      expect(result.kind).toBe('unknown')
    })
  })

  // -- user_content ---------------------------------------------------------

  it('classifies plain object with string content and no type as user_content', () => {
    const result = classifyMessage({ content: 'hello' }, null)
    expect(result.kind).toBe('user_content')
  })

  it('does not classify object with type and content as user_content', () => {
    const result = classifyMessage({ type: 'something', content: 'hello' }, null)
    expect(result.kind).not.toBe('user_content')
  })

  // -- unknown (fallback) ---------------------------------------------------

  it('returns unknown for unrecognized type', () => {
    const result = classifyMessage({ type: 'something_unknown' }, null)
    expect(result.kind).toBe('unknown')
  })

  it('returns unknown for empty object', () => {
    const result = classifyMessage({}, null)
    expect(result.kind).toBe('unknown')
  })
})

// ---------------------------------------------------------------------------
// messageRowClass
// ---------------------------------------------------------------------------

describe('messageRowClass', () => {
  it('returns messageRowCenter for notification', () => {
    expect(messageRowClass('notification', MessageRole.SYSTEM)).toBe(chatStyles.messageRowCenter)
  })

  it('returns messageRowCenter for notification_thread', () => {
    expect(messageRowClass('notification_thread', MessageRole.SYSTEM)).toBe(chatStyles.messageRowCenter)
  })

  it('returns messageRowEnd for non-meta user messages', () => {
    expect(messageRowClass('user_text', MessageRole.USER)).toBe(chatStyles.messageRowEnd)
    expect(messageRowClass('user_content', MessageRole.USER)).toBe(chatStyles.messageRowEnd)
  })

  it('returns messageRow for assistant_text', () => {
    expect(messageRowClass('assistant_text', MessageRole.ASSISTANT)).toBe(chatStyles.messageRow)
  })

  it('returns messageRow for assistant_thinking', () => {
    expect(messageRowClass('assistant_thinking', MessageRole.ASSISTANT)).toBe(chatStyles.messageRow)
  })

  it('returns messageRow for meta kinds even with USER role', () => {
    expect(messageRowClass('tool_use', MessageRole.USER)).toBe(chatStyles.messageRow)
    expect(messageRowClass('tool_result', MessageRole.USER)).toBe(chatStyles.messageRow)
    expect(messageRowClass('hidden', MessageRole.USER)).toBe(chatStyles.messageRow)
  })
})

// ---------------------------------------------------------------------------
// messageBubbleClass
// ---------------------------------------------------------------------------

describe('messageBubbleClass', () => {
  it('returns systemMessage for notification', () => {
    expect(messageBubbleClass('notification', MessageRole.SYSTEM)).toBe(chatStyles.systemMessage)
  })

  it('returns systemMessage for notification_thread', () => {
    expect(messageBubbleClass('notification_thread', MessageRole.SYSTEM)).toBe(chatStyles.systemMessage)
  })

  it('returns thinkingMessage for assistant_thinking', () => {
    expect(messageBubbleClass('assistant_thinking', MessageRole.ASSISTANT)).toBe(chatStyles.thinkingMessage)
  })

  it('returns metaMessage for meta kinds', () => {
    expect(messageBubbleClass('tool_use', MessageRole.ASSISTANT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('tool_result', MessageRole.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('hidden', MessageRole.SYSTEM)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('result_divider', MessageRole.SYSTEM)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('control_response', MessageRole.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('compact_summary', MessageRole.SYSTEM)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('task_notification', MessageRole.SYSTEM)).toBe(chatStyles.metaMessage)
  })

  it('returns assistantMessage for assistant_text with ASSISTANT role', () => {
    expect(messageBubbleClass('assistant_text', MessageRole.ASSISTANT)).toBe(chatStyles.assistantMessage)
  })

  it('returns userMessage for user_text with USER role', () => {
    expect(messageBubbleClass('user_text', MessageRole.USER)).toBe(chatStyles.userMessage)
  })

  it('returns userMessage for user_content with USER role', () => {
    expect(messageBubbleClass('user_content', MessageRole.USER)).toBe(chatStyles.userMessage)
  })

  it('returns systemMessage for unknown kind with unrecognized role', () => {
    expect(messageBubbleClass('unknown', MessageRole.SYSTEM)).toBe(chatStyles.systemMessage)
  })
})
