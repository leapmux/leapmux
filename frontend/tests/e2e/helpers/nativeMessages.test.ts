import type { ManagedNativeScenarioContext } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentChatMessageSchema, AgentInfoSchema, AgentStatus, ContentCompression, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessagesHoldingText, nativeMessageSupplement, readAllAgentMessages, readNativeMessageSnapshot, readNativeToolOutputRecord } from './nativeMessages'

const calls = vi.hoisted(() => ({ agent: vi.fn(), worker: vi.fn() }))
vi.mock('./nativeScenario', () => ({ nativeAgentById: calls.agent }))
vi.mock('./api', () => ({ getTestChannel: async () => ({ callWorker: calls.worker }) }))
const context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'> = { leapmuxServer: { hubUrl: 'http://unit.invalid', adminToken: 'unit-token', workerId: 'worker' } }
const text = new TextEncoder()
function message(id: string, seq: bigint, body: unknown = {}) {
  return create(AgentChatMessageSchema, { id, seq, agentSessionId: 'native-session', content: text.encode(JSON.stringify(body)), contentCompression: ContentCompression.NONE })
}
beforeEach(() => {
  vi.resetAllMocks()
  calls.agent.mockResolvedValue(create(AgentInfoSchema, { id: 'parent', status: AgentStatus.ACTIVE, agentSessionId: 'native-session' }))
})

describe('readAllAgentMessages', () => {
  it('reads every ascending page and reads no agent identity', async () => {
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('zero', 0n), message('one', 1n)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('two', 2n)], hasMore: false }))
    expect((await readAllAgentMessages(context, 'parent')).map(value => value.id)).toEqual(['zero', 'one', 'two'])
    expect(calls.worker.mock.calls[1]?.[4]).toEqual({ agentId: 'parent', anchor: MessagePageAnchor.AFTER, cursorSeq: 1n, limit: MESSAGE_PAGE_LIMIT })
    expect(calls.agent).not.toHaveBeenCalled()
  })

  it.each(['', '  '])('refuses an absent agent ID before any Worker read: %j', async (agentId) => {
    await expect(readAllAgentMessages(context, agentId)).rejects.toThrow('nonempty agent ID')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('refuses a page that repeats a message ID', async () => {
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('same', 0n)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('same', 1n)] }))
    await expect(readAllAgentMessages(context, 'parent')).rejects.toThrow('duplicate message ID')
  })
})

describe('readNativeMessageSnapshot', () => {
  it('reads every ascending page and retains sequence zero without latest-only load flags', async () => {
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('zero', 0n)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('one', 1n), message('two', 2n)], hasMore: false }))
    const result = await readNativeMessageSnapshot(context, 'parent')
    expect(result.messages.map(value => value.id)).toEqual(['zero', 'one', 'two'])
    expect(result.agentSessionId).toBe('native-session')
    expect(calls.worker.mock.calls[0]?.[4]).toEqual({ agentId: 'parent', anchor: MessagePageAnchor.OLDEST, limit: MESSAGE_PAGE_LIMIT })
    expect(calls.worker.mock.calls[1]?.[4]).toEqual({ agentId: 'parent', anchor: MessagePageAnchor.AFTER, cursorSeq: 0n, limit: MESSAGE_PAGE_LIMIT })
  })

  it('retains an actually empty completed history', async () => {
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema))
    expect((await readNativeMessageSnapshot(context, 'parent')).messages).toEqual([])
  })

  it('reads every saved page for an active virtual child with stable parent, spawn, and root links', async () => {
    const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', spawnSpanId: 'native-spawn', rootAgentId: 'root' })
    calls.agent.mockResolvedValue(child)
    const first = create(AgentChatMessageSchema, { ...message('child-zero', 0n, { text: 'ACTUAL_CHILD_START' }), agentSessionId: '' })
    const last = create(AgentChatMessageSchema, { ...message('child-later', 100n, { text: 'ACTUAL_CHILD_END' }), agentSessionId: '' })
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [first], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [last] }))
    const result = await readNativeMessageSnapshot(context, 'child')
    expect(result.agentId).toBe('child')
    expect(result.agentSessionId).toBe('')
    expect(result.messages).toEqual([first, last])
    expect(result.messages.map(nativeMessageBody)).toEqual([{ text: 'ACTUAL_CHILD_START' }, { text: 'ACTUAL_CHILD_END' }])
    expect(calls.worker.mock.calls[0]?.[4]).toEqual({ agentId: 'child', anchor: MessagePageAnchor.OLDEST, limit: MESSAGE_PAGE_LIMIT })
    expect(calls.worker.mock.calls[1]?.[4]).toEqual({ agentId: 'child', anchor: MessagePageAnchor.AFTER, cursorSeq: 0n, limit: MESSAGE_PAGE_LIMIT })
    expect(calls.agent.mock.calls).toEqual([[context, 'child'], [context, 'child']])
  })

  it.each([
    { field: 'parentAgentId', value: '' },
    { field: 'parentAgentId', value: ' ' },
    { field: 'spawnSpanId', value: '' },
    { field: 'spawnSpanId', value: ' ' },
    { field: 'rootAgentId', value: '' },
    { field: 'rootAgentId', value: ' ' },
  ])('refuses an empty-session virtual child with an absent $field link: "$value"', async ({ field, value }) => {
    calls.agent.mockResolvedValue(create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', spawnSpanId: 'native-spawn', rootAgentId: 'root', [field]: value }))
    await expect(readNativeMessageSnapshot(context, 'child')).rejects.toThrow('started agent')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('refuses a whitespace native session even when all virtual-child links exist', async () => {
    calls.agent.mockResolvedValue(create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, agentSessionId: ' ', parentAgentId: 'parent', spawnSpanId: 'native-spawn', rootAgentId: 'root' }))
    await expect(readNativeMessageSnapshot(context, 'child')).rejects.toThrow('started agent')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('reads an unlinked virtual child through its durable native key', async () => {
    const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', rootAgentId: 'root', providerChildKey: 'native-child-key' })
    calls.agent.mockResolvedValue(child)
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [message('native-row', 0n)] }))
    const result = await readNativeMessageSnapshot(context, 'child')
    expect(result.messages.map(row => row.id)).toEqual(['native-row'])
    expect(calls.worker).toHaveBeenCalledOnce()
  })

  it('refuses an unlinked child whose native key changes during pagination', async () => {
    const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', rootAgentId: 'root', providerChildKey: 'native-child-key' })
    calls.agent.mockResolvedValueOnce(child).mockResolvedValueOnce(create(AgentInfoSchema, { ...child, providerChildKey: 'foreign-child-key' }))
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [message('native-row', 0n)] }))
    await expect(readNativeMessageSnapshot(context, 'child')).rejects.toThrow('session changed')
  })

  it.each([
    { field: 'parentAgentId', value: 'another-parent' },
    { field: 'spawnSpanId', value: 'another-spawn' },
    { field: 'rootAgentId', value: 'another-root' },
  ])('refuses a virtual child whose $field changes during pagination', async ({ field, value }) => {
    const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', spawnSpanId: 'native-spawn', rootAgentId: 'root' })
    calls.agent.mockResolvedValueOnce(child)
      .mockResolvedValueOnce(create(AgentInfoSchema, { ...child, [field]: value }))
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('first', 0n)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('last', 1n)] }))
    await expect(readNativeMessageSnapshot(context, 'child')).rejects.toThrow('session changed')
    expect(calls.worker).toHaveBeenCalledTimes(2)
    expect(calls.agent).toHaveBeenCalledTimes(2)
  })

  it.each([null, create(AgentInfoSchema, { id: 'parent', status: AgentStatus.STARTING, agentSessionId: 'native-session' }), create(AgentInfoSchema, { id: 'parent', status: AgentStatus.ACTIVE, rootAgentId: 'parent' })])('refuses an absent, loading, or unidentified native session', async (agent) => {
    calls.agent.mockResolvedValue(agent)
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('started agent')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('refuses an empty requested ID and another agent identity before reading messages', async () => {
    await expect(readNativeMessageSnapshot(context, '')).rejects.toThrow('nonempty agent ID')
    expect(calls.agent).not.toHaveBeenCalled()
    calls.agent.mockResolvedValue(create(AgentInfoSchema, { id: 'another-agent', status: AgentStatus.ACTIVE, agentSessionId: 'native-session' }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('started agent')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('refuses an empty page that claims more data', async () => {
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { hasMore: true }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('empty but claims')
  })

  it.each([0n, -1n])('refuses a repeated or regressing page cursor: %s', async (seq) => {
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('first', 0n)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('second', seq)] }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('did not advance')
  })

  it('preserves large bigint cursors and refuses an unsorted page', async () => {
    const large = BigInt(Number.MAX_SAFE_INTEGER) + 1n
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('large', large)], hasMore: true }))
      .mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('next', large + 1n)] }))
    await readNativeMessageSnapshot(context, 'parent')
    expect(calls.worker.mock.calls[1]?.[4].cursorSeq).toBe(large)
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('later', 2n), message('earlier', 1n)] }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('did not advance')
  })

  it('refuses duplicate message IDs even when their sequence changes', async () => {
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [message('same', 1n), message('same', 2n)] }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('duplicate message ID')
  })

  it('refuses empty IDs and a session that changes during pagination', async () => {
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [message('', 1n)] }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('absent or duplicate')
    calls.worker.mockResolvedValueOnce(create(ListAgentMessagesResponseSchema))
    calls.agent.mockResolvedValueOnce(create(AgentInfoSchema, { id: 'parent', status: AgentStatus.ACTIVE, agentSessionId: 'native-session' }))
      .mockResolvedValueOnce(create(AgentInfoSchema, { id: 'parent', status: AgentStatus.ACTIVE, agentSessionId: 'changed' }))
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toThrow('session changed')
  })

  it('preserves an actual Worker read error', async () => {
    const failure = new Error('Actual Worker transport failure.')
    calls.worker.mockRejectedValue(failure)
    await expect(readNativeMessageSnapshot(context, 'parent')).rejects.toBe(failure)
  })
})

describe('nativeMessageBody', () => {
  it('retains Unicode and JSON scalar boundaries', () => {
    for (const value of [null, 0, false, '', { text: '실제 내용 🧪' }])
      expect(nativeMessageBody(message('body', 1n, value))).toEqual(value)
  })

  it('refuses unsupported compression, damaged compressed bytes, and invalid JSON', () => {
    expect(() => nativeMessageBody(create(AgentChatMessageSchema, { content: text.encode('{}') }))).toThrow('unsupported')
    expect(() => nativeMessageBody(create(AgentChatMessageSchema, { content: new Uint8Array([0, 1]), contentCompression: ContentCompression.ZSTD }))).toThrow()
    expect(() => nativeMessageBody(create(AgentChatMessageSchema, { content: text.encode('{broken'), contentCompression: ContentCompression.NONE }))).toThrow('invalid JSON')
  })
})

describe('nativeMessageSupplement', () => {
  it('treats a valid compressed empty supplement as absent', () => {
    const source = message('empty-compressed-supplement', 1n)
    source.supplementalContent = new Uint8Array([0x28, 0xB5, 0x2F, 0xFD, 0x20, 0, 1, 0, 0])
    source.supplementalContentCompression = ContentCompression.ZSTD
    expect(nativeMessageSupplement(source)).toBeUndefined()
  })

  it('distinguishes an absent supplement from present native JSON', () => {
    expect(nativeMessageSupplement(message('empty', 1n))).toBeUndefined()
    const source = message('supplement', 1n)
    source.supplementalContent = text.encode('{"provider":{"zero":0}}')
    source.supplementalContentCompression = ContentCompression.NONE
    expect(nativeMessageSupplement(source)).toEqual({ provider: { zero: 0 } })
  })

  it('refuses unsupported compression and invalid supplement JSON', () => {
    const source = message('supplement', 1n)
    source.supplementalContent = text.encode('{broken')
    expect(() => nativeMessageSupplement(source)).toThrow('unsupported')
    source.supplementalContentCompression = ContentCompression.NONE
    expect(() => nativeMessageSupplement(source)).toThrow('invalid JSON')
  })
})

describe('readNativeToolOutputRecord', () => {
  const options = { callId: 'selected', spanId: 'selected-span', accepts: (frame: Record<string, unknown>) => frame.selected === true }
  function snapshot() {
    return { agentId: 'agent', agentSessionId: 'native-session', messages: [create(AgentChatMessageSchema, { ...message('selected', 1n, { selected: true, native: { zero: 0, enabled: false, text: '' } }), spanId: 'selected-span' })] }
  }

  it('reads the original owned packet with an absent optional supplement and unchanged bytes', () => {
    const value = snapshot()
    const before = value.messages[0]!.content.slice()
    const record = readNativeToolOutputRecord(value, options)
    expect(record.message).toBe(value.messages[0])
    expect(record.frame).toEqual({ selected: true, native: { zero: 0, enabled: false, text: '' } })
    expect(record.supplement).toBeUndefined()
    expect(value.messages[0]!.content).toEqual(before)
  })

  it('keeps a decoded supplement and every original packet byte', () => {
    const value = snapshot()
    const supplement = { provider: { zero: 0, enabled: false, nullable: null } }
    value.messages[0]!.supplementalContent = text.encode(JSON.stringify(supplement))
    value.messages[0]!.supplementalContentCompression = ContentCompression.NONE
    const before = value.messages[0]!.supplementalContent.slice()
    expect(readNativeToolOutputRecord(value, options).supplement).toEqual(supplement)
    expect(value.messages[0]!.supplementalContent).toEqual(before)
  })

  it.each([{ agentId: '' }, { agentId: ' ' }, { agentSessionId: '' }, { agentSessionId: ' ' }])('refuses an absent snapshot owner: %j', (change) => {
    expect(() => readNativeToolOutputRecord({ ...snapshot(), ...change }, options)).toThrow('span owner')
  })

  it('refuses a foreign session or span and duplicate accepted records', () => {
    const value = snapshot()
    expect(() => readNativeToolOutputRecord({ ...value, agentSessionId: 'foreign' }, options)).toThrow('exactly one')
    expect(() => readNativeToolOutputRecord(value, { ...options, spanId: 'foreign' })).toThrow('exactly one')
    expect(() => readNativeToolOutputRecord({ ...value, messages: [value.messages[0]!, value.messages[0]!] }, options)).toThrow('exactly one')
  })

  it('uses a provider callback for a span and propagates both callback failures', () => {
    const value = snapshot()
    expect(readNativeToolOutputRecord(value, { ...options, spanId: frame => frame.selected === true ? 'selected-span' : '' }).message).toBe(value.messages[0])
    const failure = new Error('Callback failure')
    expect(() => readNativeToolOutputRecord(value, { ...options, accepts: () => {
      throw failure
    } })).toThrow(failure)
    expect(() => readNativeToolOutputRecord(value, { ...options, spanId: () => {
      throw failure
    } })).toThrow(failure)
    expect(() => readNativeToolOutputRecord(value, { ...options, spanId: () => '' })).toThrow('empty span owner')
  })

  it('refuses malformed selected JSON and leaves its original bytes unchanged', () => {
    const value = snapshot()
    const bytes = text.encode('{broken')
    value.messages[0]!.content = bytes
    expect(() => readNativeToolOutputRecord(value, options)).toThrow('invalid JSON')
    expect(value.messages[0]!.content).toEqual(bytes)
  })
})

describe('nativeMessagesHoldingText', () => {
  function withSupplement(id: string, seq: bigint, body: unknown, supplement: unknown) {
    return create(AgentChatMessageSchema, { ...message(id, seq, body), supplementalContent: text.encode(JSON.stringify(supplement)), supplementalContentCompression: ContentCompression.NONE })
  }

  it('selects in order each row whose content or supplement holds the text in one string value', () => {
    const rows = [
      message('content', 1n, { message: { content: [{ type: 'text', text: 'Prefix MARKER_TEXT suffix' }] } }),
      message('unrelated', 2n, { text: 'OTHER_TEXT' }),
      withSupplement('supplement', 3n, { type: 'result' }, { plain: ['MARKER_TEXT'] }),
      message('merged', 4n, { text: 'MARKER_TEXTNEXT_TEXT' }),
    ]
    expect(nativeMessagesHoldingText(rows, 'MARKER_TEXT').map(row => row.id)).toEqual(['content', 'supplement', 'merged'])
  })

  it('counts a row once when its content and supplement both hold the text', () => {
    expect(nativeMessagesHoldingText([withSupplement('both', 1n, { text: 'MARKER_TEXT' }, { text: 'MARKER_TEXT' })], 'MARKER_TEXT')).toHaveLength(1)
  })

  it('does not match an object key or text split across two string values', () => {
    const rows = [message('key', 1n, { MARKER_TEXT: 'value' }), message('split', 2n, { parts: ['MARKER_', 'TEXT'] })]
    expect(nativeMessagesHoldingText(rows, 'MARKER_TEXT')).toEqual([])
  })

  it('returns no row for an empty history', () => {
    expect(nativeMessagesHoldingText([], 'MARKER_TEXT')).toEqual([])
  })

  it.each(['', ' ', '\n'])('rejects the empty search text %j', (value) => {
    expect(() => nativeMessagesHoldingText([message('row', 1n, { text: 'MARKER_TEXT' })], value)).toThrow('nonempty text')
  })

  it('refuses a row with invalid JSON instead of skipping it', () => {
    const row = message('broken', 1n)
    row.content = text.encode('{broken')
    expect(() => nativeMessagesHoldingText([row], 'MARKER_TEXT')).toThrow('invalid JSON')
  })
})
