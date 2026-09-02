import type { AgentControlRequest, AgentStatusChange } from '~/generated/proto/leapmux/v1/agent_pb'
/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { applyNotificationMetadata, dropFinishedToolProgress, handleAgentInactive, handleAgentSessionInfo, handleControlRequest, handleResultDivider, wireRunningToolToUpdate } from '~/hooks/agentEvents'
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

function agentMessage(content: unknown, overrides: Partial<{ spanId: string, source: MessageSource }> = {}) {
  return {
    id: 'm1',
    source: overrides.source ?? MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId: overrides.spanId ?? '',
  } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
}

function sessionInfoMessage(info: unknown) {
  return agentMessage({ type: 'agent_session_info', info })
}

describe('wireRunningToolToUpdate', () => {
  it('translates a heartbeat payload', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 }))
      .toEqual({ spanId: 'toolu_A', elapsedSeconds: 30 })
  })

  it('translates a subagent-retry payload, every field camel-cased', () => {
    expect(wireRunningToolToUpdate({
      span_id: 'toolu_A',
      tool_name: 'Agent',
      subagent_type: 'Explore',
      retry: RETRY_WIRE,
    })).toEqual({ spanId: 'toolu_A', retry: RETRY })
  })

  // The badge renders neither the tool's name nor the subagent's type -- the
  // card already has both from the tool_use row -- so the translation drops them
  // rather than storing a value nothing can observe.
  it('drops the tool name and the subagent type, which no reader wants', () => {
    const update = wireRunningToolToUpdate({
      span_id: 'toolu_A',
      tool_name: 'Agent',
      subagent_type: 'Explore',
      elapsed_seconds: 30,
    })
    expect(update).toEqual({ spanId: 'toolu_A', elapsedSeconds: 30 })
  })

  it('carries an explicit null retry through as null -- the resolved signal', () => {
    const update = wireRunningToolToUpdate({ span_id: 'toolu_A', tool_name: 'Agent', retry: null })
    expect(update).toEqual({ spanId: 'toolu_A', retry: null })
  })

  it('leaves `retry` absent when the payload omits it, so a heartbeat cannot clear one', () => {
    const update = wireRunningToolToUpdate({ span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 })
    expect(update && 'retry' in update).toBe(false)
  })

  it('rejects a payload that identifies no span', () => {
    for (const value of [undefined, null, 'x', 42, {}, { span_id: '' }, { span_id: 7 }])
      expect(wireRunningToolToUpdate(value)).toBeUndefined()
  })

  it('drops an elapsed time that is not a usable number', () => {
    // NaN and Infinity would reach formatDuration and render as "NaNs" on the
    // card; a negative elapsed time is not a duration at all.
    for (const elapsed of [Number.NaN, Number.POSITIVE_INFINITY, -5, '30', null]) {
      const update = wireRunningToolToUpdate({ span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: elapsed })
      expect(update && 'elapsedSeconds' in update).toBe(false)
    }
  })

  // A retry this cannot read leaves the key OFF the update, so a live badge
  // keeps its last attempt. Only an explicit null clears one. A shape change on
  // the wire must not erase what the user is looking at, and a partial badge
  // would read "Retrying 0/0".
  it('omits a retry it cannot read, so a live badge survives an unknown shape', () => {
    for (const retry of [{ attempt: 2 }, { max_retries: 5 }, { attempt: '2', max_retries: 5 }, 'x', 42, [], undefined]) {
      const update = wireRunningToolToUpdate({ span_id: 'toolu_A', retry })
      expect(update && 'retry' in update).toBe(false)
    }
  })

  it('reads only an explicit null as the resolved signal', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', retry: null })?.retry).toBeNull()
  })

  it('forwards a zero elapsed time -- the badge, not this, decides it shows nothing', () => {
    // The worker never sends 0 for a heartbeat (the first tick is 30), but the
    // translation must not silently swallow a number the agent did report.
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 0 }))
      .toEqual({ spanId: 'toolu_A', elapsedSeconds: 0 })
  })

  it('supplies defaults for the retry fields the agent left out', () => {
    expect(wireRunningToolToUpdate({ span_id: 'toolu_A', retry: { attempt: 1, max_retries: 3 } })?.retry)
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
      const msg = sessionInfoMessage({ running_tool: { span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), s)).toBe(true)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  // It is ephemeral: the whole point of intercepting it is that it never becomes
  // a timeline row.
  it('never adds the payload to the message window', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = sessionInfoMessage({ running_tool: { span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 } })
      handleAgentSessionInfo('a1', parseMessageContent(msg), s)
      expect(s.chatStore.getMessages('a1')).toHaveLength(0)
      dispose()
    })
  })

  it('merges a retry over a heartbeat without rewinding the elapsed time', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', tool_name: 'Agent', elapsed_seconds: 90 },
      })), s)
      // The retry family reports elapsed_time_seconds 0, which the worker omits.
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', tool_name: 'Agent', subagent_type: 'Explore', retry: RETRY_WIRE },
      })), s)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toEqual({
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
        running_tool: { span_id: 'toolu_A', tool_name: 'Agent', elapsed_seconds: 90, retry: RETRY_WIRE },
      })), s)
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        running_tool: { span_id: 'toolu_A', tool_name: 'Agent', retry: null },
      })), s)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')?.retry).toBeUndefined()
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  it('ignores a payload whose running_tool identifies no span', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = sessionInfoMessage({ running_tool: { tool_name: 'Bash', elapsed_seconds: 30 } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), s)).toBe(true)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toBeUndefined()
      dispose()
    })
  })

  it('writes nothing for a running_tool the translation rejects', () => {
    createRoot((dispose) => {
      const s = stores()
      s.chatStore.applyToolProgress('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
      for (const value of [null, 'x', 42, {}])
        handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({ running_tool: value })), s)
      // The entry that WAS running is left exactly as it was -- a malformed
      // payload must not clear a live badge.
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  it('still applies the scalar keys of a payload that carries both', () => {
    createRoot((dispose) => {
      const s = stores()
      handleAgentSessionInfo('a1', parseMessageContent(sessionInfoMessage({
        total_cost_usd: 1.5,
        running_tool: { span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 },
      })), s)
      expect(s.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1.5)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')?.elapsedSeconds).toBe(30)
      dispose()
    })
  })
})

// The per-span drop above covers the ordinary end of a tool. These four cover
// the boundaries that reclaim EVERY span -- the backstop for a tool whose result
// row never arrives, which is why no provider sends an end message.
describe('tool progress is cleared at every turn and agent boundary', () => {
  const WS = 'ws-1'

  /** The stores bag each boundary handler takes, with two spans already running. */
  function boundaryStores() {
    const harness = installTestBridge({ workspaceId: WS })
    void harness
    const tabs = createTestTabStores(WS)
    const chatStore = createChatStore()
    chatStore.applyToolProgress('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    chatStore.applyToolProgress('a1', { spanId: 'toolu_B', elapsedSeconds: 60 })
    const agentSessionStore = createAgentSessionStore()
    // The thinking counter is the OTHER live per-turn indicator, and it has to
    // go at exactly the same boundaries. Seeded here so each case below can
    // assert the pair, not just the badges.
    agentSessionStore.updateInfo('a1', { thinkingTokens: 500 })
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
    return [chatStore.getToolProgress('a1', 'toolu_A'), chatStore.getToolProgress('a1', 'toolu_B')]
      .filter(Boolean)
  }

  /**
   * Every TURN-ENDING boundary drops both live per-turn indicators, or neither.
   *
   * They are one concept -- state the worker broadcasts but cannot see the end
   * of -- so a boundary that clears one and forgets the other leaves an
   * indicator frozen for the rest of the session. That is the failure mode
   * clearPerTurnLiveState exists to make impossible, and this is what pins it:
   * a future boundary that calls only `clearToolProgress` fails here.
   *
   * A control request is deliberately NOT one of these; see its own case below.
   */
  function expectNothingLive(s: ReturnType<typeof boundaryStores>) {
    expect(running(s.chatStore)).toHaveLength(0)
    expect(s.agentSessionStore.getInfo('a1').thinkingTokens).toBeUndefined()
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
      handleAgentInactive('a1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'live', s, undefined)
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
   * A control request is the ONE boundary that keeps the badges.
   *
   * Claude Code starts a tool's heartbeat only after the permission decision, so
   * the tool that raised this prompt has reported nothing and owns no entry. The
   * e2e spec rests on the same rule: it has to bypass permissions, because a Bash
   * call held at a prompt never sends a heartbeat. Every entry that exists here
   * therefore belongs to a SIBLING tool that is still running, and clearing
   * blanks that live card until its next heartbeat, up to 30 seconds later.
   */
  it('a control request clears the estimate but keeps a still-running sibling\'s badge', () => {
    createRoot((dispose) => {
      const s = boundaryStores()
      const req = {
        requestId: 'r1',
        agentId: 'a1',
        payload: new TextEncoder().encode(JSON.stringify({ method: 'x' })),
      } as unknown as AgentControlRequest
      handleControlRequest('a1', req, 'live', s, undefined)
      expect(running(s.chatStore)).toHaveLength(2)
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toEqual({ elapsedSeconds: 30 })
      expect(s.agentSessionStore.getInfo('a1').thinkingTokens).toBeUndefined()
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
      s.chatStore.applyToolProgress('a2', { spanId: 'toolu_A', elapsedSeconds: 30 })
      const msg = agentMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a1', msg, parseMessageContent(msg), s, 'live')
      expect(running(s.chatStore)).toHaveLength(0)
      expect(s.chatStore.getToolProgress('a2', 'toolu_A')?.elapsedSeconds).toBe(30)
      dispose()
    })
  })
})

describe('dropFinishedToolProgress', () => {
  function seeded() {
    const chatStore = createChatStore()
    chatStore.applyToolProgress('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
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
      expect(chatStore.getToolProgress('a1', 'toolu_A')).toBeUndefined()
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
      expect(chatStore.getToolProgress('a1', 'toolu_A')?.elapsedSeconds).toBe(30)
      dispose()
    })
  })

  it('leaves the span alone for a row that carries no span at all', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      const msg = agentMessage({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } })
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', 'toolu_A')?.elapsedSeconds).toBe(30)
      dispose()
    })
  })

  it('drops only the span whose result landed', () => {
    createRoot((dispose) => {
      const chatStore = seeded()
      chatStore.applyToolProgress('a1', { spanId: 'toolu_B', elapsedSeconds: 60 })
      const msg = agentMessage(
        { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_A', content: 'ok' }] } },
        { spanId: 'toolu_A' },
      )
      dropFinishedToolProgress('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getToolProgress('a1', 'toolu_A')).toBeUndefined()
      expect(chatStore.getToolProgress('a1', 'toolu_B')?.elapsedSeconds).toBe(60)
      dispose()
    })
  })
})
