import type { ParsedMessageContent } from './messageParser'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import { describe, expect, it } from 'vitest'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import {
  extractCompactionContextTokens,
  extractContextUsage,
  extractPlanFilePath,
  extractPlanUpdated,
  extractResultMetadata,
  extractSettingsChanges,
  getInnerMessage,
  getInnerMessageType,
  messageUsage,
  NOTIFICATION_THREAD_TYPE,
  parseMessageContent,
} from './messageParser'

/** Build a mock AgentChatMessage with the given JSON content (uncompressed). */
function makeMsg(source: MessageSource, content: unknown, opts?: { seq?: bigint, spanId?: string, spanType?: string }) {
  return makeMessage({
    source,
    content: rawContent(content),
    seq: opts?.seq,
    spanId: opts?.spanId,
    spanType: opts?.spanType,
  })
}

/** Wrap inner messages in a notification-thread wrapper envelope. */
function wrap(...messages: unknown[]): { type: typeof NOTIFICATION_THREAD_TYPE, old_seqs: number[], messages: unknown[] } {
  return { type: NOTIFICATION_THREAD_TYPE, old_seqs: [], messages }
}

// ---------------------------------------------------------------------------
// parseMessageContent
// ---------------------------------------------------------------------------

describe('parseMessageContent', () => {
  it('parses LEAPMUX notification wrapper content', () => {
    const inner = { type: 'settings_changed', changes: {} }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(inner))
    const result = parseMessageContent(msg)

    expect(result.wrapper).not.toBeNull()
    expect(result.parentObject).toEqual(inner)
    expect(result.rawText).toBeTruthy()
  })

  it('parses AGENT-source notification wrapper content (e.g. api_retry)', () => {
    const inner = { type: 'system', subtype: 'api_retry', attempt: 2, max_retries: 10 }
    const msg = makeMsg(MessageSource.AGENT, wrap(inner))
    const result = parseMessageContent(msg)

    expect(result.wrapper).not.toBeNull()
    expect(result.wrapper!.messages).toEqual([inner])
    expect(result.parentObject).toEqual(inner)
  })

  it('handles empty LEAPMUX wrapper messages array', () => {
    const msg = makeMsg(MessageSource.LEAPMUX, wrap())
    const result = parseMessageContent(msg)

    expect(result.wrapper).not.toBeNull()
    expect(result.parentObject).toBeUndefined()
  })

  it('parses raw content for non-LEAPMUX messages', () => {
    const content = { type: 'assistant', message: { content: [] } }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = parseMessageContent(msg)

    expect(result.wrapper).toBeNull()
    expect(result.parentObject).toEqual(content)
    expect(result.topLevel).toEqual(content)
  })

  it('parses unwrapped LEAPMUX content (e.g. agent_session_info)', () => {
    const content = { type: 'agent_session_info', info: {} }
    const msg = makeMsg(MessageSource.LEAPMUX, content)
    const result = parseMessageContent(msg)

    expect(result.wrapper).toBeNull()
    expect(result.parentObject).toEqual(content)
    expect(result.topLevel).toEqual(content)
  })

  it('returns safe defaults for invalid JSON', () => {
    const msg = makeMessage({ content: new TextEncoder().encode('not json') })
    const result = parseMessageContent(msg)

    expect(result.rawText).toBe('not json')
    expect(result.topLevel).toBeNull()
    expect(result.parentObject).toBeUndefined()
  })

  it('returns safe defaults for empty content', () => {
    const msg = makeMessage({ content: new Uint8Array() })
    const result = parseMessageContent(msg)

    // Empty Uint8Array decodes to "" which fails JSON.parse → topLevel null
    expect(result.rawText).toBe('')
    expect(result.topLevel).toBeNull()
    expect(result.parentObject).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// getInnerMessage / getInnerMessageType
// ---------------------------------------------------------------------------

describe('getInnerMessage', () => {
  it('returns parentObject for LEAPMUX notification wrapper', () => {
    const inner = { type: 'settings_changed', changes: {} }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(inner))
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(inner)
  })

  it('returns topLevel for raw content', () => {
    const content = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageSource.AGENT, content)
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(content)
  })
})

describe('getInnerMessageType', () => {
  it('returns type from raw content', () => {
    const msg = makeMsg(MessageSource.AGENT, { type: 'assistant' })
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('assistant')
  })

  it('returns type from LEAPMUX content', () => {
    const msg = makeMsg(MessageSource.LEAPMUX, { type: 'rate_limit' })
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('rate_limit')
  })

  it('returns undefined when no type', () => {
    const msg = makeMsg(MessageSource.AGENT, { message: {} })
    expect(getInnerMessageType(parseMessageContent(msg))).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// messageUsage — the neutral `.message.usage` accessor both Claude and Pi read
// ---------------------------------------------------------------------------

describe('messageUsage', () => {
  const usageOf = (content: unknown) => messageUsage(parseMessageContent(makeMsg(MessageSource.AGENT, content)))

  it('returns the raw usage bag when message.usage is present', () => {
    expect(usageOf({ type: 'assistant', message: { usage: { input_tokens: 42 } } })).toEqual({ input_tokens: 42 })
  })

  it('returns undefined when the message carries no usage', () => {
    expect(usageOf({ type: 'assistant', message: {} })).toBeUndefined()
  })

  it('returns undefined when there is no message field at all', () => {
    expect(usageOf({ type: 'assistant' })).toBeUndefined()
  })

  it('returns undefined when message is not an object (the isObject guard)', () => {
    // A non-object `message` (e.g. a string) must not blow up the `.usage` read.
    expect(usageOf({ type: 'assistant', message: 'not-an-object' })).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// extractContextUsage
// ---------------------------------------------------------------------------

describe('extractContextUsage', () => {
  // A stub provider hook standing in for contextUsageFromMessage; the provider-shape parsing itself
  // (Codex tokenUsage, Claude input_tokens, Pi input/cacheWrite) is tested in each plugin's test.
  const stubFallback = (parsed: ParsedMessageContent): ContextUsageInfo | null => {
    const usage = messageUsage(parsed)
    if (!usage || typeof usage.input_tokens !== 'number')
      return null
    return { inputTokens: usage.input_tokens, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 }
  }
  // The neutral cost / normalized-context_usage path with no provider usage shape. The callback is
  // required, so a caller with no provider fallback passes this explicitly.
  const noProviderUsage = (): ContextUsageInfo | null => null

  it('extracts cost and delegates the raw message.usage to the provider fallback', () => {
    const content = {
      type: 'assistant',
      total_cost_usd: 0.05,
      message: { usage: { input_tokens: 1000 } },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractContextUsage(parseMessageContent(msg), stubFallback)

    expect(result).toEqual({
      totalCostUsd: 0.05,
      contextUsage: {
        inputTokens: 1000,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 0,
      },
    })
  })

  it('extracts only cost when no token info', () => {
    const content = {
      type: 'assistant',
      total_cost_usd: 0.01,
      message: { usage: {} },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractContextUsage(parseMessageContent(msg), noProviderUsage)
    expect(result).toEqual({ totalCostUsd: 0.01 })
  })

  it('returns null when no usage field', () => {
    const content = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageSource.AGENT, content)
    expect(extractContextUsage(parseMessageContent(msg), noProviderUsage)).toBeNull()
  })

  it('extracts normalized Pi usage and cumulative cost from augmented message_end', () => {
    const content = {
      type: 'message_end',
      total_cost_usd: 0.12,
      context_usage: {
        input_tokens: 100,
        cache_creation_input_tokens: 5,
        cache_read_input_tokens: 20,
        output_tokens: 10,
        context_window: 200000,
      },
      message: {
        role: 'assistant',
        usage: {
          input: 100,
          output: 10,
          cacheRead: 20,
          cacheWrite: 5,
          totalTokens: 130,
        },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractContextUsage(parseMessageContent(msg), noProviderUsage)

    expect(result).toEqual({
      totalCostUsd: 0.12,
      contextUsage: {
        inputTokens: 100,
        cacheCreationInputTokens: 5,
        cacheReadInputTokens: 20,
        outputTokens: 10,
        contextWindow: 200000,
      },
    })
  })

  it('skips the provider fallback when a backend-normalized context_usage is present', () => {
    // The raw message.usage fallback runs ONLY when no normalized context_usage was folded in;
    // a message carrying both must use the normalized value and never invoke the fallback.
    const content = {
      type: 'message_end',
      context_usage: { input_tokens: 100, cache_read_input_tokens: 20 },
      message: { role: 'assistant', usage: { input: 999 } },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractContextUsage(parseMessageContent(msg), () => {
      throw new Error('fallback must not be called when normalized context_usage exists')
    })

    expect(result?.contextUsage).toEqual({
      inputTokens: 100,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 20,
    })
  })

  it('returns null for subagent messages with parent_tool_use_id', () => {
    const content = {
      type: 'assistant',
      parent_tool_use_id: 'toolu_abc123',
      total_cost_usd: 0.03,
      message: {
        usage: {
          input_tokens: 500,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 100,
        },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    expect(extractContextUsage(parseMessageContent(msg), noProviderUsage)).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// extractCompactionContextTokens
// ---------------------------------------------------------------------------

describe('extractCompactionContextTokens', () => {
  /** Wrap compaction metadata in the Claude `compact_boundary` system shape. */
  function boundary(compactMetadata: Record<string, unknown>) {
    return { type: 'system', subtype: 'compact_boundary', compact_metadata: compactMetadata }
  }
  const parse = (content: unknown) => parseMessageContent(makeMsg(MessageSource.AGENT, content))
  const extractPost = (parsed: ReturnType<typeof parse>) => extractCompactionContextTokens(parsed)

  it('returns post_tokens when the boundary carries it directly', () => {
    expect(extractPost(parse(boundary({ trigger: 'manual', pre_tokens: 105424, post_tokens: 8476 })))).toBe(8476)
  })

  it('derives post from pre_tokens minus tokens_saved when post_tokens is absent', () => {
    expect(extractPost(parse(boundary({ trigger: 'auto', pre_tokens: 100000, tokens_saved: 40000 })))).toBe(60000)
  })

  it('prefers explicit post_tokens over deriving from tokens_saved', () => {
    expect(extractPost(parse(boundary({ pre_tokens: 100000, post_tokens: 8000, tokens_saved: 1 })))).toBe(8000)
  })

  it('reads camelCase keys (compactMetadata / postTokens)', () => {
    const content = { type: 'system', subtype: 'compact_boundary', compactMetadata: { preTokens: 100000, postTokens: 8000 } }
    expect(extractPost(parse(content))).toBe(8000)
  })

  it('derives from camelCase preTokens minus tokensSaved', () => {
    const content = { type: 'system', subtype: 'compact_boundary', compactMetadata: { preTokens: 100000, tokensSaved: 25000 } }
    expect(extractPost(parse(content))).toBe(75000)
  })

  it('returns undefined when only pre_tokens is present (nothing to resolve post from)', () => {
    expect(extractPost(parse(boundary({ trigger: 'auto', pre_tokens: 100000 })))).toBeUndefined()
  })

  it('returns undefined when tokens_saved has no pre_tokens to anchor it', () => {
    expect(extractPost(parse(boundary({ tokens_saved: 5000 })))).toBeUndefined()
  })

  it('returns undefined when the boundary carries no metadata object', () => {
    expect(extractPost(parse({ type: 'system', subtype: 'compact_boundary' }))).toBeUndefined()
  })

  it('returns 0 for an explicit post_tokens of 0 (fully cleared context)', () => {
    expect(extractPost(parse(boundary({ pre_tokens: 100000, post_tokens: 0 })))).toBe(0)
  })

  it('clamps a derived negative post to 0 when tokens_saved exceeds pre_tokens', () => {
    expect(extractPost(parse(boundary({ pre_tokens: 30000, tokens_saved: 50000 })))).toBe(0)
  })

  it('clamps an explicit negative post_tokens to 0', () => {
    expect(extractPost(parse(boundary({ pre_tokens: 100000, post_tokens: -5 })))).toBe(0)
  })

  it('returns undefined for Codex thread/compacted, which carries no metadata', () => {
    expect(extractPost(parse({ method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }))).toBeUndefined()
  })

  it('returns undefined for a microcompact boundary (Claude emits no metadata)', () => {
    const content = { type: 'system', subtype: 'microcompact_boundary', microcompactMetadata: { preTokens: 200000, tokensSaved: 50000 } }
    expect(extractPost(parse(content))).toBeUndefined()
  })

  it('returns undefined for non-boundary messages', () => {
    expect(extractPost(parse({ type: 'assistant', message: { usage: { input_tokens: 10 } } }))).toBeUndefined()
  })

  it('finds a compact_boundary consolidated after another notification in a thread', () => {
    const msg = makeMsg(MessageSource.AGENT, wrap(
      { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } },
      boundary({ pre_tokens: 100000, post_tokens: 8000 }),
    ))
    expect(extractPost(parseMessageContent(msg))).toBe(8000)
  })

  it('uses the most recent boundary when a thread carries more than one', () => {
    const msg = makeMsg(MessageSource.AGENT, wrap(
      boundary({ pre_tokens: 100000, post_tokens: 8000 }),
      boundary({ pre_tokens: 50000, post_tokens: 3000 }),
    ))
    expect(extractPost(parseMessageContent(msg))).toBe(3000)
  })

  it('skips a most-recent boundary with no resolvable post and uses the earlier one that has it', () => {
    // The reverse scan must not bail out on the first boundary it meets when that
    // boundary carries no post (here only pre_tokens); it falls through to the
    // earlier boundary whose post_tokens can still refresh the grid.
    const msg = makeMsg(MessageSource.AGENT, wrap(
      boundary({ pre_tokens: 50000, post_tokens: 3000 }),
      boundary({ trigger: 'auto', pre_tokens: 100000 }),
    ))
    expect(extractPost(parseMessageContent(msg))).toBe(3000)
  })
})

// ---------------------------------------------------------------------------
// extractResultMetadata
// ---------------------------------------------------------------------------

describe('extractResultMetadata', () => {
  // The neutral path with no provider subtype derivation. The subtypeFallback is required, so a
  // caller with no provider fallback passes this explicitly.
  const noSubtype = (): string | undefined => undefined

  it('extracts subtype, contextWindow, and cost', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      total_cost_usd: 0.10,
      modelUsage: {
        'claude-sonnet': { contextWindow: 200000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 200000,
      totalCostUsd: 0.10,
    })
  })

  it('extracts normalized context usage from augmented Pi agent_end', () => {
    const content = {
      type: 'agent_end',
      total_cost_usd: 0.42,
      context_usage: {
        input_tokens: 0,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
        output_tokens: 0,
        context_tokens: 60000,
        context_window: 200000,
      },
      messages: [],
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)

    expect(result).toEqual({
      contextUsage: {
        inputTokens: 0,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 0,
        outputTokens: 0,
        contextTokens: 60000,
        contextWindow: 200000,
      },
      totalCostUsd: 0.42,
    })
  })

  it('selects primary model contextWindow when modelUsage includes multiple models', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      modelUsage: {
        'claude-haiku-4-5-20251001': { contextWindow: 200000 },
        'claude-opus-4-6[1m]': { contextWindow: 1000000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), 'opus[1m]', noSubtype)

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 1000000,
    })
  })

  it('matches bracket variants exactly when primary model has no suffix', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      modelUsage: {
        'claude-opus-4-6[1m]': { contextWindow: 1000000 },
        'claude-opus-4-6-20251001': { contextWindow: 200000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), 'opus', noSubtype)

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 200000,
    })
  })

  it('falls back to max contextWindow when primary model is missing from modelUsage', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      modelUsage: {
        'claude-haiku-4-5-20251001': { contextWindow: 200000 },
        'claude-opus-4-6[1m]': { contextWindow: 1000000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), 'sonnet', noSubtype)

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 1000000,
    })
  })

  it('returns null for empty inner message', () => {
    const msg = makeMsg(MessageSource.AGENT, {})
    expect(extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)).toBeNull()
  })

  it('extracts only subtype when no modelUsage or cost', () => {
    const content = { type: 'result', subtype: 'turn_end' }
    const msg = makeMsg(MessageSource.AGENT, content)
    expect(extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)).toEqual({ subtype: 'turn_end' })
  })

  it('returns null for subagent results with parent_tool_use_id', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      parent_tool_use_id: 'toolu_abc123',
      total_cost_usd: 0.05,
      modelUsage: {
        'claude-sonnet': { contextWindow: 200000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    expect(extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)).toBeNull()
  })

  it('extracts numToolUses from Claude Code result message', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      num_tool_uses: 5,
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)
    expect(result).toEqual({ subtype: 'turn_end', numToolUses: 5 })
  })

  it('extracts numToolUses=0 from Claude Code simple exchange', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      num_tool_uses: 0,
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, noSubtype)
    expect(result).toEqual({ subtype: 'turn_end', numToolUses: 0 })
  })

  it('takes the subtype from the provider fallback when inner.subtype is absent (with neutral numToolUses)', () => {
    // A provider whose terminal envelope carries no `subtype` derives it via the fallback (Codex
    // maps turn.status -> turn_completed; that derivation is tested in the codex plugin). Here a
    // stub fallback stands in, and num_tool_uses is read by the neutral extractor.
    const content = { turn: { status: 'completed' }, num_tool_uses: 3 }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, p => (getInnerMessage(p)?.turn ? 'turn_completed' : undefined))
    expect(result).toEqual({ subtype: 'turn_completed', numToolUses: 3 })
  })

  it('ignores the subtype fallback when inner.subtype is already present', () => {
    const content = { type: 'result', subtype: 'turn_end', num_tool_uses: 1 }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined, () => 'SHOULD_NOT_BE_USED')
    expect(result).toEqual({ subtype: 'turn_end', numToolUses: 1 })
  })
})

// ---------------------------------------------------------------------------
// extractSettingsChanges
// ---------------------------------------------------------------------------

describe('extractSettingsChanges', () => {
  it('extracts settings changes', () => {
    const content = {
      type: 'settings_changed',
      changes: { permissionMode: { old: 'default', new: 'plan' } },
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    const result = extractSettingsChanges(parseMessageContent(msg))

    expect(result).toEqual({ permissionMode: { old: 'default', new: 'plan' } })
  })

  it('returns null for non-settings_changed type', () => {
    const content = { type: 'rate_limit', changes: {} }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractSettingsChanges(parseMessageContent(msg))).toBeNull()
  })

  it('returns null when changes is missing', () => {
    const content = { type: 'settings_changed' }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractSettingsChanges(parseMessageContent(msg))).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// extractPlanUpdated
// ---------------------------------------------------------------------------

describe('extractPlanUpdated', () => {
  it('extracts payload from a wrapped plan_updated message', () => {
    const content = {
      type: 'plan_updated',
      plan_title: 'Add authentication',
      plan_file_path: '/plans/auth.md',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanUpdated(parseMessageContent(msg))).toEqual({
      planTitle: 'Add authentication',
      planFilePath: '/plans/auth.md',
      updateAgentTitle: false,
    })
  })

  it('preserves update_agent_title:true when the auto-rename branch fired', () => {
    const content = {
      type: 'plan_updated',
      plan_title: 'Auth Refactor',
      plan_file_path: '/plans/auth.md',
      update_agent_title: true,
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    const got = extractPlanUpdated(parseMessageContent(msg))
    expect(got?.updateAgentTitle).toBe(true)
  })

  it('returns the most recent plan_updated entry in a consolidated thread', () => {
    const earlier = { type: 'plan_updated', plan_title: 'old', plan_file_path: '/plans/old.md' }
    const later = { type: 'plan_updated', plan_title: 'new', plan_file_path: '/plans/new.md' }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(earlier, later))
    const got = extractPlanUpdated(parseMessageContent(msg))
    expect(got?.planTitle).toBe('new')
    expect(got?.planFilePath).toBe('/plans/new.md')
  })

  it('extracts payload from an unwrapped plan_updated notification', () => {
    const content = {
      type: 'plan_updated',
      plan_title: 'Unwrapped',
      plan_file_path: '/plans/u.md',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, content)
    const got = extractPlanUpdated(parseMessageContent(msg))
    expect(got?.planTitle).toBe('Unwrapped')
    expect(got?.planFilePath).toBe('/plans/u.md')
  })

  it('returns undefined for non-plan_updated messages', () => {
    const content = { type: 'settings_changed', plan_title: 'Not a plan update' }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanUpdated(parseMessageContent(msg))).toBeUndefined()
  })

  it('returns the payload even when fields are empty strings, leaving consumer to decide', () => {
    const content = { type: 'plan_updated', plan_title: '', plan_file_path: '' }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanUpdated(parseMessageContent(msg))).toEqual({
      planTitle: '',
      planFilePath: '',
      updateAgentTitle: false,
    })
  })

  it('coerces a non-boolean update_agent_title to false', () => {
    const content = {
      type: 'plan_updated',
      plan_title: 't',
      plan_file_path: '/p.md',
      update_agent_title: 'truthy-but-not-true',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanUpdated(parseMessageContent(msg))?.updateAgentTitle).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// extractPlanFilePath
// ---------------------------------------------------------------------------

describe('extractPlanFilePath', () => {
  it('extracts plan file path from wrapped plan_execution message', () => {
    const content = {
      type: 'plan_execution',
      plan_file_path: '/home/user/.claude/plans/plan.md',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/home/user/.claude/plans/plan.md')
  })

  it('extracts plan file path from wrapped thread with multiple messages', () => {
    const ccMsg = { type: 'context_cleared' }
    const peMsg = {
      type: 'plan_execution',
      plan_file_path: '/path/to/plan.md',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(ccMsg, peMsg))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/path/to/plan.md')
  })

  it('extracts plan file path from unwrapped plan_execution message', () => {
    const content = {
      type: 'plan_execution',
      plan_file_path: '/path/plan.md',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, content)
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/path/plan.md')
  })

  it('returns undefined when plan_file_path is empty', () => {
    const content = {
      type: 'plan_execution',
      plan_file_path: '',
    }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBeUndefined()
  })

  it('returns undefined for non-plan_execution messages', () => {
    const content = { type: 'context_cleared' }
    const msg = makeMsg(MessageSource.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBeUndefined()
  })
})
