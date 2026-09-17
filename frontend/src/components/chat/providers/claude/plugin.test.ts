import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerQuotableText, providerRowPreviewText } from '~/test-support/toolCallIr'
import { providerFor } from '../registry'
import { input } from '../testUtils'

// Side-effect import to register the Claude plugin.
import './plugin'

describe('claude permission presets', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('maps smart and bypass permissions to Claude modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: { sets: { permissionMode: 'auto' } },
      bypass: { sets: { permissionMode: 'bypassPermissions' } },
    })
  })
})

describe('claude extractQuotableText', () => {
  // The quote is what the ROW shows. A message that carries a text block and a
  // thinking block draws the text alone, so quoting the thinking beside it would
  // hand the reader words the transcript never displayed.
  it('quotes the text blocks a row draws, not the thinking beside them', () => {
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
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, { category: { kind: 'assistant_text' } })).toBe('Hello')
  })

  it('returns null when assistant message has no quotable content', () => {
    const parent = {
      type: 'assistant',
      message: { content: [{ type: 'tool_use', name: 'Read' }] },
    }
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, { category: { kind: 'assistant_text' } })).toBeNull()
  })

  it('reads message.content string for user_text', () => {
    const parent = { type: 'user', message: { content: '  hello  ' } }
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, { category: { kind: 'user_text' } })).toBe('hello')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, { content: ' hi ' }, { category: { kind: 'user_content' } })).toBe('hi')
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, { content: 'plan' }, { category: { kind: 'plan_execution' } })).toBe('plan')
  })

  it('returns null for non-quotable categories', () => {
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, { type: 'assistant', message: { content: [{ type: 'text', text: 'x' }] } }, { category: { kind: 'hidden' } })).toBeNull()
  })
})

describe('claude preview text (scroll-rail mark preview)', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
  // The rail reads the row through whichever shape it carries, the way
  // `chatMarkPreview` does.
  const preview = (parent: Record<string, unknown>, category: MessageCategory) =>
    providerRowPreviewText(AgentProvider.CLAUDE_CODE, parent, { category })

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
    expect(preview(parent, { kind: 'tool_result' }))
      .toBe('> **A stored** column: needs a migration.\n\nWe should go with this option.')
  })

  it('extracts a tool_result whose content is itself a block array', () => {
    const parent = {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: [{ type: 'text', text: 'answer text' }] }] },
    }
    expect(preview(parent, { kind: 'tool_result' })).toBe('answer text')
  })

  // A message can carry a result for each of several parallel calls, and each of
  // them belongs to a DIFFERENT span. A row is one span, so the preview states the
  // row this dot jumps to -- the first -- rather than a join of two unrelated calls.
  it('previews the first result of several parallel calls', () => {
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
    expect(preview(parent, { kind: 'tool_result' })).toBe('first')
  })

  it('reads the Claude {message:{content}} transcript envelope (a Claude-specific shape, not the shared default)', () => {
    // A transcript user row nests its text under message.content as a string; this Anthropic
    // shape is read here, by the provider that owns it, not by a neutral fallback.
    expect(preview({ message: { content: 'typed text' } }, { kind: 'user_text' })).toBe('typed text')
  })

  it('states the label a turn-end divider carries', () => {
    expect(preview({ type: 'result', subtype: 'success', result: 'Done', duration_ms: 1234, num_turns: 1 }, { kind: 'result_divider' })).not.toBeNull()
  })

  it('reads the LeapMux-neutral {content} user send', () => {
    expect(preview({ content: 'hello world' }, { kind: 'user_content' })).toBe('hello world')
  })

  // The rail used to show a mark-type LABEL here, because the neutral extractor
  // read a top-level string and an assistant row states its words in blocks.
  it('previews an assistant content-block array', () => {
    expect(preview({ message: { content: [{ type: 'text', text: 'hi' }] } }, { kind: 'assistant_text' })).toBe('hi')
  })

  it('derives the control-response display from the native response envelope (not previewText)', () => {
    // Control-response rows resolve through controlResponseDisplay, not previewText -- Claude's
    // derivation IS the neutral behavior envelope: allow -> Approved, deny+message -> feedback.
    const allow = { type: 'control_response', response: { request_id: 'r', response: { behavior: 'allow' } } }
    expect(plugin?.controls?.controlResponseDisplay!({ claimToken: 'claim-1', requestId: 'r', request: undefined, response: allow }))
      .toEqual({ kind: 'label', text: 'Allow' })
    const deny = { type: 'control_response', response: { request_id: 'r', response: { behavior: 'deny', message: 'use ripgrep' } } }
    expect(plugin?.controls?.controlResponseDisplay!({ claimToken: 'claim-1', requestId: 'r', request: undefined, response: deny }))
      .toEqual({ kind: 'feedback', message: 'use ripgrep' })
  })

  /*
   * The envelope above says nothing about WHICH control it answers, and the two Claude
   * offers carry different words: a plan is approved, a permission is allowed. The
   * request decides, through the same reader the banner drew it with -- so a saved row
   * reads the words its own button carried.
   */
  it.each([
    ['ExitPlanMode', 'Approve', 'Reject'],
    ['Bash', 'Allow', 'Deny'],
  ])('reads the %s answer with that control\'s own words', (toolName, allowWord, denyWord) => {
    const request = { request: { tool_name: toolName, input: {} } }
    const answer = (behavior: string) => ({ type: 'control_response', response: { request_id: 'r', response: { behavior } } })
    expect(plugin?.controls?.controlResponseDisplay!({ claimToken: 'c', requestId: 'r', request, response: answer('allow') }))
      .toEqual({ kind: 'label', text: allowWord })
    expect(plugin?.controls?.controlResponseDisplay!({ claimToken: 'c', requestId: 'r', request, response: answer('deny') }))
      .toEqual({ kind: 'label', text: denyWord })
  })
})

describe('claude plugin capabilities', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('exposes attachment capabilities', () => {
    expect(plugin?.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      binary: false,
    })
  })

  it('renders the permissionMode group as the trigger mode segment', () => {
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })
})

describe('claude planMode', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('wires plan mode to the permissionMode group', () => {
    // Claude's plan axis IS its permission mode (permissionMode=plan), so the
    // trigger naturally reads "Plan Mode" while in plan.
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: 'plan',
      defaultValue: 'default',
    })
  })

  it('reads the current permission mode from optionValues, defaulting when unset', () => {
    expect(plugin?.configuration?.planMode!.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(plugin?.configuration?.planMode!.currentMode({})).toBe('default')
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
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([{ type: 'tool_use' }]))).toBe('opener')
  })

  it('classifies a tool_result block as the result', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([{ type: 'tool_result' }]))).toBe('result')
  })

  it('lets the tool_use opener win when a message carries BOTH block types, regardless of order', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([{ type: 'tool_result' }, { type: 'tool_use' }]))).toBe('opener')
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([{ type: 'tool_use' }, { type: 'tool_result' }]))).toBe('opener')
  })

  it('skips non-object blocks and classifies text-only content as other', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([null, 'str', { type: 'text' }]))).toBe('other')
  })

  it('returns other when there is no content array', () => {
    expect(plugin?.transcript.spanRole!({ rawText: '', topLevel: null, parentObject: undefined, wrapper: null })).toBe('other')
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
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
    }))).toEqual([{ key: 'five_hour', info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 } }])
  })

  it('defaults the key to unknown when rateLimitType is missing', () => {
    const result = plugin?.session?.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { status: 'exceeded' },
    }))
    expect(result?.[0]?.key).toBe('unknown')
  })

  it('returns empty array when rate_limit_info is missing', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({ type: 'rate_limit_event' }))).toEqual([])
  })

  it('returns null for a non-rate_limit_event', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({ type: 'settings_changed' }))).toBeNull()
  })
})

describe('claude contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('reads the Claude message.usage input_tokens + cache_* shape', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({ message: { usage: { input_tokens: 1000, cache_creation_input_tokens: 200, cache_read_input_tokens: 300 } } })))
      .toEqual({ inputTokens: 1000, cacheCreationInputTokens: 200, cacheReadInputTokens: 300 })
  })

  it('returns null for a non-Claude usage shape (Pi input, no input_tokens)', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({ message: { usage: { input: 100 } } }))).toBeNull()
  })

  it('returns null when the message carries no message.usage', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({ type: 'assistant', message: {} }))).toBeNull()
  })
})

// The typed resolver supplies a row's related half by span identity, and the plugin
// says which half a row needs. A RESULT always wants its request, because Claude's
// tool name lives on the `tool_use` row. A REQUEST wants its result only when the
// body comes from there: a subagent, a task, a to-do write.
describe('claude relatedMessages', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
  const toolUse = (name: string) => ({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'toolu_1', name, input: {} }] } })

  it('a result wants its request, because the name lives there', () => {
    expect(plugin?.transcript.relatedMessages!(input({ type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'ok' }] } }))).toEqual(['request'])
  })

  it('a request wants its result only when the body comes from there', () => {
    for (const name of ['Agent', 'Task', 'TodoWrite', 'TaskCreate', 'TaskUpdate', 'TaskGet'])
      expect(plugin?.transcript.relatedMessages!(input(toolUse(name)))).toEqual(['result'])
    expect(plugin?.transcript.relatedMessages!(input(toolUse('Read')))).toEqual([])
  })
})
