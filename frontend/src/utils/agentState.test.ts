import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent, wrapContent } from '~/test-support/messageFactory'
import { isAgentWorking, shouldShowThinkingIndicator } from '~/utils/agentState'

function makeMsg(source: MessageSource, content?: Uint8Array) {
  return makeMessage({ source, content })
}

function makeAgent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    $typeName: 'leapmux.v1.AgentInfo' as const,
    id: 'agent-1',
    workspaceId: 'ws-1',
    title: 'Agent Olivia',
    status: AgentStatus.ACTIVE,
    workingDir: '/tmp',
    agentSessionId: '',
    homeDir: '/tmp',
    workerId: 'worker-1',
    createdAt: '2025-01-15T10:00:00.000Z',
    gitStatus: undefined,
    agentProvider: AgentProvider.CLAUDE_CODE,
    // Model/effort/permission mode and every provider-specific axis now live as
    // option groups; the heuristics under test read none of them, so an empty
    // catalog suffices.
    optionGroups: [],
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

  it('returns false when the last message has no registered provider plugin (UNSPECIFIED)', () => {
    // An UNSPECIFIED-provider message classifies as `unsupported_provider`: we
    // can't interpret it, so it carries no "still working" signal. Without this
    // it would fall through to `return true` and pin the thinking indicator on.
    expect(isAgentWorking([
      makeMessage({ source: MessageSource.USER }),
      makeMessage({ source: MessageSource.AGENT, content: rawContent({ type: 'result' }), agentProvider: AgentProvider.UNSPECIFIED }),
    ])).toBe(false)
  })

  it('returns false for an unsupported (version-skew) provider message, not a stuck spinner', () => {
    // A provider enum the frontend has no plugin for (backend/frontend skew) makes
    // every message `unsupported_provider`; the agent must not read as perpetually
    // working just because we can't classify its turn-end envelope.
    expect(isAgentWorking([
      makeMessage({ source: MessageSource.AGENT, content: rawContent({ type: 'whatever' }), agentProvider: 999 as AgentProvider }),
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

  describe('a Pi transcript', () => {
    const pi = (content: Uint8Array) =>
      makeMessage({ source: MessageSource.AGENT, content, agentProvider: AgentProvider.PI })
    const agentEnd = (extra: Record<string, unknown> = {}) =>
      pi(rawContent({ type: 'agent_end', messages: [{ role: 'assistant', stopReason: 'stop' }], ...extra }))

    it('skips a trailing hidden row and stops at the divider before it', () => {
      // A build before the worker dropped agent_settled persisted it as a row.
      // It classifies `hidden`, draws nothing, and must not report "working".
      expect(isAgentWorking([
        makeMsg(MessageSource.USER),
        agentEnd(),
        pi(rawContent({ type: 'agent_settled' })),
      ])).toBe(false)
    })

    it('still reports working for a visible assistant reply', () => {
      expect(isAgentWorking([
        makeMsg(MessageSource.USER),
        pi(rawContent({ type: 'message_end', message: { role: 'assistant', content: [{ type: 'text', text: 'hi' }] } })),
      ])).toBe(true)
    })

    // willRetry is the only difference between these two: a divider Pi will
    // retry past must keep the thinking indicator up for the whole backoff,
    // where nothing streams and no other row arrives.
    const failed = [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }]

    it('keeps working through a divider for a run Pi will retry', () => {
      expect(isAgentWorking([
        makeMsg(MessageSource.USER),
        agentEnd({ willRetry: true, messages: failed }),
      ])).toBe(true)
    })

    it('stops at the divider once Pi will not retry', () => {
      expect(isAgentWorking([
        makeMsg(MessageSource.USER),
        agentEnd({ willRetry: false, messages: failed }),
      ])).toBe(false)
    })

    it('stops at the divider when Pi omits willRetry (an older build)', () => {
      expect(isAgentWorking([
        makeMsg(MessageSource.USER),
        agentEnd({ messages: failed }),
      ])).toBe(false)
    })
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

  it('does NOT skip a visible system subtype (real progress)', () => {
    // A system subtype that is neither hidden nor status/api_retry renders as a
    // notification and counts as progress. This confirms that the subtype
    // filter is not too broad.
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'system', subtype: 'compact_boundary' })),
    ])).toBe(true)
  })

  it('skips Claude system init, which the transcript hides', () => {
    // `init` is in Claude's HIDDEN_SYSTEM_SUBTYPES, so the row draws nothing.
    // A transcript holding only folded-away rows shows no work in progress.
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'system', subtype: 'init', cwd: '/x' })),
    ])).toBe(false)
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

  // The user-visible end of the Pi auto-retry rule. During the backoff nothing
  // streams and the registry knows nothing about the tab. The divider's own
  // `turnContinues` is therefore the only thing that keeps the indicator up.
  it('keeps the indicator lit for a Pi divider Pi will retry past', () => {
    const retrying = makeMessage({
      source: MessageSource.AGENT,
      content: rawContent({
        type: 'agent_end',
        willRetry: true,
        messages: [{ role: 'assistant', stopReason: 'error', errorMessage: 'overloaded' }],
      }),
      agentProvider: AgentProvider.PI,
    })
    expect(shouldShowThinkingIndicator(
      makeAgent({ agentProvider: AgentProvider.PI }),
      {},
      [makeMsg(MessageSource.USER), retrying],
      '',
    )).toBe(true)
  })

  it('clears the indicator once a Pi turn really ends', () => {
    const ended = makeMessage({
      source: MessageSource.AGENT,
      content: rawContent({
        type: 'agent_end',
        willRetry: false,
        messages: [{ role: 'assistant', stopReason: 'stop' }],
      }),
      agentProvider: AgentProvider.PI,
    })
    expect(shouldShowThinkingIndicator(
      makeAgent({ agentProvider: AgentProvider.PI }),
      {},
      [makeMsg(MessageSource.USER), ended],
      '',
    )).toBe(false)
  })

  it('forces visible when this tab has active work, even with no streaming text', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent(),
      {},
      [],
      '',
      0,
      'active',
    )).toBe(true)
  })

  it('active work is ignored when status is not ACTIVE', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent({ status: AgentStatus.INACTIVE }),
      {},
      [],
      '',
      0,
      'active',
    )).toBe(false)
  })

  it('active work is ignored when a control request is pending', () => {
    expect(shouldShowThinkingIndicator(
      makeAgent(),
      {},
      [],
      '',
      1,
      'active',
    )).toBe(false)
  })

  // The reason this is a tri-state rather than a count. A finished subagent's
  // transcript often does NOT end with the closing divider -- an interrupt
  // notice, a trailing tool result, or a divider write that failed all leave
  // the message heuristic reporting "working". The registry row is the
  // authoritative record of that subagent's life, so it wins.
  describe('a finished subagent', () => {
    // A user message is a progress signal, so isAgentWorking says "working".
    const interruptedTail = [
      makeMsg(MessageSource.USER, rawContent({
        type: 'user',
        message: { role: 'user', content: [{ type: 'text', text: '[Request interrupted by user]' }] },
        parent_tool_use_id: 'toolu_1',
      })),
    ]

    it('hides the indicator although the transcript looks busy', () => {
      expect(shouldShowThinkingIndicator(makeAgent(), {}, interruptedTail, '', 0, 'unknown'))
        .toBe(true)
      expect(shouldShowThinkingIndicator(makeAgent(), {}, interruptedTail, '', 0, 'finished'))
        .toBe(false)
    })

    // The row outranks the message history, NOT live evidence of a turn. A
    // steerable subagent (Codex re-registers a collab child on a later tool
    // call) takes a new message after its row went final, and the row never
    // reopens -- so the turn the user just started must still show the indicator,
    // and must still offer Interrupt, which the same predicate gates.
    it('shows it again when a finished subagent starts streaming a new turn', () => {
      expect(shouldShowThinkingIndicator(makeAgent(), {}, [], 'half a sentence', 0, 'finished'))
        .toBe(true)
    })

    it('still hides it for a finished row with no live turn', () => {
      expect(shouldShowThinkingIndicator(makeAgent(), {}, interruptedTail, '', 0, 'finished'))
        .toBe(false)
    })
  })

  // 'unknown' must not assert a negative: a tab the registry has no row for
  // still falls through to the heuristic, which is what a plain root turn uses.
  it('falls through to the message heuristic when the registry cannot answer', () => {
    const working = [makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'working' }))]
    expect(shouldShowThinkingIndicator(makeAgent(), {}, working, '', 0, 'unknown')).toBe(true)
  })
})

// A subagent RUN is closed by the worker's subagent-end divider (written when
// that subagent's registry row reaches a final status). It is a notification, so
// without an explicit stop the backwards scan would step over it, reach the
// subagent's last real message, and report a finished subagent as still working.
//
// One divider per run, not per transcript: Claude restarts a finished subagent
// when the parent messages it, so a transcript can hold several with work
// between them.
describe('isAgentWorking: the subagent-end divider', () => {
  const ended = (status: string) =>
    makeMsg(MessageSource.LEAPMUX, rawContent({ type: 'subagent_ended', status }))

  it('reports not working once the divider has landed', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.USER, rawContent({ content: 'go' })),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'working' })),
      ended('completed'),
    ])).toBe(false)
  })

  it('reports not working for every final status', () => {
    for (const status of ['completed', 'failed', 'stopped', 'interrupted']) {
      expect(isAgentWorking([
        makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'working' })),
        ended(status),
      ])).toBe(false)
    }
  })

  it('still reports working when the subagent spoke after the divider', () => {
    // This IS a shape the worker produces: a revived subagent appends below the
    // divider that closed its previous run. The scan answers from the LAST
    // message rather than from "a divider exists somewhere", which is what makes
    // the revive need no change here.
    expect(isAgentWorking([
      ended('completed'),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'more' })),
    ])).toBe(true)
  })

  // The full revive shape end to end: first run, its divider, the message the
  // parent sent, and the restarted run's output.
  it('reports working again through a revive, and finished after the second divider', () => {
    const revived = [
      makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'first run' })),
      ended('completed'),
      makeMsg(MessageSource.USER, rawContent({ content: 'keep going' })),
      makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'second run' })),
    ]
    expect(isAgentWorking(revived)).toBe(true)
    expect(isAgentWorking([...revived, ended('completed')])).toBe(false)
  })

  it('reports working while the subagent is mid-flight', () => {
    expect(isAgentWorking([
      makeMsg(MessageSource.AGENT, rawContent({ type: 'text', text: 'working' })),
    ])).toBe(true)
  })
})
