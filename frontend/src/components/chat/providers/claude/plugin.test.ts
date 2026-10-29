import type { MessageCategory } from '../../messageClassifier'
import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerQuotableText, providerRowPreviewText } from '~/test-support/toolCallFixture'
import { extractChatRow } from '../../rowExtraction'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import { classifyClaudeCodeMessage } from './classification'

// Side-effect import to register the Claude plugin.
import './plugin'

describe('claude permission presets', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('states that the goal status is unconfirmed', () => {
    expect(plugin.session?.goalStatusUnconfirmed).toBe(true)
  })

  it('maps smart and bypass permissions to Claude modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: {
        sets: { permissionMode: 'auto' },
      },
      bypass: {
        sets: { permissionMode: 'bypassPermissions' },
      },
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
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, {
      category: { kind: 'assistant_text' },
    })).toBe('Hello')
  })

  it('returns null when assistant message has no quotable content', () => {
    const parent = {
      type: 'assistant',
      message: {
        content: [
          { type: 'tool_use', name: 'Read' },
        ],
      },
    }
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, {
      category: { kind: 'assistant_text' },
    })).toBeNull()
  })

  it('reads a stored user string through the unknown-row fallback', () => {
    const parent = {
      type: 'user',
      message: { content: '  hello  ' },
    }
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, parent, {
      category: { kind: 'unknown' },
    })).toBe('hello')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, { content: ' hi ' }, {
      category: { kind: 'user_content' },
    })).toBe('hi')
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, { content: 'plan' }, {
      category: { kind: 'plan_execution' },
    })).toBe('plan')
  })

  it('returns null for non-quotable categories', () => {
    expect(providerQuotableText(AgentProvider.CLAUDE_CODE, {
      type: 'assistant',
      message: {
        content: [
          { type: 'text', text: 'x' },
        ],
      },
    }, {
      category: { kind: 'hidden' },
    })).toBeNull()
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
        content: [
          {
            type: 'tool_result',
            content: '> **A stored** column: needs a migration.\n\nWe should go with this option.',
            is_error: true,
            tool_use_id: 'toolu_1',
          },
        ],
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
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            content: [
              { type: 'text', text: 'answer text' },
            ],
          },
        ],
      },
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

  it('reads a stored native user string through the unknown-row fallback', () => {
    expect(preview({
      type: 'user',
      message: { content: 'typed text' },
    }, { kind: 'unknown' })).toBe('typed text')
  })

  it('states the label a turn-end divider carries', () => {
    expect(preview({
      type: 'result',
      subtype: 'success',
      result: 'Done',
      duration_ms: 1234,
      num_turns: 1,
    }, { kind: 'result_divider' })).not.toBeNull()
  })

  it('reads the LeapMux-neutral {content} user send', () => {
    expect(preview({ content: 'hello world' }, { kind: 'user_content' })).toBe('hello world')
  })

  // The rail used to show a mark-type LABEL here, because the neutral extractor
  // read a top-level string and an assistant row states its words in blocks.
  it('previews an assistant content-block array', () => {
    expect(preview({
      message: {
        content: [
          { type: 'text', text: 'hi' },
        ],
      },
    }, { kind: 'assistant_text' })).toBe('hi')
  })

  it('derives the control-response display from the native response envelope (not previewText)', () => {
    // Control-response rows resolve through controlResponseDisplay, not previewText -- Claude's
    // derivation IS the neutral behavior envelope: allow -> Approved, deny+message -> feedback.
    const allow = {
      type: 'control_response',
      response: {
        request_id: 'r',
        response: { behavior: 'allow' },
      },
    }
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: 'r',
      request: undefined,
      response: allow,
    }))
      .toEqual({ kind: 'label', text: 'Allow' })
    const deny = {
      type: 'control_response',
      response: {
        request_id: 'r',
        response: { behavior: 'deny', message: 'use ripgrep' },
      },
    }
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: 'r',
      request: undefined,
      response: deny,
    }))
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
    const request = {
      request: { tool_name: toolName, input: {} },
    }
    const answer = (behavior: string) => ({
      type: 'control_response',
      response: {
        request_id: 'r',
        response: { behavior },
      },
    })
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'c',
      requestId: 'r',
      request,
      response: answer('allow'),
    }))
      .toEqual({ kind: 'label', text: allowWord })
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'c',
      requestId: 'r',
      request,
      response: answer('deny'),
    }))
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
      audio: false,
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
    expect(plugin?.configuration?.planMode!.currentMode({
      optionValues: { permissionMode: 'plan' },
    })).toBe('plan')
    expect(plugin?.configuration?.planMode!.currentMode({})).toBe('default')
  })
})

describe('claude spanRole', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  // spanRole only reads `parsed.parentObject`; build a minimal parsed shape whose
  // `message.content` holds the Anthropic-style content blocks getMessageContent reads.
  function parsedWithBlocks(blocks: unknown[]): ResolvedMessageContent {
    return resolveMessageForRendering({
      rawText: '',
      topLevel: null,
      parentObject: {
        message: { content: blocks },
      },
      wrapper: null,
    }, AgentProvider.CLAUDE_CODE)
  }

  it('classifies a tool_use block as the request', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([
      { type: 'tool_use' },
    ]))).toBe('request')
  })

  it('classifies a tool_result block as the result', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([
      { type: 'tool_result' },
    ]))).toBe('result')
  })

  it('lets the tool_use request win when a message carries BOTH block types, regardless of order', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([
      { type: 'tool_result' },
      { type: 'tool_use' },
    ]))).toBe('request')
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([
      { type: 'tool_use' },
      { type: 'tool_result' },
    ]))).toBe('request')
  })

  it('skips non-object blocks and classifies text-only content as other', () => {
    expect(plugin?.transcript.spanRole!(parsedWithBlocks([
      null,
      'str',
      { type: 'text' },
    ]))).toBe('other')
  })

  it('returns other when there is no content array', () => {
    expect(plugin?.transcript.spanRole!(resolveMessageForRendering({
      rawText: '',
      topLevel: null,
      parentObject: undefined,
      wrapper: null,
    }, AgentProvider.CLAUDE_CODE))).toBe('other')
  })
})

// Build a plain ParsedMessageContent whose inner message is `inner`;
// rateLimitsFromMessage reads it through getInnerMessage (parentObject ?? topLevel).
function parsed(inner: Record<string, unknown>): ParsedMessageContent {
  return {
    rawText: '',
    topLevel: inner,
    parentObject: inner,
    wrapper: null,
  }
}

describe('claude rateLimitsFromMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('extracts from a raw rate_limit_event', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
    }))).toEqual({
      mode: 'merge',
      values: {
        five_hour: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
      },
    })
  })

  it('defaults the key to unknown when rateLimitType is missing', () => {
    const result = plugin?.session?.rateLimitsFromMessage!(parsed({
      type: 'rate_limit_event',
      rate_limit_info: { status: 'exceeded' },
    }))
    expect(result?.values.unknown).toBeDefined()
  })

  it('returns empty array when rate_limit_info is missing', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({ type: 'rate_limit_event' }))).toEqual({ mode: 'merge', values: {} })
  })

  it('returns null for a non-rate_limit_event', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({ type: 'settings_changed' }))).toBeNull()
  })
})

describe('claude contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.CLAUDE_CODE)!

  it('reads the Claude message.usage input_tokens + cache_* shape', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({
      message: {
        usage: { input_tokens: 1000, cache_creation_input_tokens: 200, cache_read_input_tokens: 300 },
      },
    })))
      .toEqual({ inputTokens: 1000, cacheCreationInputTokens: 200, cacheReadInputTokens: 300 })
  })

  it('returns null for a non-Claude usage shape (Pi input, no input_tokens)', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({
      message: {
        usage: { input: 100 },
      },
    }))).toBeNull()
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
  const toolUse = (name: string) => ({
    type: 'assistant',
    message: {
      content: [
        {
          type: 'tool_use',
          id: 'toolu_1',
          name,
          input: {},
        },
      ],
    },
  })

  it('a result wants its request, because the name lives there', () => {
    expect(plugin?.transcript.relatedMessages!(input({
      type: 'user',
      message: {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: 'toolu_1', content: 'ok' },
        ],
      },
    }))).toEqual(['request'])
  })

  it('a request wants its result only when the body comes from there', () => {
    for (const name of [
      'Agent',
      'Task',
      'TodoWrite',
      'TaskCreate',
      'TaskUpdate',
      'TaskGet',
    ])
      expect(plugin?.transcript.relatedMessages!(input(toolUse(name)))).toEqual(['result'])
    expect(plugin?.transcript.relatedMessages!(input(toolUse('Read')))).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// The Claude Code 2.1.300 frames that reach LeapMux.
// ---------------------------------------------------------------------------

/**
 * One frame per subtype that Claude Code 2.1.300 sends on LeapMux's stream, as a
 * stored row carries it. A row of any of them draws a row or a hidden row, never
 * the no-display card (`unsupported()`). `tool_progress` and the control frames
 * are absent: the Worker stores no message row for them. A
 * `task_started`/`task_progress` row carries the spawn span as its parent,
 * which is the shape that its hide rule reads.
 */
const CLAUDE_FRAME_CORPUS: { name: string, frame: Record<string, unknown>, parentSpanId?: string, unsupported?: boolean }[] = [
  // The system notices: each one draws its own words, hides, or feeds the Worker's state.
  {
    name: 'init',
    frame: {
      type: 'system',
      subtype: 'init',
      slash_commands: ['goal'],
      session_id: 's-1',
    },
  },
  {
    name: 'commands_changed',
    frame: {
      type: 'system',
      subtype: 'commands_changed',
      commands: [
        { name: 'review', description: 'Review the diff', argumentHint: '' },
      ],
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'elicitation_complete',
    frame: {
      type: 'system',
      subtype: 'elicitation_complete',
      mcp_server_name: 'github',
      elicitation_id: 'e1',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'permission_check_status',
    frame: {
      type: 'system',
      subtype: 'permission_check_status',
      tool_use_id: 'toolu_1',
      status: 'checking',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'permission_denied',
    frame: {
      type: 'system',
      subtype: 'permission_denied',
      tool_name: 'Bash',
      tool_use_id: 'toolu_1',
      decision_reason_type: 'classifier',
      decision_reason: 'the command removes a directory',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'model_fallback',
    frame: {
      type: 'system',
      subtype: 'model_fallback',
      trigger: 'overloaded',
      original_model: 'claude-opus-5-5',
      fallback_model: 'claude-sonnet-5-5',
      content: 'Switched to claude-sonnet-5-5 due to high demand for claude-opus-5-5',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'model_refusal_fallback',
    frame: {
      type: 'system',
      subtype: 'model_refusal_fallback',
      trigger: 'refusal',
      direction: 'retry',
      scope: 'session',
      original_model: 'claude-opus-5-5',
      fallback_model: 'claude-sonnet-5-5',
      request_id: null,
      content: 'Switched to claude-sonnet-5-5',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'model_consent_fallback',
    frame: {
      type: 'system',
      subtype: 'model_consent_fallback',
      choice: 'switch_default',
      original_model: 'claude-opus-5-5',
      fallback_model: 'claude-sonnet-5-5',
      content: 'Switched to claude-sonnet-5-5',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'model_refusal_no_fallback',
    frame: {
      type: 'system',
      subtype: 'model_refusal_no_fallback',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'notification',
    frame: {
      type: 'system',
      subtype: 'notification',
      key: 'compact-error',
      text: 'Error compacting conversation',
      priority: 'high',
      color: 'error',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'informational warning',
    frame: {
      type: 'system',
      subtype: 'informational',
      content: 'Stop hook failed: boom',
      level: 'warning',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'informational info',
    frame: {
      type: 'system',
      subtype: 'informational',
      content: 'verbose only',
      level: 'info',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'memory_recall',
    frame: {
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: [
        { path: '/m/a.md', scope: 'personal' },
      ],
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'hook_response error',
    frame: {
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      output: '',
      stdout: '',
      stderr: 'boom',
      exit_code: 2,
      outcome: 'error',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'hook_response success',
    frame: {
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      output: '',
      stdout: '',
      stderr: '',
      exit_code: 0,
      outcome: 'success',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'hook_started',
    frame: {
      type: 'system',
      subtype: 'hook_started',
      hook_id: 'h1',
      hook_event: 'SessionStart',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'hook_progress',
    frame: {
      type: 'system',
      subtype: 'hook_progress',
      hook_id: 'h1',
      hook_event: 'SessionStart',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_log',
    frame: {
      type: 'system',
      subtype: 'ui_log',
      plugin: 'my-plugin',
      text: 'hello',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_toast',
    frame: {
      type: 'system',
      subtype: 'ui_toast',
      plugin: 'my-plugin',
      text: 'hello',
      timeout_ms: 3000,
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_status',
    frame: {
      type: 'system',
      subtype: 'ui_status',
      plugin: 'my-plugin',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_invalidate',
    frame: {
      type: 'system',
      subtype: 'ui_invalidate',
      plugin: 'my-plugin',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_panes',
    frame: {
      type: 'system',
      subtype: 'ui_panes',
      plugin: 'my-plugin',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_scroll',
    frame: {
      type: 'system',
      subtype: 'ui_scroll',
      plugin: 'my-plugin',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'ui_focus',
    frame: {
      type: 'system',
      subtype: 'ui_focus',
      plugin: 'my-plugin',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'vcs_state_changed',
    frame: {
      type: 'system',
      subtype: 'vcs_state_changed',
      kind: 'push',
      branch: 'main',
      cwd: '/w',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'code_change_published',
    frame: {
      type: 'system',
      subtype: 'code_change_published',
      kind: 'push',
      branch: 'main',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'dev_intent',
    frame: {
      type: 'system',
      subtype: 'dev_intent',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'session_title_changed',
    frame: {
      type: 'system',
      subtype: 'session_title_changed',
      title: 'Fix the tests',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'per_turn_effort_changed',
    frame: {
      type: 'system',
      subtype: 'per_turn_effort_changed',
      effortLevel: 'high',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'feedback_draft_queued',
    frame: {
      type: 'system',
      subtype: 'feedback_draft_queued',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'peer_message_hold',
    frame: {
      type: 'system',
      subtype: 'peer_message_hold',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'post_turn_summary',
    frame: {
      type: 'system',
      subtype: 'post_turn_summary',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },

  // The conversation, compaction, retry and background-task frames.
  {
    name: 'status compacting',
    unsupported: true,
    frame: {
      type: 'system',
      subtype: 'status',
      status: 'compacting',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'compact_boundary',
    frame: {
      type: 'system',
      subtype: 'compact_boundary',
      compact_metadata: { trigger: 'auto', tokensBefore: 180000, tokensAfter: 12000 },
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'microcompact_boundary',
    frame: {
      type: 'system',
      subtype: 'microcompact_boundary',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'api_retry',
    frame: {
      type: 'system',
      subtype: 'api_retry',
      attempt: 2,
      max_retries: 5,
      retry_delay_ms: 4000,
      error_status: 529,
      error: 'overloaded',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'thinking_tokens',
    frame: {
      type: 'system',
      subtype: 'thinking_tokens',
      estimated_tokens: 230,
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'session_state_changed',
    frame: {
      type: 'system',
      subtype: 'session_state_changed',
      state: 'running',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'task_started',
    frame: {
      type: 'system',
      subtype: 'task_started',
      task_id: 't-1',
      tool_use_id: 'tu-spawn',
      task_type: 'local_agent',
      description: 'd',
      prompt: 'p',
      uuid: 'u-1',
      session_id: 's-1',
    },
    parentSpanId: 'tu-spawn',
  },
  {
    name: 'task_progress',
    frame: {
      type: 'system',
      subtype: 'task_progress',
      task_id: 't-1',
      tool_use_id: 'tu-spawn',
      description: 'd',
      uuid: 'u-1',
      session_id: 's-1',
    },
    parentSpanId: 'tu-spawn',
  },
  {
    name: 'task_notification',
    frame: {
      type: 'system',
      subtype: 'task_notification',
      task_id: 't-1',
      tool_use_id: 'tu-spawn',
      status: 'completed',
      summary: 's',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'task_updated',
    frame: {
      type: 'system',
      subtype: 'task_updated',
      task_id: 't-1',
      patch: { status: 'completed' },
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'background_tasks_changed',
    frame: {
      type: 'system',
      subtype: 'background_tasks_changed',
      tasks: [],
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'rate_limit_event',
    frame: {
      type: 'rate_limit_event',
      rate_limit_info: { status: 'rejected', resets_at: 1792000060 },
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'assistant',
    frame: {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: 'hi' },
        ],
      },
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'user',
    frame: {
      type: 'user',
      message: { role: 'user', content: 'hi' },
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
  {
    name: 'result',
    frame: {
      type: 'result',
      subtype: 'success',
      is_error: false,
      duration_ms: 1234,
      num_turns: 1,
      result: 'Done',
      uuid: 'u-1',
      session_id: 's-1',
    },
  },
]

describe('claude 2.1.300 frame corpus', () => {
  it.each(CLAUDE_FRAME_CORPUS)('classifies the native frame $name', ({ frame, parentSpanId, unsupported }) => {
    const classified = parentSpanId === undefined
      ? input(frame)
      : {
          ...input(frame),
          parentSpanId,
        }
    const category = classifyClaudeCodeMessage(classified)
    const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, classified, category)
    expect(extraction.kind === 'unsupported').toBe(unsupported === true)
  })

  // A known frame that states no words draws nothing, never the no-display
  // card. An unknown subtype keeps that card, because it is the signal that a
  // new frame exists.
  it.each([
    {
      name: 'notification with an error color and no text',
      frame: {
        type: 'system',
        subtype: 'notification',
        key: 'compact-error',
        text: '',
        priority: 'high',
        color: 'error',
        uuid: 'u-1',
        session_id: 's-1',
      },
    },
    {
      name: 'memory_recall with no memory',
      frame: {
        type: 'system',
        subtype: 'memory_recall',
        mode: 'select',
        memories: [],
        uuid: 'u-1',
        session_id: 's-1',
      },
    },
  ])('extracts a hidden row for $name', ({ frame }) => {
    const classified = input(frame)
    const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, classified, classifyClaudeCodeMessage(classified))
    expect(extraction).toEqual(extractChatRow(AgentProvider.CLAUDE_CODE, classified, { kind: 'hidden' }))
  })

  it('leaves a stored compacting status unknown', () => {
    const classified = input({ type: 'system', subtype: 'status', status: 'compacting' })
    expect(classifyClaudeCodeMessage(classified)).toEqual({ kind: 'unknown' })
  })

  it('extracts the no-display card for an unknown subtype', () => {
    const classified = input({
      type: 'system',
      subtype: 'some_future_event',
      uuid: 'u-1',
      session_id: 's-1',
    })
    const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, classified, classifyClaudeCodeMessage(classified))
    expect(extraction.kind).toBe('unsupported')
  })
})
