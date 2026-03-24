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
    title: 'Agent 1',
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
    codexSandboxPolicy: '',
    codexNetworkAccess: '',
    codexCollaborationMode: '',
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

  it('returns false when last message is a turn-end RESULT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
    ])).toBe(false)
  })

  it('skips settings_changed RESULT and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
    ])).toBe(true)
  })

  it('skips context_cleared RESULT and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'context_cleared' })),
    ])).toBe(true)
  })

  it('skips multiple trailing notification RESULTs and finds preceding non-RESULT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'context_cleared' })),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
    ])).toBe(true)
  })

  it('skips notification RESULTs but finds turn-end RESULT underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
    ])).toBe(false)
  })

  it('returns false when all messages are notification RESULTs', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'context_cleared' })),
    ])).toBe(false)
  })

  it('does not skip interrupted RESULT (it is a genuine turn end)', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'interrupted' })),
    ])).toBe(false)
  })

  it('handles RESULT with unparseable content as a turn end', () => {
    const badContent = new TextEncoder().encode('not valid json')
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, badContent),
    ])).toBe(false)
  })

  it('handles RESULT with empty content as a turn end', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT),
    ])).toBe(false)
  })

  it('skips LEAPMUX message and finds turn-end RESULT underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
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
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
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
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.USER, undefined, 'connection lost'),
    ])).toBe(false)
  })

  it('skips trailing deliveryError messages and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.USER, undefined, 'connection lost'),
    ])).toBe(true)
  })

  it('skips mixed LEAPMUX and notification RESULT messages', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, rawContent({ type: 'result', subtype: 'turn_result' })),
      makeMsg(MessageRole.RESULT, rawContent({ type: 'settings_changed' })),
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
