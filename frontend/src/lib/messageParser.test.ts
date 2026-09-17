import type { ParsedMessageContent } from './messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { describe, expect, it } from 'vitest'
import { NOTIFICATION_THREAD_TYPE } from '~/generated/contracts/worker-vocab'
import { ContentCompression, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import {
  extractContextUsage,
  extractPlanFilePath,
  extractPlanUpdated,
  extractResultMetadata,
  extractSettingsChanges,
  getInnerMessage,
  getInnerMessageType,
  messageUsage,
  parseMessageContent,
} from './messageParser'

/** Build a mock AgentChatMessage with the given JSON content (uncompressed). */
function makeMsg(source: MessageSource, content: unknown, opts?: { seq?: bigint, spanId?: string, spanType?: string }) {
  return makeMessage({
    source,
    content: rawContent(content),
    // The factory's Partial<> optionals reject an explicit undefined; omit when absent.
    ...(opts?.seq === undefined ? {} : { seq: opts.seq }),
    ...(opts?.spanId === undefined ? {} : { spanId: opts.spanId }),
    ...(opts?.spanType === undefined ? {} : { spanType: opts.spanType }),
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
  it('keeps typed completion when provider JSON is invalid', () => {
    const content = new TextEncoder().encode('{unfinished')
    const result = parseMessageContent(makeMessage({ content, completion: MessageCompletion.INTERRUPTED }))
    expect(result.rawText).toBe('{unfinished')
    expect(result.parentObject).toBeUndefined()
    expect(result.completion).toBe(MessageCompletion.INTERRUPTED)
  })

  it('separates typed completion from a provider field with the same name', () => {
    const original = { _leapmux: { completion: 'complete' }, completion: 'provider-specific' }
    const result = parseMessageContent(makeMessage({ content: rawContent(original), completion: MessageCompletion.ERROR }))
    expect(result.completion).toBe(MessageCompletion.ERROR)
    expect(result.parentObject).toEqual(original)
  })

  it('keeps original and supplemental JSON in separate fields', () => {
    const original = { type: 'tool_call', supplemental_content: { native: true }, _leapmux: 'native' }
    const supplemental = { rawInput: { command: 'npm test' } }
    const result = parseMessageContent(makeMessage({ content: rawContent(original), supplementalContent: rawContent({ provider: supplemental, metadata: { duration_ms: 0 } }) }))
    expect(result.topLevel).toEqual(original)
    expect(result.parentObject).toEqual(original)
    expect(result.rawText).toBe(JSON.stringify(original))
    expect(result.supplementalContent).toEqual(supplemental)
    expect(result.messageMetadata).toEqual({ duration_ms: 0 })
  })

  it('preserves the original when supplemental JSON is invalid', () => {
    const original = { content: 'Original text' }
    const result = parseMessageContent(makeMessage({ content: rawContent(original), supplementalContent: new TextEncoder().encode('{invalid') }))
    expect(result.parentObject).toEqual(original)
    expect(result.supplementalContent).toBeUndefined()
    expect(result.supplementalRawText).toBe('{invalid')
  })

  it('preserves the original when supplemental compression is invalid', () => {
    const original = { content: 'Original text' }
    const message = makeMessage({
      content: rawContent(original),
      supplementalContent: new Uint8Array([1, 2, 3]),
      supplementalContentCompression: ContentCompression.ZSTD,
    })
    expect(parseMessageContent(message).parentObject).toEqual(original)
  })

  it('retains a byte order mark in the original text', () => {
    const original = '\uFEFF{"content":"Native text"}'
    const message = makeMessage({ content: new TextEncoder().encode(original) })
    expect(parseMessageContent(message).rawText).toBe(original)
  })

  it('reports invalid original text encoding without replacing its bytes', () => {
    const message = makeMessage({ content: new Uint8Array([0xFF, 0xFE]) })
    const parsed = parseMessageContent(message)
    expect(parsed.contentDecodeFailed).toBe(true)
    expect(message.content).toEqual(new Uint8Array([0xFF, 0xFE]))
  })

  it('keeps the original readable when supplemental text encoding is invalid', () => {
    const original = { content: 'Native text' }
    const message = makeMessage({ content: rawContent(original), supplementalContent: new Uint8Array([0xFF, 0xFE]) })
    const parsed = parseMessageContent(message)
    expect(parsed.parentObject).toEqual(original)
    expect(parsed.supplementalRawText).toBeUndefined()
    expect(parsed.supplementalContent).toBeUndefined()
  })

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
// extractResultMetadata
// ---------------------------------------------------------------------------

/*
 * This reads the SESSION metadata a turn end carries -- the context window, the
 * normalized usage and the running cost -- and nothing else. The turn's own totals
 * (`num_tool_uses`, `total_cost_usd`, `duration_ms`) belong to the row the reader
 * sees, and `dividerMetaFromMessage` states them there.
 */
describe('extractResultMetadata', () => {
  it('extracts contextWindow and cost', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      total_cost_usd: 0.10,
      modelUsage: {
        'claude-sonnet': { contextWindow: 200000 },
      },
    }
    const msg = makeMsg(MessageSource.AGENT, content)
    const result = extractResultMetadata(parseMessageContent(msg), undefined)

    expect(result).toEqual({
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
    const result = extractResultMetadata(parseMessageContent(msg), undefined)

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
    const result = extractResultMetadata(parseMessageContent(msg), 'opus[1m]')

    expect(result).toEqual({
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
    const result = extractResultMetadata(parseMessageContent(msg), 'opus')

    expect(result).toEqual({
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
    const result = extractResultMetadata(parseMessageContent(msg), 'sonnet')

    expect(result).toEqual({
      contextWindow: 1000000,
    })
  })

  it('returns null for empty inner message', () => {
    const msg = makeMsg(MessageSource.AGENT, {})
    expect(extractResultMetadata(parseMessageContent(msg), undefined)).toBeNull()
  })

  it('answers null when the frame carries no session metadata', () => {
    const content = { type: 'result', subtype: 'turn_end' }
    const msg = makeMsg(MessageSource.AGENT, content)
    expect(extractResultMetadata(parseMessageContent(msg), undefined)).toBeNull()
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
    expect(extractResultMetadata(parseMessageContent(msg), undefined)).toBeNull()
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
