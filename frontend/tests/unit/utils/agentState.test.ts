import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { isAgentWorking, shouldShowThinkingIndicator } from '~/utils/agentState'
import { makeMessage, rawContent, wrapContent } from '../helpers/messageFactory'

function makeMsg(source: MessageSource, content?: Uint8Array, deliveryError?: string) {
  return makeMessage({ source, content, deliveryError })
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

  it('returns true when last message is USER source', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.USER),
    ])).toBe(true)
  })

  it('returns true when last message is AGENT source', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
    ])).toBe(true)
  })

  it('returns false when last message is a result divider', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.USER),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
    ])).toBe(false)
  })

  it('skips LEAPMUX message and finds result divider underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
    ])).toBe(false)
  })

  it('skips LEAPMUX settings_changed and finds preceding AGENT', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
    ])).toBe(true)
  })

  it('treats LEAPMUX context_cleared as turn boundary (returns false)', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips multiple trailing LEAPMUX messages', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('returns false when all messages are LEAPMUX notifications', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'settings_changed' }])),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips USER message with deliveryError (agent never received it)', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.USER, undefined, 'connection lost'),
    ])).toBe(false)
  })

  it('skips trailing deliveryError messages and finds preceding AGENT', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.USER, undefined, 'connection lost'),
    ])).toBe(true)
  })

  it('skips trailing LEAPMUX context_cleared and stops at preceding result divider', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('treats LEAPMUX wrapper with [settings_changed, context_cleared] as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'settings_changed' }, { type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('treats LEAPMUX wrapper with [context_cleared, settings_changed] as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.LEAPMUX, wrapContent([{ type: 'context_cleared' }, { type: 'settings_changed' }])),
    ])).toBe(false)
  })

  // ---------------------------------------------------------------------
  // AGENT-source parity: agent-emitted metadata persisted as AGENT must
  // produce the same isAgentWorking outcome as the LEAPMUX equivalents.
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
  ])('skips AGENT-source non-progress event (%j) and finds preceding AGENT', (payload) => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.AGENT, rawContent(payload)),
    ])).toBe(true)
  })

  it.each([
    { method: 'thread/started', params: {} },
    { method: 'turn/started', params: {} },
    { method: 'thread/status/changed', params: { status: 'idle' } },
    { method: 'thread/name/updated', params: {} },
    { method: 'thread/tokenUsage/updated', params: {} },
    { method: 'skills/changed', params: {} },
    { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
    { method: 'hook/started', params: { threadId: 'thread-1', turnId: 'turn-1' } },
    { method: 'hook/completed', params: { threadId: 'thread-1', turnId: 'turn-1' } },
    { method: 'thread/compacted', params: {} },
    { method: 'mcpServer/startupStatus/updated', params: {} },
    { method: 'account/rateLimits/updated', params: {} },
  ])('skips AGENT-source Codex JSON-RPC notification (%j) and finds preceding AGENT', (payload) => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.AGENT, rawContent(payload)),
    ])).toBe(true)
  })

  it('skips Claude system status message ({type:"system",subtype:"status"}) and finds preceding AGENT', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'system', subtype: 'status', status: 'compacting' })),
    ])).toBe(true)
  })

  it('skips Claude system api_retry message and finds preceding AGENT', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'system', subtype: 'api_retry', attempt: 1 })),
    ])).toBe(true)
  })

  it('does NOT skip Claude system init (other system subtype is real progress)', () => {
    // A bare assistant followed by a system init only — system init isn't a
    // notification subtype, so it counts as progress and the agent appears
    // to be working. Sanity check that the subtype filter isn't too broad.
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'system', subtype: 'init', cwd: '/x' })),
    ])).toBe(true)
  })

  it('treats AGENT-source wrapper containing context_cleared as turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT),
      makeMsg(MessageSource.AGENT, wrapContent([{ method: 'thread/tokenUsage/updated' }, { type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips trailing AGENT-source rate-limit notifications and falls through to result divider', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'rate_limit_event', rate_limit_info: {} })),
      makeMsg(MessageSource.AGENT, rawContent({ method: 'thread/tokenUsage/updated' })),
    ])).toBe(false)
  })

  it('returns true when last message is plain AGENT content (e.g. unknown notification)', () => {
    // An AGENT message whose inner type/method isn't recognized as
    // non-progress is treated as activity — better to over-show the
    // thinking indicator than to miss a real-progress signal.
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'unknown_payload', some: 'data' })),
    ])).toBe(true)
  })

  // ---------------------------------------------------------------------
  // Notification-wrapper edge cases: an empty wrapper is what the
  // consolidator emits when every threaded message has been superseded.
  // It carries no progress signal and must not flip the indicator on.
  // ---------------------------------------------------------------------

  it('treats LEAPMUX wrapper with empty messages array as non-progress', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.LEAPMUX, wrapContent([])),
    ])).toBe(false)
  })

  it('treats AGENT-source wrapper with empty messages array as non-progress', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageSource.AGENT, wrapContent([])),
    ])).toBe(false)
  })

  it('returns false when the only message is an empty LEAPMUX wrapper', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.LEAPMUX, wrapContent([])),
    ])).toBe(false)
  })

  // ---------------------------------------------------------------------
  // context_cleared boundary scope: only notification-thread wrapper rows
  // are emitted by the platform as turn boundaries. USER/AGENT plain
  // payloads that happen to surface a top-level `type: "context_cleared"`
  // (e.g. a Pi `default`-handler echo of an unknown event) must NOT be
  // interpreted as a turn boundary — they carry user/agent content.
  // ---------------------------------------------------------------------

  it('does not treat USER message containing type:"context_cleared" as a turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.USER, rawContent({ type: 'context_cleared', content: 'literal user text' })),
    ])).toBe(true)
  })

  it('does not treat AGENT message containing type:"context_cleared" as a turn boundary', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'context_cleared' })),
    ])).toBe(true)
  })
})

describe('shouldShowThinkingIndicator', () => {
  it('returns false for an inactive agent', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent({ status: AgentStatus.INACTIVE }),
      {},
      [makeMsg(MessageSource.USER)],
      '',
    )).toBe(false)
  })

  it('returns false when a control request is pending', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent(),
      {},
      [makeMsg(MessageSource.USER)],
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
      [makeMsg(MessageSource.AGENT)],
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
      [makeMsg(MessageSource.USER)],
      '',
    )).toBe(true)
  })
})
