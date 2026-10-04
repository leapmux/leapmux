import { describe, expect, it } from 'vitest'
import { deepseekHarnessCanonicalMcpProjection, deepseekHarnessInspectReply } from './mcpToolResult'

const value = { contentMatches: true, echoedCount: 0, nextCount: 1, enabled: false, textMatches: true, textCharacters: 70000, nullable: null, hasNullable: true, hasPrivateMeta: false }
function result(projection: unknown = value, callId = 'call') {
  return { type: 'tool/result', data: { message: { toolCallId: callId, isError: false, content: [{ type: 'text', text: JSON.stringify(projection) }] } } }
}

describe('deepseekHarnessCanonicalMcpProjection', () => {
  it('reads actual native projection fields with zero, false, and null preserved', () => {
    const frame = result()
    const before = JSON.stringify(frame)
    expect(deepseekHarnessCanonicalMcpProjection(frame, 'call')).toEqual(value)
    expect(JSON.stringify(frame)).toBe(before)
  })

  it('refuses another call and absent or malformed canonical fields', () => {
    expect(() => deepseekHarnessCanonicalMcpProjection(result(), 'foreign')).toThrow('exact successful')
    for (const projection of [null, false, {}, { ...value, nullable: 0 }, { ...value, nextCount: '1' }, { ...value, textCharacters: -1 }])
      expect(() => deepseekHarnessCanonicalMcpProjection(result(projection), 'call')).toThrow('structured fields')
  })

  it('keeps contradictory native projection flags visible to the caller', () => {
    expect(deepseekHarnessCanonicalMcpProjection(result({ ...value, contentMatches: false, hasPrivateMeta: true }), 'call')).toMatchObject({ contentMatches: false, hasPrivateMeta: true })
  })
})

describe('deepseekHarnessInspectReply', () => {
  const input = { count: 0, enabled: false, text: '' }
  function receipts(id: string | number = 0) {
    return [{ request: { id, method: 'tools/call', params: { name: 'inspect', arguments: input } } }, { reply: { id, result: { content: [], structuredContent: { nextCount: 1, enabled: false, text: '' }, _meta: { privateFixture: true } } } }]
  }

  it.each([0, 'native-request'])('matches the exact native request and reply identity: %j', (id) => {
    expect(deepseekHarnessInspectReply(receipts(id), input)).toMatchObject({ _meta: { privateFixture: true }, structuredContent: { nextCount: 1, enabled: false, text: '' } })
  })

  it('rejects a different argument and ambiguous request or reply', () => {
    expect(() => deepseekHarnessInspectReply(receipts(), { ...input, enabled: true })).toThrow('exact request identity')
    expect(() => deepseekHarnessInspectReply([...receipts(), ...receipts('another')], input)).toThrow('exact request identity')
    const entries = receipts()
    const reply = entries[1]
    if (!reply)
      throw new Error('The native MCP unit fixture lost its reply.')
    expect(() => deepseekHarnessInspectReply([...entries, reply], input)).toThrow('successful server reply')
  })

  it.each([null, {}, [null], [{ request: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: input } } }]].map(receipt => ({ receipt })))('rejects an incomplete native receipt: $receipt', ({ receipt }) => {
    expect(() => deepseekHarnessInspectReply(receipt, input)).toThrow('native MCP')
  })
})
