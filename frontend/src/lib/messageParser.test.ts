import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import {
  extractAssistantUsage,
  extractRateLimitInfo,
  extractResultMetadata,
  extractSettingsChanges,
  extractTodos,
  findLatestTodos,
  getInnerMessage,
  getInnerMessageType,
  parseMessageContent,
} from './messageParser'

/** Build a mock AgentChatMessage with the given JSON content (uncompressed). */
function makeMsg(
  role: MessageRole,
  content: unknown,
  opts?: { seq?: bigint },
): AgentChatMessage {
  return {
    id: 'msg-1',
    role,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: opts?.seq ?? 1n,
    createdAt: '',
    updatedAt: '',
    deliveryError: '',
  } as AgentChatMessage
}

/** Wrap an inner message in the thread wrapper envelope. */
function wrap(...messages: unknown[]): { old_seqs: number[], messages: unknown[] } {
  return { old_seqs: [], messages }
}

// ---------------------------------------------------------------------------
// parseMessageContent
// ---------------------------------------------------------------------------

describe('parseMessageContent', () => {
  it('parses wrapped content', () => {
    const inner = { type: 'assistant', message: { content: [] } }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(inner))
    const result = parseMessageContent(msg)

    expect(result.isWrapped).toBe(true)
    expect(result.parentObject).toEqual(inner)
    expect(result.wrapper).not.toBeNull()
    expect(result.children).toEqual([])
    expect(result.rawText).toBeTruthy()
  })

  it('parses wrapped content with children', () => {
    const parent = { type: 'assistant', message: { content: [] } }
    const child = { type: 'user', message: { content: 'result' } }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(parent, child))
    const result = parseMessageContent(msg)

    expect(result.parentObject).toEqual(parent)
    expect(result.children).toEqual([child])
  })

  it('handles empty wrapper messages array', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, wrap())
    const result = parseMessageContent(msg)

    expect(result.isWrapped).toBe(true)
    expect(result.parentObject).toBeUndefined()
    expect(result.children).toEqual([])
  })

  it('parses unwrapped content', () => {
    const content = { type: 'agent_session_info', info: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, content)
    const result = parseMessageContent(msg)

    expect(result.isWrapped).toBe(false)
    expect(result.parentObject).toEqual(content)
    expect(result.topLevel).toEqual(content)
    expect(result.wrapper).toBeNull()
  })

  it('returns safe defaults for invalid JSON', () => {
    const msg = {
      id: 'msg-1',
      role: MessageRole.ASSISTANT,
      content: new TextEncoder().encode('not json'),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      createdAt: '',
      updatedAt: '',
      deliveryError: '',
    } as AgentChatMessage
    const result = parseMessageContent(msg)

    expect(result.rawText).toBe('not json')
    expect(result.topLevel).toBeNull()
    expect(result.parentObject).toBeUndefined()
  })

  it('returns safe defaults for empty content', () => {
    const msg = {
      id: 'msg-1',
      role: MessageRole.ASSISTANT,
      content: new Uint8Array(),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      createdAt: '',
      updatedAt: '',
      deliveryError: '',
    } as AgentChatMessage
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
  it('returns parentObject for wrapped content', () => {
    const inner = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(inner))
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(inner)
  })

  it('returns topLevel for unwrapped content', () => {
    const content = { type: 'agent_session_info' }
    const msg = makeMsg(MessageRole.LEAPMUX, content)
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(content)
  })
})

describe('getInnerMessageType', () => {
  it('returns type from wrapped content', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, wrap({ type: 'assistant' }))
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('assistant')
  })

  it('returns type from unwrapped content', () => {
    const msg = makeMsg(MessageRole.LEAPMUX, { type: 'rate_limit' })
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('rate_limit')
  })

  it('returns undefined when no type', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, wrap({ message: {} }))
    expect(getInnerMessageType(parseMessageContent(msg))).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// extractTodos
// ---------------------------------------------------------------------------

describe('extractTodos', () => {
  const todoContent = {
    type: 'assistant',
    message: {
      content: [
        {
          type: 'tool_use',
          name: 'TodoWrite',
          input: {
            todos: [
              { content: 'Write tests', status: 'completed', activeForm: 'Writing tests' },
              { content: 'Deploy', status: 'in_progress', activeForm: 'Deploying' },
              { content: 'Review', status: 'pending', activeForm: 'Reviewing' },
            ],
          },
        },
      ],
    },
  }

  it('extracts todos from a valid TodoWrite message', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(todoContent))
    const parsed = parseMessageContent(msg)
    const todos = extractTodos(msg, parsed)

    expect(todos).toEqual([
      { content: 'Write tests', status: 'completed', activeForm: 'Writing tests' },
      { content: 'Deploy', status: 'in_progress', activeForm: 'Deploying' },
      { content: 'Review', status: 'pending', activeForm: 'Reviewing' },
    ])
  })

  it('returns null for non-ASSISTANT role', () => {
    const msg = makeMsg(MessageRole.USER, wrap(todoContent))
    const parsed = parseMessageContent(msg)
    expect(extractTodos(msg, parsed)).toBeNull()
  })

  it('returns null for non-TodoWrite tool_use', () => {
    const content = {
      type: 'assistant',
      message: {
        content: [{ type: 'tool_use', name: 'Bash', input: { command: 'ls' } }],
      },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    const parsed = parseMessageContent(msg)
    expect(extractTodos(msg, parsed)).toBeNull()
  })

  it('returns null for assistant message with only text', () => {
    const content = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Hello' }] },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    const parsed = parseMessageContent(msg)
    expect(extractTodos(msg, parsed)).toBeNull()
  })

  it('defaults unknown status to pending', () => {
    const content = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          name: 'TodoWrite',
          input: { todos: [{ content: 'Task', status: 'unknown_status', activeForm: 'Working' }] },
        }],
      },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    const parsed = parseMessageContent(msg)
    const todos = extractTodos(msg, parsed)
    expect(todos![0].status).toBe('pending')
  })
})

// ---------------------------------------------------------------------------
// findLatestTodos
// ---------------------------------------------------------------------------

describe('findLatestTodos', () => {
  const makeTodoMsg = (seq: bigint, tasks: Array<{ content: string, status: string, activeForm: string }>) =>
    makeMsg(MessageRole.ASSISTANT, wrap({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          name: 'TodoWrite',
          input: { todos: tasks },
        }],
      },
    }), { seq })

  it('finds the latest TodoWrite scanning backward', () => {
    const messages = [
      makeMsg(MessageRole.USER, wrap({ type: 'user', message: { content: 'hello' } }), { seq: 1n }),
      makeTodoMsg(2n, [{ content: 'Old task', status: 'completed', activeForm: 'Old' }]),
      makeMsg(MessageRole.ASSISTANT, wrap({ type: 'assistant', message: { content: [{ type: 'text', text: 'response' }] } }), { seq: 3n }),
      makeTodoMsg(4n, [{ content: 'New task', status: 'in_progress', activeForm: 'New' }]),
      makeMsg(MessageRole.USER, wrap({ type: 'user', message: { content: 'bye' } }), { seq: 5n }),
    ]
    const todos = findLatestTodos(messages)
    expect(todos).toEqual([{ content: 'New task', status: 'in_progress', activeForm: 'New' }])
  })

  it('returns null when no TodoWrite exists', () => {
    const messages = [
      makeMsg(MessageRole.USER, wrap({ type: 'user', message: { content: 'hello' } }), { seq: 1n }),
      makeMsg(MessageRole.ASSISTANT, wrap({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } }), { seq: 2n }),
    ]
    expect(findLatestTodos(messages)).toBeNull()
  })

  it('returns null for empty array', () => {
    expect(findLatestTodos([])).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// extractAssistantUsage
// ---------------------------------------------------------------------------

describe('extractAssistantUsage', () => {
  it('extracts usage and cost', () => {
    const content = {
      type: 'assistant',
      total_cost_usd: 0.05,
      message: {
        usage: {
          input_tokens: 1000,
          cache_creation_input_tokens: 200,
          cache_read_input_tokens: 300,
        },
      },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    const result = extractAssistantUsage(parseMessageContent(msg))

    expect(result).toEqual({
      totalCostUsd: 0.05,
      contextUsage: {
        inputTokens: 1000,
        cacheCreationInputTokens: 200,
        cacheReadInputTokens: 300,
      },
    })
  })

  it('extracts only cost when no token info', () => {
    const content = {
      type: 'assistant',
      total_cost_usd: 0.01,
      message: { usage: {} },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    const result = extractAssistantUsage(parseMessageContent(msg))
    expect(result).toEqual({ totalCostUsd: 0.01 })
  })

  it('returns null when no usage field', () => {
    const content = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageRole.ASSISTANT, wrap(content))
    expect(extractAssistantUsage(parseMessageContent(msg))).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// extractResultMetadata
// ---------------------------------------------------------------------------

describe('extractResultMetadata', () => {
  it('extracts subtype, contextWindow, and cost', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      total_cost_usd: 0.10,
      modelUsage: {
        'claude-sonnet': { contextWindow: 200000 },
      },
    }
    const msg = makeMsg(MessageRole.RESULT, wrap(content))
    const result = extractResultMetadata(parseMessageContent(msg))

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 200000,
      totalCostUsd: 0.10,
    })
  })

  it('returns null for empty inner message', () => {
    const msg = makeMsg(MessageRole.RESULT, wrap({}))
    expect(extractResultMetadata(parseMessageContent(msg))).toBeNull()
  })

  it('extracts only subtype when no modelUsage or cost', () => {
    const content = { type: 'result', subtype: 'turn_end' }
    const msg = makeMsg(MessageRole.RESULT, wrap(content))
    expect(extractResultMetadata(parseMessageContent(msg))).toEqual({ subtype: 'turn_end' })
  })
})

// ---------------------------------------------------------------------------
// extractRateLimitInfo
// ---------------------------------------------------------------------------

describe('extractRateLimitInfo', () => {
  it('extracts rate limit info', () => {
    const content = {
      type: 'rate_limit',
      rate_limit_info: {
        rateLimitType: 'five_hour',
        status: 'allowed_warning',
        utilization: 0.85,
      },
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    const result = extractRateLimitInfo(parseMessageContent(msg))

    expect(result).toEqual({
      key: 'five_hour',
      info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
    })
  })

  it('defaults key to unknown when rateLimitType is missing', () => {
    const content = {
      type: 'rate_limit',
      rate_limit_info: { status: 'exceeded' },
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    const result = extractRateLimitInfo(parseMessageContent(msg))
    expect(result!.key).toBe('unknown')
  })

  it('returns null for non-rate_limit type', () => {
    const content = { type: 'settings_changed', rate_limit_info: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractRateLimitInfo(parseMessageContent(msg))).toBeNull()
  })

  it('returns null when rate_limit_info is missing', () => {
    const content = { type: 'rate_limit' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractRateLimitInfo(parseMessageContent(msg))).toBeNull()
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
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    const result = extractSettingsChanges(parseMessageContent(msg))

    expect(result).toEqual({ permissionMode: { old: 'default', new: 'plan' } })
  })

  it('returns null for non-settings_changed type', () => {
    const content = { type: 'rate_limit', changes: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractSettingsChanges(parseMessageContent(msg))).toBeNull()
  })

  it('returns null when changes is missing', () => {
    const content = { type: 'settings_changed' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractSettingsChanges(parseMessageContent(msg))).toBeNull()
  })
})
