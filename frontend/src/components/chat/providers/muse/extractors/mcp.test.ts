import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { MUSE_NATIVE_MCP_ITEM_FRAME, MUSE_NATIVE_MCP_ORIGIN_FRAME, MUSE_NATIVE_MCP_RESULT_FRAME } from '../toolResults.fixtures'
import '~/components/chat/providers'

function nativeMcp() {
  const payload = JSON.parse(MUSE_NATIVE_MCP_ITEM_FRAME)
  const origin = JSON.parse(MUSE_NATIVE_MCP_ORIGIN_FRAME).params.record
  const result = JSON.parse(MUSE_NATIVE_MCP_RESULT_FRAME).params.record
  return { payload, origin, result, supplement: { nativeRecords: [origin, result] } }
}

describe('museMcpSpec', () => {
  it('reads the recorded native server and tool without inventing structured output', () => {
    const source = nativeMcp()
    const original = structuredClone(source)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    if (call?.kind !== 'mcp')
      throw new Error('The native MCP item requires an MCP call.')
    expect(call.id).toBe('01a1179d-9f69-7330-a6d1-b866388ce42f')
    expect(call.name).toBe('mcp__echo_probe__echo')
    expect(call.title).toBe('echo_probe / echo')
    expect(call.request).toEqual({ server: 'echo_probe', tool: 'echo', args: { value: 'MUSE_MCP_EXECUTION_PROOF' } })
    expect(call.status).toBe('completed')
    expect(call.result).toEqual({ unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' })
    expect(call.images).toEqual([])
    expect(source).toEqual(original)
    expect(JSON.stringify(source.payload)).toBe(MUSE_NATIVE_MCP_ITEM_FRAME)
    expect(source.origin.sequence).toBe(58)
    expect(source.payload.params.sourceRange.last.sequence).toBe(68)
    expect(source.result.sequence).toBe(70)
  })

  it.each([
    ['inProgress', 'in_progress', undefined],
    ['completed', 'completed', { unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' }],
    ['failed', 'failed', { failure: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' }],
    ['timedOut', 'failed', { failure: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' }],
    ['rejected', 'declined', { failure: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' }],
    ['cancelled', 'cancelled', { unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' }],
    ['futureFinal', 'incomplete', undefined],
    ['', 'incomplete', undefined],
  ])('keeps native status %s as %s', (nativeStatus, expectedStatus, expectedResult) => {
    const source = nativeMcp()
    source.payload.params.item.status = nativeStatus
    const original = structuredClone(source)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.status).toBe(expectedStatus)
    expect(call?.result).toEqual(expectedResult)
    expect(source).toEqual(original)
  })

  it('keeps retained finality separate from a native result', () => {
    const source = nativeMcp()
    source.payload.params.item.status = 'inProgress'
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { completion: MessageCompletion.FINISHED, supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it.each(['session', 'turn', 'origin', 'sequence', 'batch', 'call', 'arguments', 'unavailable'])('keeps plain native preview when %s refuses the supplemental result', (change) => {
    const source = nativeMcp()
    switch (change) {
      case 'session': source.result.stream.id = 'foreign'
        break
      case 'turn': source.result.payload.run_id = 'foreign'
        break
      case 'origin': source.origin.id = 'foreign'
        break
      case 'sequence': source.origin.sequence = 59
        break
      case 'batch': source.result.payload.event.batch_id = 'foreign'
        break
      case 'call': source.result.payload.event.results[0].tool_call_id = 'foreign'
        break
      case 'arguments': source.origin.payload.event.tool_calls[0].args = '{}'
        break
      case 'unavailable': Object.assign(source.supplement, { nativeResultUnavailable: { reason: 'The native record is absent.' } })
        break
    }
    const original = structuredClone(source)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.result).toEqual({ unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' })
    expect(call?.images).toEqual([])
    expect(source).toEqual(original)
  })

  it.each([undefined, null, {}, { nativeRecords: [null] }])('keeps its native preview without a readable supplement %j', (supplementalContent) => {
    const source = nativeMcp()
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent })
    expect(call?.kind).toBe('mcp')
    expect(call?.result).toEqual({ unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' })
  })

  it('keeps an empty native result and empty arguments', () => {
    const source = nativeMcp()
    source.payload.params.item.args = '{}'
    source.origin.payload.event.tool_calls[0].args = '{}'
    source.payload.params.item.visibleOutput = ''
    source.result.payload.event.results[0].text = ''
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.request).toEqual({ server: 'echo_probe', tool: 'echo', args: {} })
    expect(call?.status).toBe('completed')
    expect(call?.result).toEqual({ unparsed: true, text: '' })
  })

  it('preserves zero, negative, empty, and large native argument values', () => {
    const source = nativeMcp()
    const args = { zero: 0, negative: -1, empty: '', optional: null, large: Number.MAX_SAFE_INTEGER, text: '界'.repeat(8192) }
    source.payload.params.item.args = JSON.stringify(args)
    source.origin.payload.event.tool_calls[0].args = JSON.stringify(args)
    const original = structuredClone(source)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.request).toEqual({ server: 'echo_probe', tool: 'echo', args })
    expect(source).toEqual(original)
  })

  it('keeps JSON-looking native plain output as text', () => {
    const source = nativeMcp()
    source.payload.params.item.visibleOutput = '{"native":"plain"}'
    source.result.payload.event.results[0].text = '{"native":"plain"}'
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.result).toEqual({ unparsed: true, text: '{"native":"plain"}' })
    expect(call?.images).toEqual([])
  })

  it('preserves every later separator in the native tool name', () => {
    const source = nativeMcp()
    source.payload.params.item.tool = 'mcp__echo_probe__echo__detail'
    source.origin.payload.event.tool_calls[0].name = 'mcp__echo_probe__echo__detail'
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('mcp')
    expect(call?.request).toEqual({ server: 'echo_probe', tool: 'echo__detail', args: { value: 'MUSE_MCP_EXECUTION_PROOF' } })
  })

  it.each(['mcp__', 'mcp____echo', 'mcp__echo_probe__', 'other__echo_probe__echo', 'muse.mcp__echo_probe__echo'])('keeps malformed or foreign native names generic: %s', (name) => {
    const source = nativeMcp()
    source.payload.params.item.tool = name
    source.origin.payload.event.tool_calls[0].name = name
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    expect(call?.kind).toBe('other')
    expect(call?.name).toBe(name)
    expect(call?.request).toEqual({ args: { value: 'MUSE_MCP_EXECUTION_PROOF' } })
  })
})
