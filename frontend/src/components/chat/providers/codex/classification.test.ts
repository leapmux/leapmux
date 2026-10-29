import { describe, expect, it } from 'vitest'
import { CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodexMessage } from './classification'

describe('stored rows with no current writer', () => {
  it.each([
    CODEX_METHOD.ThreadStarted,
    CODEX_METHOD.TurnStarted,
    CODEX_METHOD.ThreadStatusChanged,
    CODEX_METHOD.ThreadNameUpdated,
    CODEX_METHOD.ThreadSettingsUpdated,
    CODEX_METHOD.ThreadTokenUsageUpdated,
    CODEX_METHOD.SkillsChanged,
    CODEX_METHOD.RemoteControlStatusChanged,
    CODEX_METHOD.HookStarted,
    CODEX_METHOD.AutoApprovalReviewStarted,
    CODEX_METHOD.CommandExecutionRequestApproval,
    CODEX_METHOD.FileChangeRequestApproval,
    CODEX_METHOD.PermissionsRequestApproval,
    CODEX_METHOD.ToolRequestUserInput,
    CODEX_METHOD.McpToolCallProgress,
    CODEX_METHOD.ThreadReadStateChanged,
    CODEX_METHOD.TurnModerationMetadata,
    CODEX_METHOD.AccountUpdated,
    CODEX_METHOD.AccountRateLimitsUpdated,
    CODEX_METHOD.ThreadCompacted,
  ])('leaves a stored %s row unknown', (method) => {
    const frame = {
      method,
      params: { threadId: 'thread-1' },
    }
    expect(classifyCodexMessage(input(frame))).toEqual({ kind: 'unknown' })
    expect(classifyCodexMessage(input(frame, {
      old_seqs: [],
      messages: [frame],
    }))).toEqual({ kind: 'unknown' })
  })

  it.each(['userMessage', 'subAgentActivity'])('leaves a stored %s item unknown', (type) => {
    expect(classifyCodexMessage(input({
      item: { type, id: 'item-1' },
    }))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored compaction start unknown', () => {
    const frame = {
      method: CODEX_METHOD.ItemStarted,
      params: {
        item: { type: 'contextCompaction', id: 'item-1' },
      },
    }
    expect(classifyCodexMessage(input(frame))).toEqual({ kind: 'unknown' })
  })
})

describe('classifyCodexMessage', () => {
  it.each(['parameters', 'notification'])('recognizes native exec requests in the %s wrapper', (wrapper) => {
    const parameters = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call',
        call_id: 'exec-call',
        name: 'exec',
        input: 'text(40 + 2);',
        status: 'completed',
      },
    }
    const payload = wrapper === 'parameters'
      ? parameters
      : { method: 'rawResponseItem/completed', params: parameters }
    expect(classifyCodexMessage({
      ...input(payload, undefined, AgentProvider.CODEX),
      spanId: 'exec-call',
      spanType: 'exec',
    })).toEqual({ kind: 'tool_use' })
  })

  it.each(['parameters', 'notification'])('recognizes native exec outputs in the %s wrapper', (wrapper) => {
    const parameters = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call_output',
        call_id: 'exec-call',
        output: [
          { type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' },
          { type: 'input_text', text: '42' },
        ],
      },
    }
    const payload = wrapper === 'parameters'
      ? parameters
      : { method: 'rawResponseItem/completed', params: parameters }
    expect(classifyCodexMessage({
      ...input(payload, undefined, AgentProvider.CODEX),
      spanId: 'exec-call',
      spanType: 'exec',
    })).toEqual({ kind: 'tool_use' })
  })

  it.each([
    {
      type: 'custom_tool_call',
      call_id: 'other',
      name: 'exec',
      namespace: 'remote',
      input: 'text(42);',
    },
    {
      type: 'custom_tool_call',
      call_id: 'other',
      name: 'apply_patch',
      input: 'patch',
    },
    {
      type: 'function_call',
      call_id: 'other',
      name: 'exec_command',
      arguments: '{}',
    },
    {
      type: 'message',
      role: 'assistant',
      content: [
        { type: 'output_text', text: 'Mirror answer' },
      ],
    },
  ])('keeps a raw mirror or foreign call hidden: %j', (item) => {
    expect(classifyCodexMessage(input({
      method: 'rawResponseItem/completed',
      params: { threadId: 'native-thread', turnId: 'native-turn', item },
    }, undefined, AgentProvider.CODEX))).toEqual({ kind: 'hidden' })
  })

  it('does not classify an unnamed custom output in another tool span as native exec', () => {
    const parameters = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: { type: 'custom_tool_call_output', call_id: 'other-call', output: 'Other output' },
    }
    expect(classifyCodexMessage({
      ...input(parameters, undefined, AgentProvider.CODEX),
      spanId: 'other-call',
      spanType: 'apply_patch',
    }).kind).not.toBe('tool_use')
  })

  // The item type comes straight off the wire, and the classifier table is a plain
  // object. A type that identifies an `Object.prototype` member answered with a
  // FUNCTION, which the dispatch below then CALLED -- on the path every row takes.
  it.each([
    'constructor',
    'toString',
    'valueOf',
    'hasOwnProperty',
  ])('classifies an item typed %s as unknown', (type) => {
    expect(classifyCodexMessage(input({
      method: 'item/completed',
      params: {
        item: { id: 'x', type },
      },
    })))
      .toEqual({ kind: 'unknown' })
  })

  // The worker opts out of both methods. Stored copies have no current reader.
  it.each([
    [
      'turn/moderationMetadata',
      {
        threadId: 't1',
        turnId: 'u1',
        metadata: {
          generation: {},
          prompt: {},
          tool_call: {},
          tool_response: {},
        },
      },
    ],
    [
      'account/updated',
      { authMode: 'chatgpt', planType: 'prolite' },
    ],
  ])('leaves a stored %s row unknown', (method, params) => {
    expect(classifyCodexMessage(input({ method, params }))).toEqual({ kind: 'unknown' })
  })

  // Each RENDER notice classifies as a notification with the hidden empty
  // policy, so the row the entry builder suppresses draws nothing.
  it.each([
    [
      'warning',
      { message: 'careful' },
    ],
    [
      'configWarning',
      { summary: 'Error parsing rules', details: 'unexpected token', path: '/home/u/.codex/rules/x.rules' },
    ],
    [
      'deprecationNotice',
      { summary: 'The foo option is deprecated', details: 'Use bar instead.' },
    ],
    [
      'model/safetyBuffering/updated',
      { turnId: 'u1', showBufferingUi: true, fasterModel: 'gpt-6-luna' },
    ],
    [
      'model/verification',
      {
        verifications: ['trustedAccessForCyber'],
      },
    ],
    [
      'modelProvider/authRecoveryStarted',
      { provider: 'amazon-bedrock', message: 'AWS session has expired. Reauthenticating...' },
    ],
    [
      'modelProvider/authRecoveryCompleted',
      { provider: 'amazon-bedrock', message: 'Reauthenticated.' },
    ],
  ])('classifies %s as a notification', (method, params) => {
    const result = classifyCodexMessage(input({ method, params }))
    expect(result.kind).toBe('notification')
  })

  // Two persisted notices fold into ONE notification thread, and the thread
  // states one entry for each member, in order.
  it('folds consecutive render notices into one notification thread', () => {
    const configWarning = {
      method: 'configWarning',
      params: { summary: 'Error parsing rules' },
    }
    const deprecationNotice = {
      method: 'deprecationNotice',
      params: { summary: 'The foo option is deprecated' },
    }
    expect(classifyCodexMessage(input(configWarning, {
      old_seqs: [],
      messages: [configWarning, deprecationNotice],
    }))).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'status', text: 'Warning: Error parsing rules' },
        { kind: 'status', text: 'Deprecated: The foo option is deprecated' },
      ],
    })
  })

  it('leaves stored thread/started notifications unknown', () => {
    const parent = {
      method: 'thread/started',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves stored turn/started notifications unknown', () => {
    const parent = {
      method: 'turn/started',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
        turn: {
          id: 'turn_123',
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves stored thread/status/changed notifications unknown', () => {
    const parent = {
      method: 'thread/status/changed',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
        status: {
          type: 'active',
          activeFlags: ['waitingOnApproval'],
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it.each([
    null,
    { type: 'turn', turnId: 'turn-1' },
  ])('leaves stored read-state metadata with first unread %j unknown', (firstUnread) => {
    const frame = {
      method: 'thread/readState/changed',
      params: {
        threadId: 'thread-1',
        readState: { firstUnread, revision: 'revision-1' },
      },
      emittedAtMs: 1792140694686,
    }
    expect(classifyCodexMessage(input(frame))).toEqual({ kind: 'unknown' })
    expect(classifyCodexMessage(input(undefined, {
      old_seqs: [],
      messages: [frame],
    }))).toEqual({ kind: 'unknown' })
  })

  it('keeps unknown content beside native read-state metadata visible', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'thread/readState/changed',
          params: {
            threadId: 'thread-1',
            readState: { firstUnread: null, revision: 'r1' },
          },
        },
        {
          method: 'thread/futureContent',
          params: { text: 'Preserve this native content.' },
        },
      ],
    }
    expect(classifyCodexMessage(input(undefined, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('leaves stored skills/changed notifications unknown', () => {
    const parent = {
      method: 'skills/changed',
      params: {},
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves stored remoteControl/status/changed notifications unknown', () => {
    const parent = {
      method: 'remoteControl/status/changed',
      params: { status: 'disabled', environmentId: null },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it.each([
    'hook/started',
  ])('leaves stored %s notifications unknown', (method) => {
    const parent = {
      method,
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        run: { name: 'hook' },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('renders a failed hook completion as a notification', () => {
    const parent = {
      method: 'hook/completed',
      params: {
        run: { status: 'failed', statusMessage: 'permission denied' },
      },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'hook', outcome: 'failed', detail: 'permission denied' },
      ],
    })
  })

  it('reads a stored completed hook as a notification', () => {
    const parent = {
      method: 'hook/completed',
      params: {
        run: { status: 'completed' },
      },
    }
    expect(classifyCodexMessage(input(parent))).toMatchObject({ kind: 'notification' })
  })

  it('reads context_cleared beside an obsolete lifecycle event', () => {
    const contextCleared = { type: 'context_cleared' }
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'thread/started',
          params: { threadId: 'thread-1' },
        },
        contextCleared,
      ],
    }
    // The shared context-cleared entry keeps this wrapper readable. The obsolete method supplies no entry.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'context-cleared' },
      ],
    })
  })

  it('leaves a compaction item without its native completion method unknown', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          threadId: 't1',
          turnId: 'turn1',
          item: { type: 'contextCompaction', id: 'compact-1' },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('unknown')
  })

  it('leaves a wrapped compaction start unknown', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'item/started',
          params: {
            item: { type: 'contextCompaction', id: 'compact-1' },
            threadId: 't1',
            turnId: 'turn1',
          },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('unknown')
  })

  it('classifies wrapped raw item/completed+contextCompaction as a notification thread', () => {
    // The worker stores the native completion as a boundary. It publishes the start only as live state.
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'item/completed',
          params: {
            item: { type: 'contextCompaction', id: 'compact-1' },
            threadId: 't1',
            turnId: 'turn1',
          },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('notification')
  })

  it('does NOT classify wrapped item/completed for non-compaction items as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'item/completed',
          params: {
            item: { type: 'agentMessage', id: 'msg-1' },
          },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).not.toBe('notification')
  })

  it('does NOT classify wrapped item/started for non-compaction items as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'item/started',
          params: {
            item: { type: 'commandExecution', id: 'cmd-1' },
          },
        },
      ],
    }
    // commandExecution is rendered through the assistant span flow, not the
    // notification thread. Wrapping it must not turn it into a notification.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).not.toBe('notification')
  })

  it('leaves a stored skills and remote-control wrapper unknown', () => {
    // The worker opts out of both methods. Neither method has a browser reader.
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        {
          method: 'remoteControl/status/changed',
          params: { status: 'disabled', environmentId: null },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('reads a settings change beside obsolete metadata', () => {
    const settingsChanged = {
      type: 'settings_changed',
      changes: {
        model: { old: 'a', new: 'b' },
      },
    }
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        settingsChanged,
        {
          method: 'remoteControl/status/changed',
          params: { status: 'disabled', environmentId: null },
        },
      ],
    }
    // Only the settings change supplies an entry. Copy Raw JSON still receives the complete wrapper.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({
      kind: 'notification',
      entries: [
        {
          kind: 'settings-changed',
          changes: [
            { label: 'Model', old: 'a', new: 'b' },
          ],
        },
      ],
    })
  })

  it('leaves a stored name and usage wrapper unknown', () => {
    // No current worker produces this wrapper. Neither method has a browser reader.
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'thread/name/updated',
          params: { threadId: 't1', name: 'Refactor auth' },
        },
        {
          method: 'thread/tokenUsage/updated',
          params: { threadId: 't1' },
        },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves stored high-usage rate limit transcript rows unknown', () => {
    const parent = {
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: {
            usedPercent: 85,
            windowDurationMins: 300,
          },
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored account block unknown', () => {
    // Account state reaches the live session-info reader, not the transcript reader.
    const parent = {
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          rateLimitReachedType: 'workspace_owner_credits_depleted',
          primary: { usedPercent: 20, windowDurationMins: 300 },
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('reads stored MCP startup starting notifications', () => {
    const parent = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'starting', error: null },
    }
    expect(classifyCodexMessage(input(parent))).toMatchObject({ kind: 'notification' })
  })

  it('reads stored MCP startup finished states that are not failures', () => {
    for (const status of ['ready', 'cancelled']) {
      const parent = {
        method: 'mcpServer/startupStatus/updated',
        params: { name: 'codex_apps', status, error: null },
      }
      expect(classifyCodexMessage(input(parent))).toMatchObject({ kind: 'notification' })
    }
  })

  it('keeps MCP startup failures visible', () => {
    const parent = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'failed', error: 'boom' },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(1)
  })

  it('leaves stored MCP tool-call progress notifications unknown', () => {
    expect(classifyCodexMessage(input({
      method: 'item/mcpToolCall/progress',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        itemId: 'mcp-1',
        message: 'Working',
      },
    }))).toEqual({ kind: 'unknown' })
  })

  it('reads stored successful MCP OAuth completion and keeps failures visible', () => {
    const success = {
      method: 'mcpServer/oauthLogin/completed',
      params: { name: 'docs', success: true },
    }
    const failure = {
      method: 'mcpServer/oauthLogin/completed',
      params: { name: 'docs', success: false, error: 'authorization failed' },
    }
    expect(classifyCodexMessage(input(success))).toMatchObject({ kind: 'notification' })
    expect(classifyCodexMessage(input(failure))).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'status', text: 'MCP OAuth login failed for docs: authorization failed' },
      ],
    })
  })

  it('reads every stored MCP entry without a success filter', () => {
    const startupFailure = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'broken', status: 'failed', error: 'boom' },
    }
    const oauthFailure = {
      method: 'mcpServer/oauthLogin/completed',
      params: { name: 'docs', success: false, error: 'denied' },
    }
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'mcpServer/startupStatus/updated',
          params: { name: 'ready', status: 'ready' },
        },
        {
          method: 'item/mcpToolCall/progress',
          params: { itemId: 'mcp-1', message: 'Working' },
        },
        {
          method: 'mcpServer/oauthLogin/completed',
          params: { name: 'docs', success: true },
        },
        startupFailure,
        oauthFailure,
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('notification')
    if (result.kind === 'notification')
      expect(result.entries).toHaveLength(4)
  })

  it('leaves a stored compacting envelope of an older build unknown', () => {
    // No worker writes the type (F1): a stored row draws the no-display card.
    const parent = { type: 'compacting' }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored synthesized compaction boundary unknown', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    const result = classifyCodexMessage(input(parent))
    expect(result.kind).toBe('unknown')
  })

  it('classifies turn/plan/updated as a Codex tool-use message', () => {
    const parent = {
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        explanation: null,
        plan: [
          { step: 'Inspect messages', status: 'inProgress' },
          { step: 'Update renderer', status: 'pending' },
        ],
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'tool_use' })
  })

  // The worker stores each plan update as its own message row. Two updates in
  // one notification-thread row would freeze the card on the first plan: the
  // row's classifier reads the wrapper's first message. So consecutive plan rows
  // must never be claimed as one notification thread, and each keeps its own
  // tool-use row.
  it('does not fold consecutive turn/plan/updated rows into a notification thread', () => {
    const first = {
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        plan: [
          { step: 'Inspect messages', status: 'inProgress' },
        ],
      },
    }
    const second = {
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        plan: [
          { step: 'Inspect messages', status: 'completed' },
          { step: 'Update renderer', status: 'inProgress' },
        ],
      },
    }
    expect(classifyCodexMessage(input(first, {
      old_seqs: [],
      messages: [first, second],
    }))).toEqual({ kind: 'tool_use' })
    expect(classifyCodexMessage(input(second, {
      old_seqs: [],
      messages: [first, second],
    }))).toEqual({ kind: 'tool_use' })
  })

  it('classifies webSearch items as Codex tool-use messages', () => {
    const parent = {
      item: {
        type: 'webSearch',
        id: 'ws-1',
        query: 'https://example.com',
        action: { type: 'openPage', url: 'https://example.com' },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'tool_use' })
  })

  it('hides webSearch openPage items with null url', () => {
    const parent = {
      item: {
        type: 'webSearch',
        id: 'ws-2',
        query: '',
        action: { type: 'openPage', url: null },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('leaves stored thread/tokenUsage/updated notifications unknown', () => {
    const parent = {
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
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored usage and rate-limit wrapper unknown', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
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
        },
        {
          method: 'account/rateLimits/updated',
          params: {
            rateLimits: {
              primary: { usedPercent: 34, windowMinutes: 300 },
              secondary: { usedPercent: 10, windowMinutes: 10080 },
            },
          },
        },
      ],
    }
    expect(classifyCodexMessage(input(undefined, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored standalone thread/compacted notification unknown', () => {
    // Codex 0.163 sends no thread/compacted frame. Stored copies have no special reader.
    const parent = {
      method: 'thread/compacted',
      params: { threadId: 't1', turnId: 'turn1' },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored thread/compacted consolidated into a notification thread unknown', () => {
    const compacted = {
      method: 'thread/compacted',
      params: { threadId: 't1', turnId: 'turn1' },
    }
    const wrapper = {
      old_seqs: [7],
      messages: [compacted],
    }
    expect(classifyCodexMessage(input(compacted, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored standalone thread/settings/updated notification unknown', () => {
    // The worker applies this settings update and stores no transcript row.
    const parent = {
      method: 'thread/settings/updated',
      params: {
        threadId: 't1',
        threadSettings: { model: 'gpt-5.5', effort: 'xhigh' },
      },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored thread/settings/updated consolidated into a notification thread unknown', () => {
    const settingsUpdated = {
      method: 'thread/settings/updated',
      params: {
        threadId: 't1',
        threadSettings: { model: 'gpt-5.5' },
      },
    }
    const wrapper = {
      old_seqs: [6],
      messages: [settingsUpdated],
    }
    expect(classifyCodexMessage(input(settingsUpdated, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored synthesized compaction status unknown', () => {
    const parent = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored synthesized status wrapper unknown', () => {
    // The worker stores no synthesized system status. A wrapper adds no reader for it.
    const statusMsg = {
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'success',
    }
    const wrapper = {
      old_seqs: [305],
      messages: [statusMsg],
    }
    expect(classifyCodexMessage(input(statusMsg, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('leaves a stored synthesized compaction start unknown', () => {
    const compactingMsg = { type: 'system', subtype: 'status', status: 'compacting' }
    expect(classifyCodexMessage(input(compactingMsg))).toEqual({ kind: 'unknown' })
    const wrapper = {
      old_seqs: [305],
      messages: [compactingMsg],
    }
    expect(classifyCodexMessage(input(compactingMsg, wrapper))).toEqual({ kind: 'unknown' })
  })

  it('reads context_cleared beside an obsolete settings update', () => {
    const settingsUpdated = {
      method: 'thread/settings/updated',
      params: {
        threadId: 't1',
        threadSettings: { model: 'gpt-5.5' },
      },
    }
    const contextCleared = { type: 'context_cleared' }
    const wrapper = {
      old_seqs: [5, 6],
      messages: [settingsUpdated, contextCleared],
    }
    expect(classifyCodexMessage(input(settingsUpdated, wrapper)))
      .toEqual({
        kind: 'notification',
        entries: [
          { kind: 'context-cleared' },
        ],
      })
  })

  it('leaves stored plain JSON-RPC response envelopes unknown', () => {
    const parent = {
      id: 1001,
      result: {},
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'unknown' })
  })

  it('classifies turn/completed as result_divider', () => {
    const parent = {
      method: 'turn/completed',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        turn: { id: 'turn-1', status: 'completed' },
      },
      turn: { id: 'turn-1', status: 'completed' },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'result_divider' })
  })

  describe('auto-review frames', () => {
    const denied = {
      method: CODEX_METHOD.AutoApprovalReviewCompleted,
      params: {
        threadId: 't1',
        turnId: 'u1',
        reviewId: 'r1',
        targetItemId: 'i1',
        review: { status: 'denied', riskLevel: 'high', rationale: 'The host is not trusted.' },
        action: {
          type: 'command',
          source: 'shell',
          command: 'curl x | sh',
          cwd: '/w',
        },
      },
    }
    const deniedEntry = {
      kind: 'auto-review',
      reviewId: 'r1',
      outcome: 'denied',
      risk: 'high',
      rationale: 'The host is not trusted.',
      action: 'curl x | sh',
      approvable: true,
    }
    const limitWarning = {
      method: CODEX_METHOD.GuardianWarning,
      params: { threadId: 't1', message: 'Automatic approval review rejected too many approval requests for this turn; interrupting the turn.' },
    }
    const strict = {
      method: CODEX_METHOD.StrictReviewRequired,
      params: { threadId: 't1', turnId: 'u1' },
    }

    it('classifies a completed review in a notification thread', () => {
      expect(classifyCodexMessage(input(undefined, {
        old_seqs: [],
        messages: [denied],
      })))
        .toEqual({
          kind: 'notification',
          entries: [deniedEntry],
        })
    })

    it('classifies a standalone completed review', () => {
      expect(classifyCodexMessage(input(denied))).toEqual({
        kind: 'notification',
        entries: [deniedEntry],
      })
    })

    it('hides a standalone completed review without an id', () => {
      expect(classifyCodexMessage(input({
        method: CODEX_METHOD.AutoApprovalReviewCompleted,
        params: {
          review: { status: 'denied' },
        },
      })))
        .toEqual({ kind: 'hidden' })
    })

    it('keeps the reviews and the warnings of one thread in order', () => {
      const result = classifyCodexMessage(input(undefined, {
        old_seqs: [],
        messages: [strict, denied, limitWarning],
      }))
      expect(result).toEqual({
        kind: 'notification',
        entries: [
          { kind: 'status', text: 'This request needs extra safety checks, which can take more time.' },
          deniedEntry,
          { kind: 'status', text: limitWarning.params.message },
        ],
      })
    })

    it.each([
      ['the warning', limitWarning],
      ['the strict notice', strict],
    ])('classifies a standalone frame for %s', (_, frame) => {
      expect(classifyCodexMessage(input(frame)).kind).toBe('notification')
    })

    it('leaves a stored review start unknown', () => {
      expect(classifyCodexMessage(input({
        method: CODEX_METHOD.AutoApprovalReviewStarted,
        params: {
          threadId: 't1',
          reviewId: 'r1',
          review: { status: 'inProgress' },
        },
      })))
        .toEqual({ kind: 'unknown' })
    })

    it.each([
      CODEX_METHOD.CommandExecutionRequestApproval,
      CODEX_METHOD.FileChangeRequestApproval,
      CODEX_METHOD.PermissionsRequestApproval,
      CODEX_METHOD.ToolRequestUserInput,
    ])('leaves a stored %s control request unknown', (method) => {
      expect(classifyCodexMessage(input({
        id: 7,
        method,
        params: { threadId: 't1', turnId: 'u1', itemId: 'i1' },
      }))).toEqual({ kind: 'unknown' })
    })
  })

  it('reads stored synthetic Codex turn failed notifications', () => {
    const parent = {
      type: 'agent_error',
      error: 'Codex turn failed',
    }
    expect(classifyCodexMessage(input(parent))).toMatchObject({ kind: 'notification' })
  })

  it('reads stored notification threads containing only synthetic Codex turn failed notifications', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { type: 'agent_error', error: 'Codex turn failed' },
      ],
    }
    expect(classifyCodexMessage(input(undefined, wrapper))).toMatchObject({ kind: 'notification' })
  })
})
