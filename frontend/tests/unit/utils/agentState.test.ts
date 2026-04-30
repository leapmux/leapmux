import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isAgentWorking, shouldShowThinkingIndicator } from '~/utils/agentState'
import { makeMessage, rawContent, wrapContent } from '../helpers/messageFactory'

function makeMsg(role: MessageRole, content?: Uint8Array, deliveryError?: string) {
  return makeMessage({ role, content, deliveryError })
}

function makeAgent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    $typeName: 'leapmux.v1.AgentInfo' as const,
    id: 'agent-1',
    workspaceId: 'ws-1',
    title: 'Agent Olivia',
    model: '',
    status: AgentStatus.ACTIVE,
    workingDir: '/tmp',
    permissionMode: '',
    effort: '',
    agentSessionId: '',
    homeDir: '/tmp',
    workerId: 'worker-1',
    createdAt: '2025-01-15T10:00:00.000Z',
    gitStatus: undefined,
    agentProvider: AgentProvider.CLAUDE_CODE,
    availableModels: [],
    availableOptionGroups: [],
    extraSettings: {},
    ...overrides,
  } as AgentInfo
}

describe('isAgentWorking', () => {
  it('returns false for an empty messages array', () => {
    expect(isAgentWorking([])).toBe(false)
  })

  it('returns true when last message is USER role', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
    ])).toBe(true)
  })

  it('returns true when last message is ASSISTANT role', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
    ])).toBe(true)
  })

  it('returns false when last message is TURN_END', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
    ])).toBe(false)
  })

  it('returns false on TURN_END regardless of inner type (every TURN_END is a real turn end)', () => {
    // No producer creates TURN_END messages with mid-turn types; the role
    // alone is the contract. Spot-check with a non-result inner type.
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'interrupted' })),
    ])).toBe(false)
  })

  it('handles TURN_END with unparseable content as a turn end', () => {
    const badContent = new TextEncoder().encode('not valid json')
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.TURN_END, badContent),
    ])).toBe(false)
  })

  it('handles TURN_END with empty content as a turn end', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.TURN_END),
    ])).toBe(false)
  })

  it('skips LEAPMUX message and finds TURN_END underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
    ])).toBe(false)
  })

  it('skips LEAPMUX settings_changed and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
    ])).toBe(true)
  })

  it('treats LEAPMUX context_cleared as turn boundary (returns false)', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips multiple trailing LEAPMUX messages', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('returns false when all messages are LEAPMUX notifications', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips USER message with deliveryError (agent never received it)', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.USER, undefined, 'connection lost'),
    ])).toBe(false)
  })

  it('skips trailing deliveryError messages and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.USER, undefined, 'connection lost'),
    ])).toBe(true)
  })

  it('skips trailing LEAPMUX context_cleared and stops at preceding TURN_END', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('treats LEAPMUX wrapper with [settings_changed, context_cleared] as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'settings_changed' }, { type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('treats LEAPMUX wrapper with [context_cleared, settings_changed] as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.LEAPMUX, wrapContent([{ type: 'context_cleared' }, { type: 'settings_changed' }])),
    ])).toBe(false)
  })

  // ---------------------------------------------------------------------
  // SYSTEM-role parity: agent-emitted metadata that migrated from LEAPMUX
  // to SYSTEM must produce the same isAgentWorking outcome.
  // ---------------------------------------------------------------------

  it.each([
    { type: 'compacting' },
    { type: 'rate_limit', rate_limit_info: { rateLimitType: 'unknown' } },
    { type: 'rate_limit_event', rate_limit_info: { rateLimitType: 'unknown' } },
    { type: 'compaction_start' },
    { type: 'compaction_end' },
    { type: 'auto_retry_start' },
    { type: 'auto_retry_end' },
    { type: 'extension_error', error: 'oops' },
    { type: 'extension_ui_request', method: 'notify' },
  ])('skips SYSTEM-roled non-progress event (%j) and finds preceding ASSISTANT', (payload) => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, rawContent(payload)),
    ])).toBe(true)
  })

  it.each([
    { method: 'thread/started', params: {} },
    { method: 'turn/started', params: {} },
    { method: 'thread/status/changed', params: { status: 'idle' } },
    { method: 'thread/name/updated', params: {} },
    { method: 'thread/tokenUsage/updated', params: {} },
    { method: 'skills/changed', params: {} },
    { method: 'thread/compacted', params: {} },
    { method: 'mcpServer/startupStatus/updated', params: {} },
    { method: 'account/rateLimits/updated', params: {} },
  ])('skips SYSTEM-roled Codex JSON-RPC notification (%j) and finds preceding ASSISTANT', (payload) => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, rawContent(payload)),
    ])).toBe(true)
  })

  it('skips Claude SYSTEM status message ({type:"system",subtype:"status"}) and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'system', subtype: 'status', status: 'compacting' })),
    ])).toBe(true)
  })

  it('skips Claude SYSTEM api_retry message and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'system', subtype: 'api_retry', attempt: 1 })),
    ])).toBe(true)
  })

  it('does NOT skip Claude SYSTEM init (other system subtype is real progress)', () => {
    // A bare assistant followed by a system init only — system init isn't a
    // notification subtype, so it counts as progress and the agent appears
    // to be working. Sanity check that the subtype filter isn't too broad.
    expect(isAgentWorking([
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'system', subtype: 'init', cwd: '/x' })),
    ])).toBe(true)
  })

  it('treats SYSTEM context_cleared as turn boundary (returns false)', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'context_cleared' })),
    ])).toBe(false)
  })

  it('treats SYSTEM wrapper containing context_cleared as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.SYSTEM, wrapContent([{ method: 'thread/tokenUsage/updated' }, { type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips trailing SYSTEM rate-limit notifications and falls through to TURN_END', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'rate_limit_event', rate_limit_info: {} })),
      makeMsg(MessageRole.SYSTEM, rawContent({ method: 'thread/tokenUsage/updated' })),
    ])).toBe(false)
  })

  it('returns true when last message is plain SYSTEM content (e.g. unknown notification)', () => {
    // A SYSTEM message whose inner type/method isn't recognized as
    // non-progress is treated as activity — better to over-show the
    // thinking indicator than to miss a real-progress signal.
    expect(isAgentWorking([
      makeMsg(MessageRole.SYSTEM, rawContent({ type: 'unknown_payload', some: 'data' })),
    ])).toBe(true)
  })

  // ---------------------------------------------------------------------
  // Notification-wrapper edge cases: an empty wrapper is what the
  // consolidator emits when every threaded message has been superseded.
  // It carries no progress signal and must not flip the indicator on.
  // ---------------------------------------------------------------------

  it('treats LEAPMUX wrapper with empty messages array as non-progress', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.LEAPMUX, wrapContent([])),
    ])).toBe(false)
  })

  it('treats SYSTEM wrapper with empty messages array as non-progress', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.TURN_END, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.SYSTEM, wrapContent([])),
    ])).toBe(false)
  })

  it('returns false when the only message is an empty LEAPMUX wrapper', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.LEAPMUX, wrapContent([])),
    ])).toBe(false)
  })

  // ---------------------------------------------------------------------
  // context_cleared boundary scope: only LEAPMUX/SYSTEM-roled messages
  // are emitted by the platform as turn boundaries. USER/ASSISTANT
  // payloads that happen to surface a top-level `type: "context_cleared"`
  // (e.g. a Pi `default`-handler echo of an unknown event) must NOT be
  // interpreted as a turn boundary — they carry user/agent content.
  // ---------------------------------------------------------------------

  it('does not treat USER message containing type:"context_cleared" as a turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER, rawContent({ type: 'context_cleared', content: 'literal user text' })),
    ])).toBe(true)
  })

  it('does not treat ASSISTANT message containing type:"context_cleared" as a turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT, rawContent({ type: 'context_cleared' })),
    ])).toBe(true)
  })
})

describe('shouldShowThinkingIndicator', () => {
  it('returns false for an inactive agent', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent({ status: AgentStatus.INACTIVE }),
      {},
      [makeMsg(MessageRole.USER)],
      '',
    )).toBe(false)
  })

  it('returns false when a control request is pending', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent(),
      {},
      [makeMsg(MessageRole.USER)],
      '',
      1,
    )).toBe(false)
  })

  it('returns true when streaming text is present', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent(),
      {},
      [],
      'streaming...',
    )).toBe(true)
  })

  it('uses codexTurnId for Codex instead of chat-history heuristics', () => {
    const sessionInfo: AgentSessionInfo = {}
    expect(shouldShowThinkingIndicator(
      makeAgent({ agentProvider: AgentProvider.CODEX }),
      sessionInfo,
      [makeMsg(MessageRole.ASSISTANT)],
      '',
    )).toBe(false)
  })

  it('shows thinking for Codex when codexTurnId is set', () => {
    const sessionInfo: AgentSessionInfo = { codexTurnId: 'turn-123' }
    expect(shouldShowThinkingIndicator(
      makeAgent({ agentProvider: AgentProvider.CODEX }),
      sessionInfo,
      [],
      '',
    )).toBe(true)
  })

  it('falls back to chat-history heuristics for non-Codex agents', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent({ agentProvider: AgentProvider.CLAUDE_CODE }),
      {},
      [makeMsg(MessageRole.USER)],
      '',
    )).toBe(true)
  })
})
