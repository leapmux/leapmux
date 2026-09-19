import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { renderKeyForEntry } from '~/components/chat/chatEntryCache'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'

// The harness's own contract. Every case here is a path the E2E suite used to
// prove with a browser, a worker and a database: pairing, the out-of-window
// fetch, the session-isolated fetch, and the late supplement that must reach
// the resident row, its classified entry, its rendered bubble, and its sibling's
// cache key.

const SESSION = 'session-a'
const CALL = 'call-1'

function zcodeRequest(id: string, input: Record<string, unknown> | undefined, toolName = 'Bash'): TranscriptFrame {
  return {
    id,
    provider: AgentProvider.ZCODE,
    spanId: CALL,
    spanType: toolName,
    agentSessionId: SESSION,
    content: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: CALL, toolName, ...(input === undefined ? { inputOmitted: true } : { input }) } },
  }
}

function zcodeResult(id: string, content: string, toolName = 'Bash'): TranscriptFrame {
  return {
    id,
    provider: AgentProvider.ZCODE,
    spanId: CALL,
    spanType: toolName,
    agentSessionId: SESSION,
    content: { type: 'tool.updated', payload: { kind: 'result', toolCallId: CALL, result: { success: true, content } } },
  }
}

function message(frame: TranscriptFrame, seq: bigint): AgentChatMessage {
  return makeTranscriptMessage(frame, seq)
}

describe('the transcript scenario harness', () => {
  it('resolves both sides of a request and result pair', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', { command: 'printf paired' }), 1n),
        message(zcodeResult('result', 'paired output'), 2n),
      ],
    })
    const requestRow = scenario.toolRow('request')
    const resultRow = scenario.toolRow('result')
    expect(requestRow.role).toBe('request')
    expect(requestRow.hasResultRow).toBe(true)
    expect(resultRow.role).toBe('result')
    expect(resultRow.hasRequestRow).toBe(true)
    const identity = { spanId: CALL, agentSessionId: SESSION }
    expect(scenario.resolver.request(identity)?.message.id).toBe('request')
    expect(scenario.resolver.result(identity)?.message.id).toBe('result')
    // The call itself stays the typed kind on both sides: no degradation to the
    // generic row.
    expect(requestRow.call.kind).toBe('execute')
    expect(resultRow.call.kind).toBe('execute')
  })

  it('fetches a result\'s request from outside the window', async () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', { command: 'printf fetched' }), 1n),
        message(zcodeResult('result', 'fetched output'), 2n),
      ],
      windowIds: ['result'],
    })
    expect(scenario.toolRow('result').hasRequestRow).toBe(false)
    await scenario.loadSpan('result')
    expect(scenario.toolRow('result').hasRequestRow).toBe(true)
    expect(scenario.entry('result').category.kind).toBe('tool_result')
  })

  it('rejects another session\'s reused tool ID when a span is fetched', async () => {
    const otherSession = zcodeRequest('other-session-request', { command: 'rm -rf /' })
    const scenario = createTranscriptScenario({
      archive: [
        message({ ...otherSession, agentSessionId: 'session-b' }, 1n),
        message(zcodeRequest('request', { command: 'printf mine' }), 2n),
        message(zcodeResult('result', 'my output'), 3n),
      ],
      windowIds: ['result'],
    })
    await scenario.loadSpan('result')
    // The fetch answered the span under THIS session's identity alone, so the
    // pairing landed on this session's own request.
    expect(scenario.toolRow('result').hasRequestRow).toBe(true)
    const request = scenario.resolver.request({ spanId: CALL, agentSessionId: SESSION })
    expect(request?.message.id).toBe('request')
    // The other session's row under the same tool ID never entered the resolver:
    // its identity resolves to nothing, and its bytes reach no resolved side.
    expect(scenario.resolver.request({ spanId: CALL, agentSessionId: 'session-b' })).toBeUndefined()
    expect(JSON.stringify(request?.parsed)).toContain('printf mine')
    expect(JSON.stringify(request?.parsed)).not.toContain('rm -rf')
  })

  it('replaces the resident row with a late supplement', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', undefined), 1n),
        message(zcodeResult('result', 'supplemented output'), 2n),
      ],
    })
    expect(scenario.toolRow('request').call.kind).toBe('execute')
    const before = scenario.toolRow('request').call
    scenario.replace({
      ...zcodeRequest('request', undefined),
      supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: CALL, input: { command: 'printf recovered' } } },
    })
    const after = scenario.toolRow('request').call
    expect(before).not.toBe(after)
    expect(after.kind === 'execute' ? after.request.command : '').toBe('printf recovered')
  })

  it('changes the classified-entry object when a supplement lands', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', undefined), 1n),
        message(zcodeResult('result', 'entry output'), 2n),
      ],
    })
    const before = scenario.entry('request')
    scenario.replace({
      ...zcodeRequest('request', undefined),
      supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: CALL, input: { command: 'printf second' } } },
    })
    const after = scenario.entry('request')
    expect(after).not.toBe(before)
    expect(after.freshness.contentVersion).toBe(before.freshness.contentVersion + 1)
  })

  it('changes the rendered bubble after the supplement', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', undefined), 1n),
        message(zcodeResult('result', 'rendered output'), 2n),
      ],
    })
    const before = scenario.renderBubble('request')
    const beforeText = before.container.textContent ?? ''
    scenario.replace({
      ...zcodeRequest('request', undefined),
      supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: CALL, input: { command: 'printf rendered' } } },
    })
    const after = scenario.renderBubble('request')
    const afterText = after.container.textContent ?? ''
    expect(afterText).not.toBe(beforeText)
    expect(afterText).toContain('printf rendered')
    before.unmount()
    after.unmount()
  })

  it('changes the request row\'s render key when a result-side supplement lands', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message(zcodeRequest('request', { command: 'printf keyed' }), 1n),
        message(zcodeResult('result', ''), 2n),
      ],
    })
    const before = renderKeyForEntry(scenario.entry('request'))
    scenario.replace({
      ...zcodeResult('result', 'the recovered body'),
      supplementalRevision: 2n,
    })
    const after = renderKeyForEntry(scenario.entry('request'))
    // The request row's key carries its sibling result's revision, so a
    // result-side change rebuilds the request row too.
    expect(after).not.toBe(before)
    // Its own entry object is new as well, while the result row stays the row
    // that changed.
    expect(scenario.entry('request').freshness.hasToolResultSibling).toBe(true)
  })
})
