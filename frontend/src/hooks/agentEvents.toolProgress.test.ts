import type { AgentControlRequest, AgentStatusChange } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
/// <reference types="vitest/globals" />
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { applyNotificationMetadata, dropFinishedToolProgress, handleAgentInactive, handleAgentMessage, handleAgentSessionInfo, handleControlRequest, handleResultDivider, wireRunningToolToUpdate } from '~/hooks/agentEvents'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import '~/components/chat/providers'

const RETRY_WIRE = {
  attempt: 2,
  max_retries: 5,
  retry_delay_ms: 4000,
  error_status: 529,
  error_category: 'overloaded',
}
const RETRY = { attempt: 2, maxRetries: 5, retryDelayMs: 4000, errorStatus: 529, errorCategory: 'overloaded' }

/** The provider session every span below belongs to, unless a case states another. */
const SESSION = 'sess-1'

function span(spanId: string, agentSessionId: string = SESSION): MessageSpanIdentity {
  return { spanId, agentSessionId }
}

function agentMessage(content: unknown, overrides: Partial<{ spanId: string, agentSessionId: string, source: MessageSource, agentProvider: AgentProvider }> = {}) {
  return create(AgentChatMessageSchema, {
    id: 'm1',
    source: overrides.source ?? MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    agentProvider: overrides.agentProvider ?? AgentProvider.CLAUDE_CODE,
    spanId: overrides.spanId ?? '',
    agentSessionId: overrides.agentSessionId ?? SESSION,
  })
}

function sessionInfoMessage(info: unknown) {
  return agentMessage({ type: 'agent_session_info', info })
}

describe('wireRunningToolToUpdate', () => {
  it('translates a heartbeat payload', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 }))
      .toEqual({ ...span('toolu_A'), elapsedSeconds: 30 })
  })

  // The store keys an entry by the session and the span together, so the
  // translation must carry the session the producer stated.
  it('reads the agent session the payload states', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: 'other-session', elapsed_seconds: 30 })?.agentSessionId)
      .toBe('other-session')
  })

  // A command can produce no output while it runs.
  // Its empty output tail remains a real value, so check the key's presence.
  it('reads the output tail and its truncation flag', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, output_tail: 'building...\n', output_truncated: true }))
      .toEqual({ ...span('toolu_A'), outputTail: 'building...\n', outputTruncated: true })
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, output_tail: '' }))
      .toEqual({ ...span('toolu_A'), outputTail: '', outputTruncated: false })
  })

  // A heartbeat supplies no output tail. Retain the tail from the last output frame because the
  // two event types report different fields.

  it('leaves the tail off an update that states none', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, elapsed_seconds: 30 }))
      .toEqual({ ...span('toolu_A'), elapsedSeconds: 30 })
  })

  // An absent session becomes an empty string, as it does in messageSpanKey.
  // The progress update and its row therefore use the same key.
  it('reads a missing or unusable agent session as an empty string', () => {
    for (const value of [undefined, null, 42, {}]) {
      expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: value, elapsed_seconds: 30 })?.agentSessionId)
        .toBe('')
    }
  })

  it('translates a subagent-retry payload, every field camel-cased', () => {
    expect(wireRunningToolToUpdate({
      span_id: 'toolu_A',
      agent_session_id: SESSION,
      tool_name: 'Agent',
      subagent_type: 'Explore',
      retry: RETRY_WIRE,
    })).toEqual({ ...span('toolu_A'), retry: RETRY })
  })

  // The tool row supplies the tool name and subagent type. The progress badge uses neither
  // field, so the translation omits them.

  it('drops the tool name and the subagent type, which no reader wants', () => {
    const update = wireRunningToolToUpdate({
      span_id: 'toolu_A',
      agent_session_id: SESSION,
      tool_name: 'Agent',
      subagent_type: 'Explore',
      elapsed_seconds: 30,
    })
    expect(update).toEqual({ ...span('toolu_A'), elapsedSeconds: 30 })
  })

  it('carries an explicit null retry through as null -- the resolved signal', () => {
    const update = wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Agent', retry: null })
    expect(update).toEqual({ ...span('toolu_A'), retry: null })
  })

  it('leaves `retry` absent when the payload omits it, so a heartbeat cannot clear one', () => {
    const update = wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 })
    expect(update && 'retry' in update).toBe(false)
  })

  it('rejects a payload that identifies no span', () => {
    for (const value of [undefined, null, 'x', 42, {}, { span_id: '' }, { span_id: 7 }])
      expect(wireRunningToolToUpdate(value)).toBeUndefined()
  })

  it('drops an elapsed time that is not a usable number', () => {
    // Without validation, NaN and Infinity reach formatDuration and produce an invalid label on the card.
    // A negative elapsed time is not a duration.
    for (const elapsed of [Number.NaN, Number.POSITIVE_INFINITY, -5, '30', null]) {
      const update = wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: elapsed })
      expect(update && 'elapsedSeconds' in update).toBe(false)
    }
  })

  // An unreadable retry leaves the key absent and retains the last attempt. Only explicit null
  // clears it. A partial badge must not display an invented 0/0 count.

  it('omits a retry it cannot read, so a live badge survives an unknown shape', () => {
    for (const retry of [{ attempt: 2 }, { max_retries: 5 }, { attempt: '2', max_retries: 5 }, 'x', 42, [], undefined]) {
      const update = wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, retry })
      expect(update && 'retry' in update).toBe(false)
    }
  })

  it('reads only an explicit null as the resolved signal', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, retry: null })?.retry).toBeNull()
  })

  it('forwards a zero elapsed time -- the badge, not this, decides it shows nothing', () => {
    // A zero elapsed time is still a supplied value. The translator must preserve it even when a
    // normal heartbeat reports a positive value.

    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 0 }))
      .toEqual({ ...span('toolu_A'), elapsedSeconds: 0 })
  })

  it('supplies defaults for the retry fields the agent left out', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', agent_session_id: SESSION, retry: { attempt: 1, max_retries: 3 } })?.retry)
      .toEqual({ attempt: 1, maxRetries: 3, retryDelayMs: 0, errorStatus: null, errorCategory: '' })
  })
})

describe('handleAgentSessionInfo running_tool', () => {
  function stores() {
    return { agentSessionStore: createAgentSessionStore(), chatStore: createChatStore() }
  }

  it('applies a running_tool payload to the chat store and consumes the message', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = sessionInfoMessage({ running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), s)).toBe(true)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  // The worker does not persist this session-info message.
  // The handler consumes it without creating a transcript row.
  it('never adds the payload to the message window', () => {
    createRoot((dispose) => {
      installTestBridge({ workspaceId: 'tool-progress-messages' })
      const s = { ...stores(), ...createTestTabStores('tool-progress-messages'), getActiveWorkspaceId: () => 'tool-progress-messages' }
      const msg = sessionInfoMessage({
        thinking_tokens: 42,
        output_bytes: 2048,
        output_bytes_minimum: true,
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 },
      })
      handleAgentMessage('a1', msg, s, 'live')
      expect(s.chatStore.getMessages('a1')).toHaveLength(0)
      expect(s.chatStore.getMessageVersion('a1')).toBe(0)
      handleAgentMessage('a1', agentMessage({ type: 'assistant', message: { content: [{ type: 'text', text: 'A real transcript row.' }] } }), s, 'live')
      expect(s.chatStore.getMessages('a1')).toHaveLength(1)
      expect(s.chatStore.getMessageVersion('a1')).toBeGreaterThan(0)
      dispose()
    })
  })

  it('merges a retry over a heartbeat without rewinding the elapsed time', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Agent', elapsed_seconds: 90 },
      })), s)
      // The retry family reports elapsed_time_seconds 0, which the worker omits.
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Agent', subagent_type: 'Explore', retry: RETRY_WIRE },
      })), s)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))).toEqual({
        elapsedSeconds: 90,
        retry: RETRY,
      })
      dispose()
    })
  })

  it('clears the retry on the resolved signal and keeps the elapsed time', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Agent', elapsed_seconds: 90, retry: RETRY_WIRE },
      })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Agent', retry: null },
      })), s)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))?.retry).toBeUndefined()
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // The store uses the session ID and span ID together.
  // A heartbeat must not update another session that reuses the span ID.
  it('keeps the entries of two provider sessions apart', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: 'sess-1', elapsed_seconds: 30 },
      })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', agent_session_id: 'sess-2', elapsed_seconds: 90 },
      })), s)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A', 'sess-1'))?.elapsedSeconds).toBe(30)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A', 'sess-2'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  it('ignores a payload whose running_tool identifies no span', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = sessionInfoMessage({ running_tool: { agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), s)).toBe(true)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))).toBeUndefined()
      dispose()
    })
  })

  it('writes nothing for a running_tool the translation rejects', () => {
    createRoot((dispose) => {
      const s = stores()
      s.chatStore.applyToolProgress('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      for (const value of [null, 'x', 42, {}])
        handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ running_tool: value })), s)
      // A malformed payload must preserve the current progress badge.

      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  it('still applies the scalar keys of a payload that carries both', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        total_cost_usd: 1.5,
        running_tool: { span_id: 'toolu_A', agent_session_id: SESSION, tool_name: 'Bash', elapsed_seconds: 30 },
      })), s)
      expect(s.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1.5)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))?.elapsedSeconds).toBe(30)
      dispose()
    })
  })
})

// The per-span cleanup handles a normal tool result. Lifecycle cleanup removes remaining spans
// when their result rows never arrive.

describe('tool progress is cleared at every turn and agent boundary', () => {
  const WS = 'ws-1'

  /** Supply the stores that each boundary handler requires, with two spans that run. */
  function boundaryStores() {
    const harness = installTestBridge({ workspaceId: WS })
    void harness
    const tabs = createTestTabStores(WS)
    const chatStore = createChatStore()
    chatStore.applyToolProgress('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
    chatStore.applyToolProgress('a1', { ...span('toolu_B'), elapsedSeconds: 60 })
    const agentSessionStore = createAgentSessionStore()
    // The thinking counter and tool badges both describe the live turn.
    // Seed both so each boundary test checks that the handler clears them together.
    agentSessionStore.applyProgress('a1', {
      revision: 1,
      thinkingTokens: 500,
      output: { bytes: 2048, minimum: true },
    })
    return {
      agentSessionStore,
      chatStore,
      controlStore: createControlStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS as string | null,
      tabs,
    }
  }

  function running(chatStore: ReturnType<typeof createChatStore>) {
    return [chatStore.getToolProgress('a1', span('toolu_A')), chatStore.getToolProgress('a1', span('toolu_B'))]
      .filter(Boolean)
  }

  /**
   * Each turn-end boundary must clear both live indicators or retain both.
   * A lost connection can omit the worker's explicit counter clear.
   * The lifecycle handler must clear every live field. Otherwise, an indicator stays stale for the rest of the session.
   * A handler that calls only clearToolProgress fails these checks.
   * A control request clears neither indicator. Its separate case checks that rule.
   */
  function expectNothingLive(s: ReturnType<typeof boundaryStores>) {
    expect(running(s.chatStore)).toHaveLength(0)
    expect(s.agentSessionStore.getProgress('a1').thinkingTokens).toBeUndefined()
    expect(s.agentSessionStore.getProgress('a1').output).toBeUndefined()
  }

  it('the turn-end result divider clears every live indicator', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      expect(running(s.chatStore)).toHaveLength(2)
      const msg = agentMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a1', msg, parseMessageContent(msg), s, 'live')
      expectNothingLive(s)
      dispose()
    })
  })

  it('the agent going INACTIVE clears every live indicator', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      handleAgentInactive('a1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'live', s)
      expectNothingLive(s)
      dispose()
    })
  })

  it('a context clear clears every live indicator', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      const msg = agentMessage({ type: 'context_cleared' }, { source: MessageSource.LEAPMUX })
      applyNotificationMetadata('a1', msg, parseMessageContent(msg), s, 'live')
      expectNothingLive(s)
      dispose()
    })
  })

  /**
   * A control request does not infer a counter lifecycle. The Worker sends the
   * progress reset before it sends the request.
   */
  it('a control request keeps live state until the Worker reset arrives', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      const req = {
        requestId: 'r1',
        agentId: 'a1',
        agentProvider: AgentProvider.REASONIX,
        payload: new TextEncoder().encode(JSON.stringify({ method: 'x' })),
      } as unknown as AgentControlRequest
      handleControlRequest('a1', req, 'live', s)
      expect(s.controlStore.getRequests('a1')[0]?.agentProvider).toBe(AgentProvider.REASONIX)
      expect(running(s.chatStore)).toHaveLength(2)
      expect(s.chatStore.getToolProgress('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      expect(s.agentSessionStore.getProgress('a1').thinkingTokens).toBe(500)
      expect(s.agentSessionStore.getProgress('a1').output?.bytes).toBe(2048)
      dispose()
    })
  })

  it('closing the agent reclaims its spans', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      s.chatStore.forgetAgent('a1')
      expect(running(s.chatStore)).toHaveLength(0)
      dispose()
    })
  })

  it('a boundary on ONE agent leaves another agent\'s spans alone', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      s.chatStore.applyToolProgress('a2', { ...span('toolu_A'), elapsedSeconds: 30 })
      const msg = agentMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a1', msg, parseMessageContent(msg), s, 'live')
      expect(running(s.chatStore)).toHaveLength(0)
      expect(s.chatStore.getToolProgress('a2', span('toolu_A'))?.elapsedSeconds).toBe(30)
      dispose()
    })
  })
})

describe('dropFinishedToolProgress', () => {
  function seeded() {
    const chatStore = createChatStore()
    chatStore.applyToolProgress('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
    return chatStore
  }

  it('drops the span when its tool_result row lands', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      // A Claude tool_result arrives as a `user` envelope carrying the block.
      const msg = agentMessage(
        { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_A', content: 'ok' }] } },
        { spanId: 'toolu_A' },
      )
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))).toBeUndefined()
      dispose()
    })
  })

  // Resolve the payload before reading its result role. A result that only the supplement
  // establishes must still clear its progress.

  it('drops the span from the resolved result role', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      const msg = agentMessage(
        { id: 'native-result', role: 'result', seq: 3, content: { sessionUpdate: 'tool_call_update', toolCallId: 'toolu_A', status: 'completed', content: [] } },
        { spanId: 'toolu_A', agentProvider: AgentProvider.OPENCODE },
      )
      const parsed = parseMessageContent(msg)
      expect(parsed.parentObject).not.toHaveProperty('sessionUpdate')
      dropFinishedToolProgress('a1', msg, parsed, chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))).toBeUndefined()
      dispose()
    })
  })

  // The cleanup key must match the progress key. Another native session can reuse the span ID
  // and must retain its own progress.

  it('drops nothing for a result row of another provider session', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      const content = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_A', content: 'ok' }] } }
      const stranger = agentMessage(content, { spanId: 'toolu_A', agentSessionId: 'other-session' })
      dropFinishedToolProgress('a1', stranger, parseMessageContent(stranger), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))?.elapsedSeconds).toBe(30)

      const own = agentMessage(content, { spanId: 'toolu_A' })
      dropFinishedToolProgress('a1', own, parseMessageContent(own), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))).toBeUndefined()
      dispose()
    })
  })

  it('leaves the span alone for the tool_use row that OPENED it', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      const msg = agentMessage(
        { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'toolu_A', name: 'Bash', input: {} }] } },
        { spanId: 'toolu_A' },
      )
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))?.elapsedSeconds).toBe(30)
      dispose()
    })
  })

  it('leaves the span alone for a row that carries no span at all', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      const msg = agentMessage({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } })
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))?.elapsedSeconds).toBe(30)
      dispose()
    })
  })

  it('drops only the span whose result landed', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      chatStore.applyToolProgress('a1', { ...span('toolu_B'), elapsedSeconds: 60 })
      const msg = agentMessage(
        { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_A', content: 'ok' }] } },
        { spanId: 'toolu_A' },
      )
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', span('toolu_A'))).toBeUndefined()
      expect(chatStore.getToolProgress('a1', span('toolu_B'))?.elapsedSeconds).toBe(60)
      dispose()
    })
  })
})
