import type { MessageCategory } from '~/components/chat/messageClassification'
import { describe, expect, it } from 'vitest'
import { bubbleRunsToRightEdge, classifyMessage, isMirroredMessageRow, messageBubbleClass, messageRowChrome, messageRowChromeClass, messageRowClass } from '~/components/chat/messageClassification'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { input } from '~/components/chat/providers/testUtils'
import { AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'

/**
 * Every kind the classifier can produce. The `satisfies Record<...>` annotation is
 * the guard: a new member of `MessageCategory` fails `tsc` here until it is listed,
 * which is what forces the whole-domain sweeps below to stay whole.
 */
const ALL_MESSAGE_KINDS = Object.keys({
  agent_prompt: true,
  assistant_text: true,
  assistant_thinking: true,
  compact_summary: true,
  control_response: true,
  hidden: true,
  notification: true,
  plan_execution: true,
  result_divider: true,
  tool_result: true,
  tool_use: true,
  unknown: true,
  unsupported_provider: true,
  user_content: true,
  user_text: true,
} satisfies Record<MessageCategory['kind'], true>) as MessageCategory['kind'][]

/** Every source a persisted row can carry. */
const ALL_MESSAGE_SOURCES = [
  MessageSource.UNSPECIFIED,
  MessageSource.USER,
  MessageSource.AGENT,
  MessageSource.LEAPMUX,
] as const

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
  // -- notification (consolidated thread) -----------------------------------

  describe('notification (consolidated thread)', () => {
    it('classifies wrapper with settings_changed first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'settings_changed' })))
      expect(result.kind).toBe('notification')
    })

    it('classifies wrapper with context_cleared first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'context_cleared' })))
      expect(result.kind).toBe('notification')
    })

    it('classifies wrapper when a notification appears after a hidden lifecycle message', () => {
      const result = classifyMessage(input(undefined, {
        old_seqs: [],
        messages: [
          { type: 'system', subtype: 'init' },
          { type: 'context_cleared' },
        ],
      }))
      expect(result.kind).toBe('notification')
    })

    it('classifies wrapper with interrupted first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'interrupted' })))
      expect(result.kind).toBe('notification')
    })

    it('classifies wrapper with non-allowed rate_limit as a notification', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'rate_limit_event', rate_limit_info: { status: 'rate_limited' } })))
      expect(result.kind).toBe('notification')
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
      expect(result.kind).toBe('notification')
      if (result.kind === 'notification') {
        expect(result.messages).toHaveLength(1)
        expect((result.messages[0] as Record<string, unknown>).type).toBe('settings_changed')
      }
    })

    it('classifies wrapper with system (non-init, non-task_notification) first message', () => {
      const result = classifyMessage(input(undefined, wrapper({ type: 'system', subtype: 'compact_boundary' })))
      expect(result.kind).toBe('notification')
    })

    it('does not classify wrapper with system init as a notification', () => {
      const parent = { type: 'system', subtype: 'init' }
      const result = classifyMessage(input(parent, wrapper(parent)))
      expect(result.kind).not.toBe('notification')
    })

    it('does not classify wrapper with system task_notification as a notification', () => {
      const parent = { type: 'system', subtype: 'task_notification' }
      const result = classifyMessage(input(parent, wrapper(parent)))
      expect(result.kind).not.toBe('notification')
    })

    it('does not classify null wrapper as a notification', () => {
      const result = classifyMessage(input({ type: 'assistant' }))
      expect(result.kind).not.toBe('notification')
    })

    it('classifies empty messages array as hidden (consolidated no-op)', () => {
      const result = classifyMessage(input(undefined, { old_seqs: [], messages: [] }))
      // Empty wrapper (all notifications consolidated to no-ops) is hidden
      expect(result.kind).toBe('hidden')
    })

    it('returns messages array in the notification category', () => {
      const msgs = [{ type: 'settings_changed' }, { type: 'other' }]
      const result = classifyMessage(input(undefined, { old_seqs: [], messages: msgs }))
      expect(result.kind).toBe('notification')
      if (result.kind === 'notification') {
        expect(result.messages).toStrictEqual(msgs)
      }
    })
  })

  // -- unknown (null parent) ------------------------------------------------

  it('returns unknown when parentObject is undefined and wrapper is null', () => {
    expect(classifyMessage(input()).kind).toBe('unknown')
  })

  // -- unsupported provider (no registered plugin) --------------------------

  it('classifies an UNSPECIFIED provider as unsupported_provider (no Claude guess)', () => {
    // The provider is the proto-0 default; there is no plugin, so refuse to guess
    // Claude and surface the message as unsupported rather than mis-rendering it.
    const result = classifyMessage(input({ type: 'result', duration_ms: 5 }, null, AgentProvider.UNSPECIFIED))
    expect(result.kind).toBe('unsupported_provider')
  })

  it('classifies an unregistered provider value as unsupported_provider', () => {
    // A provider enum the frontend has no plugin for (e.g. backend/frontend
    // version skew) must surface loudly, not fall back to Claude's renderers.
    const result = classifyMessage(input({ type: 'assistant' }, null, 999 as AgentProvider))
    expect(result.kind).toBe('unsupported_provider')
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

  it('classifies a persisted control-response row as control_response for EVERY provider (before plugin dispatch)', () => {
    // The {isSynthetic, controlResponse} row is a LeapMux-neutral synthetic shape classified once in
    // classifyMessage -- not per plugin -- so it resolves identically for every provider and a new
    // plugin can't forget it. Exercise a spread of provider plugins (Claude / Codex / an ACP question
    // provider / Pi / Cursor) through the real dispatch.
    for (const provider of [
      AgentProvider.CLAUDE_CODE,
      AgentProvider.CODEX,
      AgentProvider.OPENCODE,
      AgentProvider.PI,
      AgentProvider.CURSOR,
    ]) {
      const row = { isSynthetic: true, controlResponse: { provider: 'X', requestId: 'r', response: { response: { behavior: 'allow' } } } }
      expect(classifyMessage(input(row, null, provider)).kind).toBe('control_response')
    }
  })

  it('does NOT short-circuit a control-response-shaped row that sits inside a notification thread', () => {
    // The row is persisted standalone, never inside a thread, so the shared classifier guards on
    // !wrapper to preserve each plugin's wrapper-first precedence: a thread whose first message
    // happens to look synthetic still classifies as its plugin's kind, not control_response.
    const row = { isSynthetic: true, controlResponse: { provider: 'X', response: {} } }
    const wrapped = input(row, { old_seqs: [], messages: [row] }, AgentProvider.CLAUDE_CODE)
    expect(classifyMessage(wrapped).kind).not.toBe('control_response')
  })

  it('does not classify non-synthetic with controlResponse', () => {
    const result = classifyMessage(input({ controlResponse: { provider: 'CLAUDE_CODE', response: {} } }))
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

  // -- worker-authored notifications ----------------------------------------

  // The worker writes the subagent-end divider itself, so no agent wire format
  // carries it and no provider plugin can classify it. These pin that it is
  // classified before dispatch, for every provider: a miss here renders the
  // divider as raw JSON in the subagent's own transcript.
  describe('worker-authored notification', () => {
    const divider = { type: 'subagent_ended', status: 'completed' }

    it.each([
      ['Claude', AgentProvider.CLAUDE_CODE],
      ['Codex', AgentProvider.CODEX],
      ['Pi', AgentProvider.PI],
      ['OpenCode', AgentProvider.OPENCODE],
      ['Cursor', AgentProvider.CURSOR],
      ['Goose', AgentProvider.GOOSE],
    ])('classifies subagent_ended as a notification for %s', (_name, provider) => {
      const result = classifyMessage(input(divider, null, provider))
      expect(result.kind).toBe('notification')
      expect(result.kind === 'notification' && result.messages).toEqual([divider])
    })

    it('carries every final status through unchanged', () => {
      for (const status of ['completed', 'failed', 'stopped', 'interrupted']) {
        const parent = { type: 'subagent_ended', status }
        const result = classifyMessage(input(parent))
        expect(result.kind === 'notification' && result.messages).toEqual([parent])
      }
    })

    // The branch is scoped to the types the worker authors, never to the whole
    // NOTIFICATION_TYPE vocabulary: Codex suppresses its own `agent_error` for a
    // turn it already reported, and a blanket membership test placed ahead of
    // plugin.classify would render that hidden row again.
    it('leaves an agent-emitted type to the provider plugin', () => {
      const result = classifyMessage(
        input({ type: 'agent_error', error: 'Codex turn failed' }, null, AgentProvider.CODEX),
      )
      expect(result.kind).toBe('hidden')
    })

    // A wrapper means a consolidated thread, and the plugins resolve those
    // first. Guarding on `!wrapper` keeps that precedence.
    it('yields to a notification thread wrapper', () => {
      const result = classifyMessage(input(divider, wrapper({ type: 'interrupted' })))
      expect(result.kind).toBe('notification')
      expect(result.kind === 'notification' && result.messages).toEqual([{ type: 'interrupted' }])
    })
  })
})

// ---------------------------------------------------------------------------
// messageRowClass
// ---------------------------------------------------------------------------

describe('messageRowClass', () => {
  it('returns messageRowCenter for notification', () => {
    expect(messageRowClass('notification', MessageSource.AGENT)).toBe(chatStyles.messageRowCenter)
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
// isMirroredMessageRow
// ---------------------------------------------------------------------------

describe('isMirroredMessageRow', () => {
  it('is true for exactly the rows messageRowClass sends to messageRowEnd', () => {
    // The toolbar's button order reads this predicate and the row class reads it
    // too; a disagreement would reverse the buttons on a row that is not
    // mirrored, which is how the agent rows' new order once leaked into user rows.
    const cases: Array<[Parameters<typeof messageRowClass>[0], MessageSource]> = [
      ['user_text', MessageSource.USER],
      ['user_content', MessageSource.USER],
      ['user_text', MessageSource.AGENT],
      ['assistant_text', MessageSource.AGENT],
      ['assistant_thinking', MessageSource.AGENT],
      ['tool_use', MessageSource.USER],
      ['tool_result', MessageSource.USER],
      ['hidden', MessageSource.USER],
      ['notification', MessageSource.USER],
      ['plan_execution', MessageSource.USER],
    ]
    for (const [kind, source] of cases) {
      expect(isMirroredMessageRow(kind, source))
        .toBe(messageRowClass(kind, source) === chatStyles.messageRowEnd)
    }
  })

  it('mirrors a user message and never an agent one', () => {
    expect(isMirroredMessageRow('user_text', MessageSource.USER)).toBe(true)
    expect(isMirroredMessageRow('assistant_text', MessageSource.AGENT)).toBe(false)
    expect(isMirroredMessageRow('tool_use', MessageSource.USER)).toBe(false)
    expect(isMirroredMessageRow('notification', MessageSource.USER)).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// messageBubbleClass
// ---------------------------------------------------------------------------

describe('messageBubbleClass', () => {
  it('returns systemMessage for notification', () => {
    expect(messageBubbleClass('notification', MessageSource.AGENT)).toBe(chatStyles.systemMessage)
  })

  it('returns bandMessage for assistant_thinking', () => {
    expect(messageBubbleClass('assistant_thinking', MessageSource.AGENT)).toBe(chatStyles.bandMessage)
  })

  it('returns metaMessage for meta kinds', () => {
    expect(messageBubbleClass('tool_use', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('tool_result', MessageSource.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('hidden', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('result_divider', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('control_response', MessageSource.USER)).toBe(chatStyles.metaMessage)
    expect(messageBubbleClass('compact_summary', MessageSource.AGENT)).toBe(chatStyles.metaMessage)
  })

  it('returns bandMessage for assistant_text with AGENT source', () => {
    expect(messageBubbleClass('assistant_text', MessageSource.AGENT)).toBe(chatStyles.bandMessage)
  })

  it('gives a message and a thought the SAME content class -- the line style lives on the row', () => {
    expect(messageBubbleClass('assistant_text', MessageSource.AGENT))
      .toBe(messageBubbleClass('assistant_thinking', MessageSource.AGENT))
  })

  it('returns bandMessage for a band kind regardless of source', () => {
    // The band is decided by kind alone, exactly as messageRowChromeClass and the
    // virtualizer's gap decide it -- a source-dependent answer here would let the
    // strip and its content disagree.
    expect(messageBubbleClass('assistant_text', MessageSource.USER)).toBe(chatStyles.bandMessage)
    expect(messageBubbleClass('assistant_thinking', MessageSource.LEAPMUX)).toBe(chatStyles.bandMessage)
  })

  it('returns the fallback bubble for an unclassified AGENT shape', () => {
    // `unknown` renders as a bare raw-text span, so it still needs a container.
    expect(messageBubbleClass('unknown', MessageSource.AGENT)).toBe(chatStyles.agentFallbackMessage)
  })

  it('keeps plan_execution on its own bubble, not a band', () => {
    expect(messageBubbleClass('plan_execution', MessageSource.AGENT)).toBe(chatStyles.planExecutionMessage)
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

// ---------------------------------------------------------------------------
// messageRowChromeClass
// ---------------------------------------------------------------------------

describe('messageRowChromeClass', () => {
  it('returns the plain band for an assistant message', () => {
    expect(messageRowChromeClass('assistant_text', MessageSource.AGENT)).toBe(chatStyles.bandRow)
  })

  it('adds the dashed variant for a thought', () => {
    const classes = messageRowChromeClass('assistant_thinking', MessageSource.AGENT).split(' ')
    expect(classes).toContain(chatStyles.bandRow)
    expect(classes).toContain(chatStyles.bandRowThought)
  })

  it('widens a turn-end divider row so its rule can reach both edges', () => {
    expect(messageRowChromeClass('result_divider', MessageSource.AGENT)).toBe(chatStyles.bleedRow)
  })

  it('widens a user message row so its bubble can reach the right edge', () => {
    expect(messageRowChromeClass('user_text', MessageSource.USER)).toBe(chatStyles.bleedRow)
    expect(messageRowChromeClass('user_content', MessageSource.USER)).toBe(chatStyles.bleedRow)
  })

  it('widens exactly the rows that mirror -- never a meta row from the user', () => {
    // The bubble's negative margin and the row's widening must cover the same
    // rows; a tool row sent by the user renders a metaMessage, not a bubble, so
    // widening it would be pointless and could clip nothing into view.
    for (const kind of ['tool_use', 'tool_result', 'hidden', 'notification'] as const) {
      expect(messageRowChromeClass(kind, MessageSource.USER)).toBe('')
      expect(isMirroredMessageRow(kind, MessageSource.USER)).toBe(false)
    }
  })

  it('gives a widened row NO band chrome -- it paints no strip of its own', () => {
    for (const [kind, source] of [['result_divider', MessageSource.AGENT], ['user_text', MessageSource.USER]] as const) {
      const classes = messageRowChromeClass(kind, source).split(' ')
      expect(classes).not.toContain(chatStyles.bandRow)
      expect(classes).not.toContain(chatStyles.bandRowThought)
    }
  })

  it('returns an empty string for every row that stays inside the gutter', () => {
    for (const kind of ['tool_use', 'tool_result', 'agent_prompt', 'user_text', 'user_content', 'plan_execution', 'notification', 'control_response', 'compact_summary', 'hidden', 'unsupported_provider', 'unknown'] as const) {
      expect(messageRowChromeClass(kind, MessageSource.AGENT)).toBe('')
    }
  })

  it('agrees with messageBubbleClass on which kinds are bands', () => {
    // The strip and the content inside it are chosen by two functions; a
    // disagreement would paint gray behind a bubble or leave a band unpainted.
    // result_divider and a user row bleed WITHOUT being bands, so this checks
    // the band class specifically, not "chrome is non-empty".
    for (const kind of ['assistant_text', 'assistant_thinking', 'result_divider', 'tool_use', 'user_text', 'plan_execution', 'notification', 'unknown'] as const) {
      const isBand = messageRowChromeClass(kind, MessageSource.AGENT).split(' ').includes(chatStyles.bandRow)
      expect(messageBubbleClass(kind, MessageSource.AGENT) === chatStyles.bandMessage).toBe(isBand)
    }
  })

  it('widens a row whenever its bubble runs to the right edge', () => {
    // The invariant the layout rests on: a bubble that bleeds needs its row widened,
    // or paint containment clips the bleed away. Swept over the WHOLE domain, not a
    // sample -- a new kind that reaches the edge without bleedRow would otherwise
    // land as a silently clipped bubble that only a screenshot catches.
    let bleedingBubbles = 0
    for (const kind of ALL_MESSAGE_KINDS) {
      for (const source of ALL_MESSAGE_SOURCES) {
        if (!bubbleRunsToRightEdge(kind, source, false))
          continue
        bleedingBubbles++
        expect(messageRowChromeClass(kind, source)).toBe(chatStyles.bleedRow)
      }
    }
    // Guard the loop against becoming a no-op if the bubble mapping ever changes.
    expect(bleedingBubbles).toBeGreaterThan(0)
  })
})

describe('messageRowChrome', () => {
  it('joins the base class with the row chrome and reports the band beside it', () => {
    // The four row-mount sites spread this ONE result. Before it existed they each
    // hand-built the class join and looked the band up again, and the streaming tail
    // hardcoded both -- so a row could be measured without the chrome it renders with.
    const chrome = messageRowChrome('base-class', 'assistant_thinking', MessageSource.AGENT)
    expect(chrome.class.split(' ')).toEqual([
      'base-class',
      chatStyles.bandRow,
      chatStyles.bandRowThought,
    ])
    expect(chrome.band).toBe('thought')
  })

  it('emits no stray separator for a row that paints nothing', () => {
    // messageRowChromeClass returns '' for such a row, and a naive join would leave a
    // trailing space -- which reads as an empty class token in the DOM.
    const chrome = messageRowChrome('base-class', 'tool_use', MessageSource.AGENT)
    expect(chrome.class).toBe('base-class')
    expect(chrome.band).toBeUndefined()
  })

  it('drops the separator the other way too, for a row mounted with no base class', () => {
    // The streaming tail passes '' -- it is in flow, so it has no positioning class of
    // its own. A leading space would be the same empty token.
    const chrome = messageRowChrome('', 'assistant_text', MessageSource.AGENT)
    expect(chrome.class).toBe(chatStyles.bandRow)
    expect(chrome.band).toBe('text')
  })

  it('agrees with messageRowChromeClass over the whole domain', () => {
    for (const kind of ALL_MESSAGE_KINDS) {
      for (const source of ALL_MESSAGE_SOURCES) {
        const expected = [messageRowChromeClass(kind, source)].filter(Boolean).join(' ')
        expect(messageRowChrome('', kind, source).class).toBe(expected)
      }
    }
  })
})

describe('bubbleRunsToRightEdge', () => {
  it('is true for exactly the rows whose bubble is a user bubble', () => {
    // Derived from messageBubbleClass rather than from a condition written again,
    // so the two can never disagree about which bubbles reach the edge.
    for (const kind of ALL_MESSAGE_KINDS) {
      for (const source of ALL_MESSAGE_SOURCES) {
        const isUserBubble = messageBubbleClass(kind, source) === chatStyles.userMessage
        expect(bubbleRunsToRightEdge(kind, source, false)).toBe(isUserBubble)
      }
    }
  })

  it('is false for every row once a delivery error stacks controls under the bubble', () => {
    // Retry and Delete are laid out against the row's CONTENT edge, so a bleeding
    // bubble above them would leave the two right edges a whole gutter apart.
    for (const kind of ALL_MESSAGE_KINDS) {
      for (const source of ALL_MESSAGE_SOURCES)
        expect(bubbleRunsToRightEdge(kind, source, true)).toBe(false)
    }
  })
})
