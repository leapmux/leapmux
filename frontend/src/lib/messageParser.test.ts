import { describe, expect, it } from 'vitest'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '../../tests/unit/helpers/messageFactory'
import {
  extractAgentRenamed,
  extractAssistantUsage,
  extractCodexTokenUsage,
  extractPlanFilePath,
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
function makeMsg(role: MessageRole, content: unknown, opts?: { seq?: bigint }) {
  return makeMessage({ role, content: rawContent(content), seq: opts?.seq })
}

/** Wrap inner messages in a notification thread wrapper envelope (LEAPMUX only). */
function wrap(...messages: unknown[]): { old_seqs: number[], messages: unknown[] } {
  return { old_seqs: [], messages }
}

// ---------------------------------------------------------------------------
// parseMessageContent
// ---------------------------------------------------------------------------

describe('parseMessageContent', () => {
  it('parses LEAPMUX notification wrapper content', () => {
    const inner = { type: 'settings_changed', changes: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(inner))
    const result = parseMessageContent(msg)

    expect(result.wrapper).not.toBeNull()
    expect(result.parentObject).toEqual(inner)
    expect(result.rawText).toBeTruthy()
  })

  it('handles empty LEAPMUX wrapper messages array', () => {
    const msg = makeMsg(MessageRole.LEAPMUX, wrap())
    const result = parseMessageContent(msg)

    expect(result.wrapper).not.toBeNull()
    expect(result.parentObject).toBeUndefined()
  })

  it('parses raw content for non-LEAPMUX messages', () => {
    const content = { type: 'assistant', message: { content: [] } }
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    const result = parseMessageContent(msg)

    expect(result.wrapper).toBeNull()
    expect(result.parentObject).toEqual(content)
    expect(result.topLevel).toEqual(content)
  })

  it('parses unwrapped LEAPMUX content (e.g. agent_session_info)', () => {
    const content = { type: 'agent_session_info', info: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, content)
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
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(inner))
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(inner)
  })

  it('returns topLevel for raw content', () => {
    const content = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    const parsed = parseMessageContent(msg)

    expect(getInnerMessage(parsed)).toEqual(content)
  })
})

describe('getInnerMessageType', () => {
  it('returns type from raw content', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, { type: 'assistant' })
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('assistant')
  })

  it('returns type from LEAPMUX content', () => {
    const msg = makeMsg(MessageRole.LEAPMUX, { type: 'rate_limit' })
    expect(getInnerMessageType(parseMessageContent(msg))).toBe('rate_limit')
  })

  it('returns undefined when no type', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, { message: {} })
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
    const msg = makeMsg(MessageRole.ASSISTANT, todoContent)
    const parsed = parseMessageContent(msg)
    const todos = extractTodos(msg, parsed)

    expect(todos).toEqual([
      { content: 'Write tests', status: 'completed', activeForm: 'Writing tests' },
      { content: 'Deploy', status: 'in_progress', activeForm: 'Deploying' },
      { content: 'Review', status: 'pending', activeForm: 'Reviewing' },
    ])
  })

  it('returns null for non-ASSISTANT role', () => {
    const msg = makeMsg(MessageRole.USER, todoContent)
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
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    const parsed = parseMessageContent(msg)
    expect(extractTodos(msg, parsed)).toBeNull()
  })

  it('returns null for assistant message with only text', () => {
    const content = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Hello' }] },
    }
    const msg = makeMsg(MessageRole.ASSISTANT, content)
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
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    const parsed = parseMessageContent(msg)
    const todos = extractTodos(msg, parsed)
    expect(todos![0].status).toBe('pending')
  })

  it('extracts todos from a Codex turn/plan/updated message', () => {
    const msg = makeMsg(MessageRole.ASSISTANT, {
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        explanation: 'Need a plan',
        plan: [
          { step: 'Inspect messages', status: 'inProgress' },
          { step: 'Patch renderer', status: 'pending' },
          { step: 'Run tests', status: 'completed' },
        ],
      },
    })
    const parsed = parseMessageContent(msg)
    const todos = extractTodos(msg, parsed)

    expect(todos).toEqual([
      { content: 'Inspect messages', status: 'in_progress', activeForm: 'Inspect messages' },
      { content: 'Patch renderer', status: 'pending', activeForm: 'Patch renderer' },
      { content: 'Run tests', status: 'completed', activeForm: 'Run tests' },
    ])
  })
})

// ---------------------------------------------------------------------------
// findLatestTodos
// ---------------------------------------------------------------------------

describe('findLatestTodos', () => {
  const makeTodoMsg = (seq: bigint, tasks: Array<{ content: string, status: string, activeForm: string }>) =>
    makeMsg(MessageRole.ASSISTANT, {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          name: 'TodoWrite',
          input: { todos: tasks },
        }],
      },
    }, { seq })

  it('finds the latest TodoWrite scanning backward', () => {
    const messages = [
      makeMsg(MessageRole.USER, { type: 'user', message: { content: 'hello' } }, { seq: 1n }),
      makeTodoMsg(2n, [{ content: 'Old task', status: 'completed', activeForm: 'Old' }]),
      makeMsg(MessageRole.ASSISTANT, { type: 'assistant', message: { content: [{ type: 'text', text: 'response' }] } }, { seq: 3n }),
      makeTodoMsg(4n, [{ content: 'New task', status: 'in_progress', activeForm: 'New' }]),
      makeMsg(MessageRole.USER, { type: 'user', message: { content: 'bye' } }, { seq: 5n }),
    ]
    const todos = findLatestTodos(messages)
    expect(todos).toEqual([{ content: 'New task', status: 'in_progress', activeForm: 'New' }])
  })

  it('returns null when no TodoWrite exists', () => {
    const messages = [
      makeMsg(MessageRole.USER, { type: 'user', message: { content: 'hello' } }, { seq: 1n }),
      makeMsg(MessageRole.ASSISTANT, { type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } }, { seq: 2n }),
    ]
    expect(findLatestTodos(messages)).toBeNull()
  })

  it('returns null for empty array', () => {
    expect(findLatestTodos([])).toBeNull()
  })

  it('finds the latest Codex turn/plan/updated scanning backward', () => {
    const messages = [
      makeTodoMsg(2n, [{ content: 'Old task', status: 'completed', activeForm: 'Old' }]),
      makeMsg(MessageRole.ASSISTANT, {
        method: 'turn/plan/updated',
        params: {
          threadId: 'thread-1',
          turnId: 'turn-1',
          explanation: null,
          plan: [{ step: 'New Codex task', status: 'inProgress' }],
        },
      }, { seq: 4n }),
    ]
    const todos = findLatestTodos(messages)
    expect(todos).toEqual([{ content: 'New Codex task', status: 'in_progress', activeForm: 'New Codex task' }])
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
    const msg = makeMsg(MessageRole.ASSISTANT, content)
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
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    const result = extractAssistantUsage(parseMessageContent(msg))
    expect(result).toEqual({ totalCostUsd: 0.01 })
  })

  it('returns null when no usage field', () => {
    const content = { type: 'assistant', message: {} }
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    expect(extractAssistantUsage(parseMessageContent(msg))).toBeNull()
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
    const msg = makeMsg(MessageRole.ASSISTANT, content)
    expect(extractAssistantUsage(parseMessageContent(msg))).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// extractCodexTokenUsage
// ---------------------------------------------------------------------------

describe('extractCodexTokenUsage', () => {
  it('extracts context usage from persisted Codex token usage notifications', () => {
    const msg = makeMsg(MessageRole.LEAPMUX, wrap({
      method: 'thread/tokenUsage/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        tokenUsage: {
          total: {
            totalTokens: 200,
            inputTokens: 100,
            cachedInputTokens: 25,
            outputTokens: 50,
            reasoningOutputTokens: 9,
          },
          last: {
            totalTokens: 23,
            inputTokens: 10,
            cachedInputTokens: 5,
            outputTokens: 7,
            reasoningOutputTokens: 1,
          },
          modelContextWindow: 4096,
        },
      },
    }))

    expect(extractCodexTokenUsage(parseMessageContent(msg))).toEqual({
      contextUsage: {
        inputTokens: 100,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 25,
        contextWindow: 4096,
      },
    })
  })

  it('returns null for unrelated messages', () => {
    const msg = makeMsg(MessageRole.LEAPMUX, wrap({ method: 'turn/completed', params: {} }))
    expect(extractCodexTokenUsage(parseMessageContent(msg))).toBeNull()
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
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))

    expect(result).toEqual({
      subtype: 'turn_end',
      contextWindow: 200000,
      totalCostUsd: 0.10,
    })
  })

  it('returns null for empty inner message', () => {
    const msg = makeMsg(MessageRole.RESULT, {})
    expect(extractResultMetadata(parseMessageContent(msg))).toBeNull()
  })

  it('extracts only subtype when no modelUsage or cost', () => {
    const content = { type: 'result', subtype: 'turn_end' }
    const msg = makeMsg(MessageRole.RESULT, content)
    expect(extractResultMetadata(parseMessageContent(msg))).toEqual({ subtype: 'turn_end' })
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
    const msg = makeMsg(MessageRole.RESULT, content)
    expect(extractResultMetadata(parseMessageContent(msg))).toBeNull()
  })

  it('extracts numToolUses from Claude Code result message', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      num_tool_uses: 5,
    }
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))
    expect(result).toEqual({ subtype: 'turn_end', numToolUses: 5 })
  })

  it('extracts numToolUses=0 from Claude Code simple exchange', () => {
    const content = {
      type: 'result',
      subtype: 'turn_end',
      num_tool_uses: 0,
    }
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))
    expect(result).toEqual({ subtype: 'turn_end', numToolUses: 0 })
  })

  it('extracts Codex turn/completed with tool uses as complex turn', () => {
    // Codex turn/completed is stored unwrapped with num_tool_uses injected by backend
    const content = {
      turn: { status: 'completed', usage: { inputTokens: 100, outputTokens: 50 } },
      num_tool_uses: 3,
    }
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))
    expect(result).toEqual({ subtype: 'turn_completed', numToolUses: 3 })
  })

  it('extracts Codex turn/completed with zero tool uses as simple exchange', () => {
    const content = {
      turn: { status: 'completed', usage: { inputTokens: 100, outputTokens: 50 } },
      num_tool_uses: 0,
    }
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))
    expect(result).toEqual({ subtype: 'turn_completed', numToolUses: 0 })
  })

  it('extracts Codex turn/completed with failed status', () => {
    const content = {
      turn: { status: 'failed' },
      num_tool_uses: 0,
    }
    const msg = makeMsg(MessageRole.RESULT, content)
    const result = extractResultMetadata(parseMessageContent(msg))
    expect(result).toEqual({ subtype: 'turn_completed', numToolUses: 0 })
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

    expect(result).toEqual([{
      key: 'five_hour',
      info: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85 },
    }])
  })

  it('defaults key to unknown when rateLimitType is missing', () => {
    const content = {
      type: 'rate_limit',
      rate_limit_info: { status: 'exceeded' },
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    const result = extractRateLimitInfo(parseMessageContent(msg))
    expect(result[0].key).toBe('unknown')
  })

  it('returns empty array for non-rate_limit type', () => {
    const content = { type: 'settings_changed', rate_limit_info: {} }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractRateLimitInfo(parseMessageContent(msg))).toEqual([])
  })

  it('returns empty array when rate_limit_info is missing', () => {
    const content = { type: 'rate_limit' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractRateLimitInfo(parseMessageContent(msg))).toEqual([])
  })

  it('extracts Codex native rate limit info', () => {
    const content = {
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: { usedPercent: 85, windowDurationMins: 300, resetsAt: 1774070211 },
          secondary: { usedPercent: 4, windowDurationMins: 10080, resetsAt: 1774525963 },
        },
      },
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    const result = extractRateLimitInfo(parseMessageContent(msg))
    expect(result).toHaveLength(2)
    expect(result[0].key).toBe('five_hour')
    expect(result[0].info.utilization).toBeCloseTo(0.85)
    expect(result[0].info.status).toBe('allowed_warning')
    expect(result[1].key).toBe('seven_day')
    expect(result[1].info.utilization).toBeCloseTo(0.04)
    expect(result[1].info.status).toBe('allowed')
  })

  it('returns empty array for Codex rate limit without tiers', () => {
    const content = {
      method: 'account/rateLimits/updated',
      params: { rateLimits: {} },
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractRateLimitInfo(parseMessageContent(msg))).toEqual([])
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

// ---------------------------------------------------------------------------
// extractAgentRenamed
// ---------------------------------------------------------------------------

describe('extractAgentRenamed', () => {
  it('extracts title from wrapped agent_renamed message', () => {
    const content = { type: 'agent_renamed', title: 'Add authentication' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractAgentRenamed(parseMessageContent(msg))).toBe('Add authentication')
  })

  it('extracts title from wrapped thread with multiple messages', () => {
    const otherMsg = { type: 'settings_changed' }
    const renameMsg = { type: 'agent_renamed', title: 'My Plan Title' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(otherMsg, renameMsg))
    expect(extractAgentRenamed(parseMessageContent(msg))).toBe('My Plan Title')
  })

  it('extracts title from unwrapped agent_renamed message', () => {
    const content = { type: 'agent_renamed', title: 'Unwrapped Title' }
    const msg = makeMsg(MessageRole.LEAPMUX, content)
    expect(extractAgentRenamed(parseMessageContent(msg))).toBe('Unwrapped Title')
  })

  it('returns undefined when title is empty', () => {
    const content = { type: 'agent_renamed', title: '' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractAgentRenamed(parseMessageContent(msg))).toBeUndefined()
  })

  it('returns undefined for non-agent_renamed messages', () => {
    const content = { type: 'settings_changed', title: 'Not a rename' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractAgentRenamed(parseMessageContent(msg))).toBeUndefined()
  })

  it('returns undefined when title is missing', () => {
    const content = { type: 'agent_renamed' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractAgentRenamed(parseMessageContent(msg))).toBeUndefined()
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
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/home/user/.claude/plans/plan.md')
  })

  it('extracts plan file path from wrapped thread with multiple messages', () => {
    const ccMsg = { type: 'context_cleared' }
    const peMsg = {
      type: 'plan_execution',
      plan_file_path: '/path/to/plan.md',
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(ccMsg, peMsg))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/path/to/plan.md')
  })

  it('extracts plan file path from unwrapped plan_execution message', () => {
    const content = {
      type: 'plan_execution',
      plan_file_path: '/path/plan.md',
    }
    const msg = makeMsg(MessageRole.LEAPMUX, content)
    expect(extractPlanFilePath(parseMessageContent(msg))).toBe('/path/plan.md')
  })

  it('returns undefined when plan_file_path is empty', () => {
    const content = {
      type: 'plan_execution',
      plan_file_path: '',
    }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBeUndefined()
  })

  it('returns undefined for non-plan_execution messages', () => {
    const content = { type: 'context_cleared' }
    const msg = makeMsg(MessageRole.LEAPMUX, wrap(content))
    expect(extractPlanFilePath(parseMessageContent(msg))).toBeUndefined()
  })
})
