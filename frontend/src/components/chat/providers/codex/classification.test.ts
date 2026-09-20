import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { classifyCodexMessage } from './classification'

describe('classifyCodexMessage', () => {
  // The item type comes straight off the wire, and the classifier table is a plain
  // object. A type that identifies an `Object.prototype` member answered with a
  // FUNCTION, which the dispatch below then CALLED -- on the path every row takes.
  it.each(['constructor', 'toString', 'valueOf', 'hasOwnProperty'])('classifies an item typed %s as unknown', (type) => {
    expect(classifyCodexMessage(input({ method: 'item/completed', params: { item: { id: 'x', type } } })))
      .toEqual({ kind: 'unknown' })
  })

  it('hides thread/started notifications', () => {
    const parent = {
      method: 'thread/started',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides turn/started notifications', () => {
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
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides thread/status/changed notifications', () => {
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
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides skills/changed notifications', () => {
    const parent = {
      method: 'skills/changed',
      params: {},
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides remoteControl/status/changed notifications', () => {
    const parent = {
      method: 'remoteControl/status/changed',
      params: { status: 'disabled', environmentId: null },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it.each([
    'hook/started',
  ])('hides %s notifications', (method) => {
    const parent = {
      method,
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        run: { name: 'hook' },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('renders a failed hook completion as a notification', () => {
    const parent = {
      method: 'hook/completed',
      params: { run: { status: 'failed', statusMessage: 'permission denied' } },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('hides a successful hook completion', () => {
    const parent = {
      method: 'hook/completed',
      params: { run: { status: 'completed' } },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies mixed wrappers when context_cleared follows a hidden Codex lifecycle event', () => {
    const contextCleared = { type: 'context_cleared' }
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'thread/started', params: { threadId: 'thread-1' } },
        contextCleared,
      ],
    }
    // thread/started is a hidden lifecycle event, so it is dropped from the
    // rendered messages; the visible context_cleared keeps the thread alive.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'notification', messages: [contextCleared] })
  })

  it('classifies a completed contextCompaction item as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{ threadId: 't1', turnId: 'turn1', item: { type: 'contextCompaction', id: 'compact-1' } }],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('notification')
  })

  it('classifies wrapped raw item/started+contextCompaction (Phase 4.2) as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/started',
        params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
      }],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('notification')
  })

  it('classifies wrapped raw item/completed+contextCompaction as a notification thread', () => {
    // The Worker persists the COMPLETION of a contextCompaction item as the
    // compaction boundary. The thread that shows the compacting indicator must
    // recognize the boundary that closes it.
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/completed',
        params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
      }],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).toBe('notification')
  })

  it('does NOT classify wrapped item/completed for non-compaction items as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/completed',
        params: { item: { type: 'agentMessage', id: 'msg-1' } },
      }],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).not.toBe('notification')
  })

  it('does NOT classify wrapped item/started for non-compaction items as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/started',
        params: { item: { type: 'commandExecution', id: 'cmd-1' } },
      }],
    }
    // commandExecution is rendered through the assistant span flow, not the
    // notification thread. Wrapping it must not turn it into a notification.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result.kind).not.toBe('notification')
  })

  it('collapses a thread of only hidden Codex metadata (skills + remote-control) to hidden', () => {
    // Both are hidden lifecycle methods that render nothing; a thread of only
    // such entries must collapse to hidden rather than surface a `notification`
    // that renders no rows and falls back to a raw-JSON bubble.
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('drops hidden Codex metadata but keeps the visible notification thread entry', () => {
    const settingsChanged = { type: 'settings_changed', changes: { model: { old: 'a', new: 'b' } } }
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        settingsChanged,
        { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
      ],
    }
    // The hidden metadata is filtered from the rendered messages (the full
    // wrapper is still preserved for "Copy Raw JSON" via parsed.rawText); only
    // the visible settings_changed survives.
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'notification', messages: [settingsChanged] })
  })

  it('collapses a thread of only thread/name/updated + thread/tokenUsage/updated to hidden', () => {
    // Both are hidden lifecycle methods, so the consolidated thread is hidden --
    // matching how each is hidden when it arrives standalone.
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'thread/name/updated', params: { threadId: 't1', name: 'Refactor auth' } },
        { method: 'thread/tokenUsage/updated', params: { threadId: 't1' } },
      ],
    }
    const result = classifyCodexMessage(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('keeps high-usage rate limit notifications visible', () => {
    const parent = {
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: {
            usedPercent: 85,
            windowMinutes: 300,
          },
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('keeps a reached-type block visible even when all windows are under threshold', () => {
    // Credit depletion leaves the rolling windows with headroom, so the
    // all-allowed check would hide it; the authoritative reached-type must not.
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
    expect(result).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('hides MCP startup starting notifications', () => {
    const parent = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'starting', error: null },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides MCP startup terminal states that are not failures', () => {
    for (const status of ['ready', 'cancelled']) {
      const parent = {
        method: 'mcpServer/startupStatus/updated',
        params: { name: 'codex_apps', status, error: null },
      }
      expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
    }
  })

  it('keeps MCP startup failures visible', () => {
    const parent = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'failed', error: 'boom' },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('hides MCP tool-call progress notifications', () => {
    expect(classifyCodexMessage(input({
      method: 'item/mcpToolCall/progress',
      params: { threadId: 'thread-1', turnId: 'turn-1', itemId: 'mcp-1', message: 'Working' },
    }))).toEqual({ kind: 'hidden' })
  })

  it('hides successful MCP OAuth completion and keeps failures visible', () => {
    const success = { method: 'mcpServer/oauthLogin/completed', params: { name: 'docs', success: true } }
    const failure = { method: 'mcpServer/oauthLogin/completed', params: { name: 'docs', success: false, error: 'authorization failed' } }
    expect(classifyCodexMessage(input(success))).toEqual({ kind: 'hidden' })
    expect(classifyCodexMessage(input(failure))).toEqual({ kind: 'notification', messages: [failure] })
  })

  it('filters non-failure MCP entries from a notification thread', () => {
    const startupFailure = { method: 'mcpServer/startupStatus/updated', params: { name: 'broken', status: 'failed', error: 'boom' } }
    const oauthFailure = { method: 'mcpServer/oauthLogin/completed', params: { name: 'docs', success: false, error: 'denied' } }
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'mcpServer/startupStatus/updated', params: { name: 'ready', status: 'ready' } },
        { method: 'item/mcpToolCall/progress', params: { itemId: 'mcp-1', message: 'Working' } },
        { method: 'mcpServer/oauthLogin/completed', params: { name: 'docs', success: true } },
        startupFailure,
        oauthFailure,
      ],
    }
    expect(classifyCodexMessage(input(undefined, wrapper)))
      .toEqual({ kind: 'notification', messages: [startupFailure, oauthFailure] })
  })

  it('classifies compacting as notification', () => {
    const parent = { type: 'compacting' }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'notification', messages: [parent] })
  })

  it('classifies compact_boundary system messages as notification', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'notification', messages: [parent] })
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

  it('hides thread/tokenUsage/updated notifications', () => {
    const parent = {
      method: 'thread/tokenUsage/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        tokenUsage: {
          total: { totalTokens: 200, inputTokens: 100, cachedInputTokens: 25, outputTokens: 50, reasoningOutputTokens: 9 },
          last: { totalTokens: 23, inputTokens: 10, cachedInputTokens: 5, outputTokens: 7, reasoningOutputTokens: 1 },
          modelContextWindow: 4096,
        },
      },
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides notification threads containing only hidden Codex notifications', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'thread/tokenUsage/updated',
          params: {
            threadId: 'thread-1',
            turnId: 'turn-1',
            tokenUsage: {
              total: { totalTokens: 200, inputTokens: 100, cachedInputTokens: 25, outputTokens: 50, reasoningOutputTokens: 9 },
              last: { totalTokens: 23, inputTokens: 10, cachedInputTokens: 5, outputTokens: 7, reasoningOutputTokens: 1 },
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
    expect(classifyCodexMessage(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('hides a standalone thread/compacted notification', () => {
    // Codex reports an automatic compaction with this method. The chat shows
    // the boundary that item/completed carries, so this one renders nothing.
    const parent = { method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a thread/compacted consolidated into a notification thread', () => {
    const compacted = { method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }
    const wrapper = { old_seqs: [7], messages: [compacted] }
    expect(classifyCodexMessage(input(compacted, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('hides a standalone thread/settings/updated notification', () => {
    // Codex emits this whenever thread settings change (model, effort, sandbox,
    // etc.); it carries no chat-worthy content, so it is a hidden lifecycle event.
    const parent = {
      method: 'thread/settings/updated',
      params: { threadId: 't1', threadSettings: { model: 'gpt-5.5', effort: 'xhigh' } },
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a thread/settings/updated consolidated into a notification thread', () => {
    const settingsUpdated = {
      method: 'thread/settings/updated',
      params: { threadId: 't1', threadSettings: { model: 'gpt-5.5' } },
    }
    const wrapper = { old_seqs: [6], messages: [settingsUpdated] }
    expect(classifyCodexMessage(input(settingsUpdated, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('hides a standalone terminal compaction status (status=null, compact_result=success)', () => {
    const parent = { type: 'system', subtype: 'status', status: null, compact_result: 'success' }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides a terminal compaction status when consolidated into a notification thread', () => {
    // Parity with the standalone classifier and with Claude: a status hidden on
    // its own stays hidden once Hub threads it, instead of leaking as raw JSON.
    const statusMsg = { type: 'system', subtype: 'status', status: null, compact_result: 'success' }
    const wrapper = { old_seqs: [305], messages: [statusMsg] }
    expect(classifyCodexMessage(input(statusMsg, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('keeps the in-progress compacting status visible standalone and consolidated', () => {
    const compactingMsg = { type: 'system', subtype: 'status', status: 'compacting' }
    expect(classifyCodexMessage(input(compactingMsg))).toEqual({ kind: 'notification', messages: [compactingMsg] })
    const wrapper = { old_seqs: [305], messages: [compactingMsg] }
    expect(classifyCodexMessage(input(compactingMsg, wrapper))).toEqual({ kind: 'notification', messages: [compactingMsg] })
  })

  it('drops a hidden thread/settings/updated from a thread but keeps the visible entry', () => {
    const settingsUpdated = {
      method: 'thread/settings/updated',
      params: { threadId: 't1', threadSettings: { model: 'gpt-5.5' } },
    }
    const contextCleared = { type: 'context_cleared' }
    const wrapper = { old_seqs: [5, 6], messages: [settingsUpdated, contextCleared] }
    expect(classifyCodexMessage(input(settingsUpdated, wrapper)))
      .toEqual({ kind: 'notification', messages: [contextCleared] })
  })

  it('hides plain JSON-RPC response envelopes', () => {
    const parent = {
      id: 1001,
      result: {},
    }
    const result = classifyCodexMessage(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
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

  it('hides synthetic Codex turn failed notifications', () => {
    const parent = {
      type: 'agent_error',
      error: 'Codex turn failed',
    }
    expect(classifyCodexMessage(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides notification threads containing only synthetic Codex turn failed notifications', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { type: 'agent_error', error: 'Codex turn failed' },
      ],
    }
    expect(classifyCodexMessage(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })
})
