import type { MessageInitShape } from '@bufbuild/protobuf'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelScenarioStatus } from './mockModelScript'
import type { NativeControlFrame } from './nativeControlWatch'
import type { NativeMessageSnapshot } from './nativeMessages'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MESSAGE_METADATA_FIELD, MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, ContentCompression, ControlResponseState, MarkType, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectTurnEndedAfter, onlyObservedNativeControl, readNativeStoredControlDecision, readObservedNativeDecision, waitForOneNativeControl } from './nativeStoredControlDecision'

const reads = vi.hoisted(() => ({ snapshot: vi.fn<(context: unknown, agentId: string) => Promise<NativeMessageSnapshot>>() }))
vi.mock('./nativeMessages', () => ({ readNativeMessageSnapshot: reads.snapshot }))
beforeEach(() => {
  reads.snapshot.mockReset()
})

const SESSION = 'native-session'
const REQUEST_ID = 'observed-request'
const CLAIM_TOKEN = 'observed-claim'
const NATIVE_REQUEST = { jsonrpc: '2.0', id: 0, method: 'native/review', params: { enabled: false, count: 0, text: '' } }
const NATIVE_ANSWER = { jsonrpc: '2.0', id: 0, result: { approved: false, count: 0, text: '' } }

const encoder = new TextEncoder()

function json(value: unknown): Uint8Array {
  return encoder.encode(JSON.stringify(value))
}

/** The supplement that the Worker writes beside a delivered answer. */
function supplement(requestId = REQUEST_ID, claimToken = CLAIM_TOKEN, request: unknown = NATIVE_REQUEST): Record<string, unknown> {
  return {
    [MESSAGE_SUPPLEMENT_FIELD.Provider]: request,
    [MESSAGE_SUPPLEMENT_FIELD.Metadata]: {
      [MESSAGE_METADATA_FIELD.ControlRequestID]: requestId,
      [MESSAGE_METADATA_FIELD.ControlRequestClaimToken]: claimToken,
    },
  }
}

interface DecisionRow {
  /** The decoded native answer. */
  answer?: unknown
  /** The decoded supplement. */
  supplemental?: unknown
  /** The remaining row fields. */
  row?: MessageInitShape<typeof AgentChatMessageSchema>
}

/** Build the control-response row that the Worker writes after a delivery. */
function decisionRow({ answer = NATIVE_ANSWER, supplemental = supplement(), row = {} }: DecisionRow = {}): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: 'native-decision-row',
    seq: 0n,
    agentSessionId: SESSION,
    source: MessageSource.USER,
    markType: MarkType.CONTROL_RESPONSE,
    depth: 0,
    parentSpanId: '',
    content: json(answer),
    contentCompression: ContentCompression.NONE,
    supplementalContent: json(supplemental),
    supplementalContentCompression: ContentCompression.NONE,
    ...row,
  })
}

function snapshot(messages: AgentChatMessage[] = [decisionRow()], identity: Partial<Omit<NativeMessageSnapshot, 'messages'>> = {}): NativeMessageSnapshot {
  return { agentId: 'root-agent', agentSessionId: SESSION, messages, ...identity }
}

function frame(requestId: string, payload: Record<string, unknown>, responseState = ControlResponseState.READY): NativeControlFrame {
  return { requestId, payload, responseState }
}

describe('readNativeStoredControlDecision', () => {
  it('returns the row, the native request, the native answer, and the claim token of the observed request', () => {
    const value = snapshot()
    const decision = readNativeStoredControlDecision(value, REQUEST_ID)
    expect(decision.message).toBe(value.messages[0])
    expect(decision.message.seq).toBe(0n)
    expect(decision.requestId).toBe(REQUEST_ID)
    expect(decision.claimToken).toBe(CLAIM_TOKEN)
    expect(decision.request).toEqual(NATIVE_REQUEST)
    expect(decision.response).toEqual(NATIVE_ANSWER)
  })

  it('selects the observed request beside decisions for other requests and rows without a decision', () => {
    const other = decisionRow({
      answer: { jsonrpc: '2.0', id: 1, result: { approved: true } },
      supplemental: supplement('another-request', 'another-claim', { jsonrpc: '2.0', id: 1, method: 'native/review' }),
      row: { id: 'another-row', seq: 1n },
    })
    const assistant = create(AgentChatMessageSchema, {
      id: 'assistant-row',
      seq: 2n,
      agentSessionId: SESSION,
      source: MessageSource.AGENT,
      content: json({ type: 'assistant', text: REQUEST_ID }),
      contentCompression: ContentCompression.NONE,
    })
    const observed = decisionRow({ row: { id: 'observed-row', seq: 3n } })
    const decision = readNativeStoredControlDecision(snapshot([other, assistant, observed]), REQUEST_ID)
    expect(decision.message).toBe(observed)
    expect(decision.response).toEqual(NATIVE_ANSWER)
  })

  it.each(['', ' '])('refuses an absent observed request ID: %j', (requestId) => {
    expect(() => readNativeStoredControlDecision(snapshot(), requestId)).toThrow('requires an agent ID, a native session ID, and an observed request ID')
  })

  it.each([
    { agentId: '' },
    { agentId: ' ' },
    { agentSessionId: '' },
    { agentSessionId: ' ' },
  ])('refuses an absent snapshot identity: %j', (identity) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow()], identity), REQUEST_ID)).toThrow('requires an agent ID, a native session ID, and an observed request ID')
  })

  it.each([
    { source: MessageSource.AGENT },
    { source: MessageSource.LEAPMUX },
    { markType: MarkType.UNSPECIFIED },
  ])('refuses a row that is not a saved control response: %j', (row) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow({ row })]), REQUEST_ID)).toThrow(`The row for native request ${REQUEST_ID} is not a saved control response.`)
  })

  it.each([
    { agentSessionId: 'foreign-session' },
    { agentSessionId: '' },
    { depth: 1 },
    { parentSpanId: 'child-span' },
  ])('refuses a decision outside the root scope of the native session: %j', (row) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow({ row })]), REQUEST_ID)).toThrow(`outside the root scope of native session ${SESSION}`)
  })

  it('refuses a foreign-session decision even beside the valid decision', () => {
    const foreign = decisionRow({ row: { id: 'foreign-row', seq: 1n, agentSessionId: 'foreign-session' } })
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow(), foreign]), REQUEST_ID)).toThrow('outside the root scope')
  })

  it.each([
    { name: 'an empty row ID', row: { id: '' } },
    { name: 'a blank row ID', row: { id: ' ' } },
    { name: 'a negative sequence', row: { seq: -1n } },
  ])('refuses a decision with $name', ({ row }) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow({ row })]), REQUEST_ID)).toThrow('has no valid Worker row identity')
  })

  it('refuses an absent decision', () => {
    expect(() => readNativeStoredControlDecision(snapshot([]), REQUEST_ID)).toThrow(`The Worker holds 0 saved decisions for native request ${REQUEST_ID}. Exactly one must exist.`)
    const other = decisionRow({ supplemental: supplement('another-request') })
    expect(() => readNativeStoredControlDecision(snapshot([other]), REQUEST_ID)).toThrow('holds 0 saved decisions')
  })

  it('refuses a duplicate decision', () => {
    const duplicate = decisionRow({ row: { id: 'duplicate-row', seq: 1n } })
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow(), duplicate]), REQUEST_ID)).toThrow(`The Worker holds 2 saved decisions for native request ${REQUEST_ID}. Exactly one must exist.`)
  })

  it.each([null, false, 0, '', [], ['answer']])('refuses a native answer that is not a JSON object: %j', (answer) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow({ answer })]), REQUEST_ID)).toThrow('lacks its native request, its native answer, or its claim token')
  })

  it('refuses native answer bytes that are not JSON and keeps those bytes on the row', () => {
    const row = decisionRow({ row: { content: encoder.encode('{"result":') } })
    expect(() => readNativeStoredControlDecision(snapshot([row]), REQUEST_ID)).toThrow('lacks its native request, its native answer, or its claim token')
    expect(new TextDecoder().decode(row.content)).toBe('{"result":')
  })

  it.each([
    { name: 'an absent native request', supplemental: { [MESSAGE_SUPPLEMENT_FIELD.Metadata]: supplement()[MESSAGE_SUPPLEMENT_FIELD.Metadata] } },
    { name: 'a native request that is not an object', supplemental: supplement(REQUEST_ID, CLAIM_TOKEN, ['native/review']) },
    { name: 'an empty claim token', supplemental: supplement(REQUEST_ID, '') },
    { name: 'a blank claim token', supplemental: supplement(REQUEST_ID, ' ') },
  ])('refuses $name', ({ supplemental }) => {
    expect(() => readNativeStoredControlDecision(snapshot([decisionRow({ supplemental })]), REQUEST_ID)).toThrow('lacks its native request, its native answer, or its claim token')
  })
})

describe('onlyObservedNativeControl', () => {
  it('refuses a watch that observed no request', () => {
    expect(() => onlyObservedNativeControl([])).toThrow('The Worker watch observed no native control request.')
  })

  it('returns the first frame of the one observed request across its state changes', () => {
    const ready = frame(REQUEST_ID, NATIVE_REQUEST)
    const completed = frame(REQUEST_ID, structuredClone(NATIVE_REQUEST), ControlResponseState.COMPLETED)
    expect(onlyObservedNativeControl([ready, completed])).toBe(ready)
  })

  it('refuses a second request ID', () => {
    expect(() => onlyObservedNativeControl([frame(REQUEST_ID, NATIVE_REQUEST), frame('second-request', NATIVE_REQUEST)]))
      .toThrow(`The Worker sent a second native control request second-request after ${REQUEST_ID}.`)
  })

  it('refuses a payload that changed for the same request ID', () => {
    const changed = { ...NATIVE_REQUEST, params: { ...NATIVE_REQUEST.params, count: 1 } }
    expect(() => onlyObservedNativeControl([frame(REQUEST_ID, NATIVE_REQUEST), frame(REQUEST_ID, changed, ControlResponseState.PENDING)]))
      .toThrow(`The native control request ${REQUEST_ID} changed its payload.`)
  })

  it.each(['', ' '])('refuses an observed request without a request ID: %j', (requestId) => {
    expect(() => onlyObservedNativeControl([frame(requestId, NATIVE_REQUEST)])).toThrow('has no request ID')
  })
})

describe('waitForOneNativeControl', () => {
  it('returns the first frame of the one request that the watch observed', async () => {
    const ready = frame(REQUEST_ID, NATIVE_REQUEST)
    const completed = frame(REQUEST_ID, structuredClone(NATIVE_REQUEST), ControlResponseState.COMPLETED)
    await expect(waitForOneNativeControl({ controls: () => [ready, completed] })).resolves.toBe(ready)
  })

  it('refuses a watch that observed two requests', async () => {
    await expect(waitForOneNativeControl({ controls: () => [frame(REQUEST_ID, NATIVE_REQUEST), frame('second-request', NATIVE_REQUEST)] })).rejects.toThrow('second native control request')
  })
})

describe('readObservedNativeDecision', () => {
  const context = { leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker' } }
  const agent = { id: 'root-agent', agentSessionId: SESSION }
  const observed = frame(REQUEST_ID, NATIVE_REQUEST)

  it('reads the saved answer of the agent and returns it with its snapshot', async () => {
    const saved = snapshot()
    reads.snapshot.mockResolvedValue(saved)
    const { decision, snapshot: read } = await readObservedNativeDecision(context, agent, { controls: () => [observed] }, observed)
    expect(reads.snapshot).toHaveBeenCalledWith(context, 'root-agent')
    expect(read).toBe(saved)
    expect(decision).toMatchObject({ requestId: REQUEST_ID, claimToken: CLAIM_TOKEN, request: NATIVE_REQUEST, response: NATIVE_ANSWER })
  })

  it('refuses a second request that the watch observed after the decision, before it reads the Worker', async () => {
    await expect(readObservedNativeDecision(context, agent, { controls: () => [observed, frame('second-request', NATIVE_REQUEST)] }, observed)).rejects.toThrow('second native control request')
    expect(reads.snapshot).not.toHaveBeenCalled()
  })

  it('refuses a frame other than the observed one, before it reads the Worker', async () => {
    reads.snapshot.mockResolvedValue(snapshot())
    const other = frame(REQUEST_ID, structuredClone(NATIVE_REQUEST))
    await expect(readObservedNativeDecision(context, agent, { controls: () => [other] }, observed)).rejects.toThrow('the watch still holds the observed request as its first frame')
    expect(reads.snapshot).not.toHaveBeenCalled()
  })

  it('refuses a snapshot of another native session', async () => {
    // The rows belong to the other session too, so only the session check can refuse the snapshot.
    reads.snapshot.mockResolvedValue(snapshot([decisionRow({ row: { agentSessionId: 'other-session' } })], { agentSessionId: 'other-session' }))
    await expect(readObservedNativeDecision(context, agent, { controls: () => [observed] }, observed)).rejects.toThrow('the saved decision belongs to the native session of the agent')
  })

  it('refuses a stored request that differs from the observed payload', async () => {
    reads.snapshot.mockResolvedValue(snapshot([decisionRow({ supplemental: supplement(REQUEST_ID, CLAIM_TOKEN, { ...NATIVE_REQUEST, id: 1 }) })]))
    await expect(readObservedNativeDecision(context, agent, { controls: () => [observed] }, observed)).rejects.toThrow('the Worker stored the native request that the browser answered')
  })
})

describe('expectTurnEndedAfter', () => {
  function status(nextStep: number, unexpected = 0): MockModelScenarioStatus {
    return {
      complete: true,
      nextStep,
      stepCount: nextStep,
      requests: [],
      unexpectedRequests: Array.from({ length: unexpected }, () => ({ protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', reason: 'unscripted', body: {} })),
      ruleMatches: {},
      pendingGates: [],
    }
  }

  it('accepts a turn that requested exactly the stated steps', async () => {
    await expect(expectTurnEndedAfter({ status: async () => status(3) }, 3)).resolves.toBeUndefined()
  })

  it.each([2, 4])('refuses a turn that requested %i ordered steps instead of 3', async (nextStep) => {
    await expect(expectTurnEndedAfter({ status: async () => status(nextStep) }, 3)).rejects.toThrow()
  })

  it('refuses a request that the script did not expect', async () => {
    await expect(expectTurnEndedAfter({ status: async () => status(3, 1) }, 3)).rejects.toThrow()
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses the step count %s before it reads the script', async (nextStep) => {
    const read = vi.fn(async () => status(1))
    await expect(expectTurnEndedAfter({ status: read }, nextStep)).rejects.toThrow('one or more ordered steps')
    expect(read).not.toHaveBeenCalled()
  })
})
