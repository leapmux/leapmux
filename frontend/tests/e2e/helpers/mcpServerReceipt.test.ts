import type { McpExchangeEntry, McpServerReceipt } from './mcpServerReceipt'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  mcpCallArguments,
  mcpCallExchange,
  mcpReceiptListsTool,
  mcpReceiptRequestId,
  nativeMcpCancellation,
  nativeMcpRefusal,
  nativeMcpUnansweredInput,
  parseMcpServerReceipt,
  readMcpCallArguments,
  readMcpCallExchange,
  readMcpServerReceipt,
  waitForMcpToolListed,
} from './mcpServerReceipt'

// The poll of vitest ends after one second, and the poll of Playwright after five, which is the limit of a vitest case.
// The wait reads a receipt that the runtime replaces through a rename, so no read throws, and both polls act alike.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

function emptyReceipt(): McpServerReceipt {
  return { initializeCapabilities: null, toolCatalogs: [], elicitationRequests: [], elicitationReplies: [], toolResults: [], exchange: [] }
}

function refusalReceipt(): McpServerReceipt {
  return {
    initializeCapabilities: {},
    toolCatalogs: [{ id: 0, tools: [{ name: 'ask', inputSchema: { type: 'object', properties: {} } }] }],
    elicitationRequests: [{ id: 'probe-form', toolRequestId: 2, params: { mode: 'form' } }],
    elicitationReplies: [{ id: 'probe-form', kind: 'error', error: { code: -32601, message: 'Method not found', data: { native: true } } }],
    toolResults: [{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_REFUSED: -32601 Method not found', isError: true }],
    exchange: [],
  }
}

describe('parseMcpServerReceipt', () => {
  it('accepts an empty pending receipt without inventing an initialize or request', () => {
    expect(parseMcpServerReceipt(emptyReceipt())).toEqual(emptyReceipt())
  })

  it('keeps the exchange in order and in its direction', () => {
    const value = { ...emptyReceipt(), exchange: [{ received: { id: 0, method: 'initialize' } }, { sent: { id: 0, result: {} } }] }
    expect(parseMcpServerReceipt(value).exchange).toEqual(value.exchange)
  })

  it('preserves native catalogs, matching IDs, error data, zero, and false', () => {
    const value = refusalReceipt()
    value.elicitationRequests.push({ id: '', toolRequestId: 0, params: { count: 0, enabled: false } })
    value.elicitationReplies.push({ id: '', kind: 'result', result: { action: 'accept', content: { count: 0, enabled: false } } })
    expect(parseMcpServerReceipt(value)).toEqual(value)
  })

  it.each([
    { label: 'a missing root', value: null },
    { label: 'a missing capabilities field', value: { ...emptyReceipt(), initializeCapabilities: undefined } },
    { label: 'array capabilities', value: { ...emptyReceipt(), initializeCapabilities: [] } },
    { label: 'a missing catalog array', value: { ...emptyReceipt(), toolCatalogs: undefined } },
    { label: 'a null tool schema', value: { ...emptyReceipt(), toolCatalogs: [{ id: 1, tools: [{ name: 'ask', inputSchema: null }] }] } },
    { label: 'an empty tool name', value: { ...emptyReceipt(), toolCatalogs: [{ id: 1, tools: [{ name: '', inputSchema: {} }] }] } },
    { label: 'a missing originating tool ID', value: { ...emptyReceipt(), elicitationRequests: [{ id: 1, params: {} }] } },
    { label: 'an unsafe request ID', value: { ...emptyReceipt(), elicitationRequests: [{ id: Number.MAX_SAFE_INTEGER + 1, toolRequestId: 2, params: {} }] } },
    { label: 'a repeated request ID', value: { ...refusalReceipt(), elicitationRequests: [refusalReceipt().elicitationRequests[0], refusalReceipt().elicitationRequests[0]] } },
    { label: 'an unknown reply kind', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'timeout' }] } },
    { label: 'a string error code', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'error', error: { code: '-32601', message: 'Method not found' } }] } },
    { label: 'an absent error message', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'error', error: { code: -32601 } }] } },
    { label: 'an absent result object', value: { ...emptyReceipt(), elicitationReplies: [{ id: 1, kind: 'result', result: null }] } },
    { label: 'a non-boolean error flag', value: { ...emptyReceipt(), toolResults: [{ id: 1, tool: 'ask', text: '', isError: 0 }] } },
    { label: 'a missing exchange', value: { ...emptyReceipt(), exchange: undefined } },
    { label: 'an exchange entry that is not an object', value: { ...emptyReceipt(), exchange: [null] } },
    { label: 'an exchange entry with no direction', value: { ...emptyReceipt(), exchange: [{ request: { id: 0 } }] } },
    { label: 'an exchange entry with two directions', value: { ...emptyReceipt(), exchange: [{ received: { id: 0 }, sent: { id: 0 } }] } },
    { label: 'a received message that is not an object', value: { ...emptyReceipt(), exchange: [{ received: [] }] } },
    { label: 'a sent message that is not an object', value: { ...emptyReceipt(), exchange: [{ sent: 'reply' }] } },
  ])('rejects $label', ({ value }) => {
    expect(() => parseMcpServerReceipt(value)).toThrow('MCP receipt')
  })
})

describe('nativeMcpRefusal', () => {
  it('pairs the actual request, native unsupported error, and exact originating tool result', () => {
    const value = refusalReceipt()
    expect(nativeMcpRefusal(value)).toEqual({ request: value.elicitationRequests[0], reply: value.elicitationReplies[0], toolResult: value.toolResults[0], reason: '-32601 Method not found' })
  })

  it('uses the matching result when another tool finishes last', () => {
    const value = refusalReceipt()
    value.toolResults.push({ id: 99, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false })
    expect(nativeMcpRefusal(value).toolResult.id).toBe(2)
  })

  it.each([
    { label: 'no initialize', error: 'has no native initialize capabilities', change: (value: McpServerReceipt) => { value.initializeCapabilities = null } },
    { label: 'no actual input request', error: 'has no actual input request', change: (value: McpServerReceipt) => { value.elicitationRequests = [] } },
    { label: 'an unmatched reply ID', error: 'The MCP input request has no matching native reply.', change: (value: McpServerReceipt) => { value.elicitationReplies[0]!.id = 'different' } },
    { label: 'an accepted form', error: 'A declined or accepted MCP result does not prove an unsupported input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'accept' } }] } },
    { label: 'a user decline', error: 'A declined or accepted MCP result does not prove an unsupported input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'decline' } }] } },
    { label: 'a timeout instead of native refusal', error: 'The native MCP error does not prove an unsupported input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'error', error: { code: -32000, message: 'The operation timed out' } }] } },
    { label: 'an unrelated tool result', error: 'The refused MCP input has no matching native tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.id = 99 } },
    { label: 'a lost native error flag', error: 'The native MCP error lost its exact refused tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.isError = false } },
    { label: 'an altered native refusal reason', error: 'The native MCP error lost its exact refused tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.text = 'FORM_ROUND_TRIP_REFUSED: other' } },
  ])('rejects $label as unsupported-feature proof', ({ change, error }) => {
    const value = refusalReceipt()
    change(value)
    expect(() => nativeMcpRefusal(value)).toThrow(error)
  })
})

describe('nativeMcpCancellation', () => {
  function cancellationReceipt(): McpServerReceipt {
    return {
      ...refusalReceipt(),
      initializeCapabilities: { elicitation: { form: {}, url: {} } },
      elicitationReplies: [{ id: 'probe-form', kind: 'result', result: { action: 'cancel' } }],
      toolResults: [{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_CANCELLED', isError: false }],
    }
  }

  it('pairs the actual request, native cancel, and exact originating tool result', () => {
    const value = cancellationReceipt()
    expect(nativeMcpCancellation(value)).toEqual({ request: value.elicitationRequests[0], reply: value.elicitationReplies[0], toolResult: value.toolResults[0] })
  })

  it('accepts an empty elicitation capability', () => {
    const value = cancellationReceipt()
    value.initializeCapabilities = { elicitation: {} }
    expect(nativeMcpCancellation(value).request.id).toBe('probe-form')
  })

  it('uses the last reply and the matching result of the last request', () => {
    const value = cancellationReceipt()
    value.elicitationRequests.unshift({ id: 'earlier-form', toolRequestId: 1, params: { mode: 'form' } })
    value.elicitationReplies.unshift({ id: 'earlier-form', kind: 'result', result: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } })
    value.toolResults.unshift({ id: 1, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false })
    value.toolResults.push({ id: 99, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false })
    const proof = nativeMcpCancellation(value)
    expect(proof.request.id).toBe('probe-form')
    expect(proof.toolResult.id).toBe(2)
  })

  it.each([
    { label: 'no initialize', error: 'has no native initialize capabilities', change: (value: McpServerReceipt) => { value.initializeCapabilities = null } },
    { label: 'no declared elicitation capability', error: 'A client that declares no elicitation does not prove a cancelled input request.', change: (value: McpServerReceipt) => { value.initializeCapabilities = {} } },
    { label: 'no actual input request', error: 'has no actual input request', change: (value: McpServerReceipt) => { value.elicitationRequests = [] } },
    { label: 'an unanswered request', error: 'The MCP input request has no matching native reply.', change: (value: McpServerReceipt) => { value.elicitationReplies = [] } },
    { label: 'an unmatched reply ID', error: 'The MCP input request has no matching native reply.', change: (value: McpServerReceipt) => { value.elicitationReplies[0]!.id = 'different' } },
    { label: 'an accepted form', error: 'Only a native cancel result proves a cancelled input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } }] } },
    { label: 'a user decline', error: 'Only a native cancel result proves a cancelled input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'decline' } }] } },
    { label: 'a result without an action', error: 'Only a native cancel result proves a cancelled input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: {} }] } },
    { label: 'a cancel that holds form content', error: 'A native cancel result must hold no form content.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'cancel', content: {} } }] } },
    { label: 'a native refusal error', error: 'Only a native cancel result proves a cancelled input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = refusalReceipt().elicitationReplies } },
    { label: 'a later accept of the same request', error: 'Only a native cancel result proves a cancelled input request.', change: (value: McpServerReceipt) => { value.elicitationReplies.push({ id: 'probe-form', kind: 'result', result: { action: 'accept', content: {} } }) } },
    { label: 'an unrelated tool result', error: 'The cancelled MCP input has no matching native tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.id = 99 } },
    { label: 'a result of another tool', error: 'The cancelled MCP input has no matching native tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.tool = 'echo' } },
    { label: 'an error flag on the cancelled result', error: 'The native MCP cancel lost its exact cancelled tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.isError = true } },
    { label: 'an altered cancelled result', error: 'The native MCP cancel lost its exact cancelled tool result.', change: (value: McpServerReceipt) => { value.toolResults[0]!.text = 'FORM_ROUND_TRIP_DECLINED' } },
  ])('rejects $label as cancelled-input proof', ({ change, error }) => {
    const value = cancellationReceipt()
    change(value)
    expect(() => nativeMcpCancellation(value)).toThrow(error)
  })
})

describe('nativeMcpUnansweredInput', () => {
  function unansweredReceipt(): McpServerReceipt {
    return { ...refusalReceipt(), elicitationReplies: [], toolResults: [] }
  }

  it('returns the actual request that the client left without a reply', () => {
    const value = unansweredReceipt()
    expect(nativeMcpUnansweredInput(value)).toEqual({ request: value.elicitationRequests[0] })
  })

  it('accepts a reply and a result that belong to an earlier request', () => {
    const value = unansweredReceipt()
    value.elicitationRequests.unshift({ id: 'earlier-form', toolRequestId: 1, params: { mode: 'form' } })
    value.elicitationReplies.push({ id: 'earlier-form', kind: 'error', error: { code: -32601, message: 'Method not found' } })
    value.toolResults.push({ id: 1, tool: 'ask', text: 'FORM_ROUND_TRIP_REFUSED: -32601 Method not found', isError: true })
    expect(nativeMcpUnansweredInput(value).request.id).toBe('probe-form')
  })

  it.each([
    { label: 'no initialize', error: 'has no native initialize capabilities', change: (value: McpServerReceipt) => { value.initializeCapabilities = null } },
    { label: 'a declared elicitation capability', error: 'A client that declares elicitation does not prove an unanswered input request.', change: (value: McpServerReceipt) => { value.initializeCapabilities = { elicitation: {} } } },
    { label: 'no actual input request', error: 'has no actual input request', change: (value: McpServerReceipt) => { value.elicitationRequests = [] } },
    { label: 'a native refusal', error: 'The native client answered the MCP input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = refusalReceipt().elicitationReplies } },
    { label: 'a declined form', error: 'The native client answered the MCP input request.', change: (value: McpServerReceipt) => { value.elicitationReplies = [{ id: 'probe-form', kind: 'result', result: { action: 'decline' } }] } },
    { label: 'a completed tool call', error: 'The MCP server completed the tool call whose input request has no reply.', change: (value: McpServerReceipt) => { value.toolResults = [{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_FAILED', isError: true }] } },
  ])('rejects $label as unanswered-input proof', ({ change, error }) => {
    const value = unansweredReceipt()
    change(value)
    expect(() => nativeMcpUnansweredInput(value)).toThrow(error)
  })
})

describe('mcpReceiptRequestId', () => {
  it.each([0, -1, Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER, '', '0', 'native-call-한글'])('preserves a valid typed request ID: %j', (value) => {
    expect(mcpReceiptRequestId(value, 'request ID')).toBe(value)
  })

  it.each([undefined, null, false, {}, [], 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, Number.MIN_SAFE_INTEGER - 1])('rejects an invalid request ID: %j', (value) => {
    expect(() => mcpReceiptRequestId(value, 'request ID')).toThrow('request ID')
  })
})

describe('mcpReceiptListsTool', () => {
  it('finds a tool in any catalog, and no other tool', () => {
    const value = refusalReceipt()
    value.toolCatalogs.push({ id: 1, tools: [{ name: 'echo', inputSchema: {} }] })
    expect(mcpReceiptListsTool(value, 'ask')).toBe(true)
    expect(mcpReceiptListsTool(value, 'echo')).toBe(true)
    expect(mcpReceiptListsTool(value, 'Ask')).toBe(false)
    expect(mcpReceiptListsTool(emptyReceipt(), 'ask')).toBe(false)
  })

  it.each(['', ' '])('refuses an empty tool name: %j', (name) => {
    expect(() => mcpReceiptListsTool(refusalReceipt(), name)).toThrow('tool name')
  })
})

let directory: string
let path: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'mcp-server-receipt-'))
  path = join(directory, 'receipt.json')
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

/** A receipt that holds only the exchange `entries`. */
function exchangeReceipt(entries: McpExchangeEntry[]): McpServerReceipt {
  return { ...emptyReceipt(), exchange: entries }
}

/** The message of one exchange entry, whatever its direction. */
function messageOf(entry: McpExchangeEntry | undefined): Record<string, unknown> {
  if (!entry)
    throw new Error('The exchange has no such entry.')
  return 'received' in entry ? entry.received : entry.sent
}

describe('readMcpServerReceipt', () => {
  it('reads and validates the file that a server writes', () => {
    writeFileSync(path, JSON.stringify(refusalReceipt()))
    expect(readMcpServerReceipt(path)).toEqual(refusalReceipt())
  })

  it('preserves file-read and malformed-JSON failures', () => {
    expect(() => readMcpServerReceipt(path)).toThrow(expect.objectContaining({ code: 'ENOENT' }))
    writeFileSync(path, '{')
    expect(() => readMcpServerReceipt(path)).toThrow(SyntaxError)
  })
})

describe('waitForMcpToolListed', () => {
  /** Write a receipt whose server answered `initialize` with `initializeCapabilities` and listed `tools`. */
  function writeListing(initializeCapabilities: Record<string, unknown> | null, tools: string[]): void {
    writeFileSync(path, JSON.stringify({
      ...emptyReceipt(),
      initializeCapabilities,
      toolCatalogs: [{ id: 1, tools: tools.map(name => ({ name, inputSchema: { type: 'object' } })) }],
    }))
  }

  it('returns once the receipt lists the tool', async () => {
    writeFileSync(path, JSON.stringify(refusalReceipt()))
    await expect(waitForMcpToolListed(path, 'ask')).resolves.toBeUndefined()
  })

  it('accepts a server that answered initialize and listed the tool among others', async () => {
    writeListing({ tools: {} }, ['ask', 'probe'])
    await expect(waitForMcpToolListed(path, 'probe')).resolves.toBeUndefined()
  })

  it('waits for a receipt that the server writes after the wait starts', async () => {
    const waited = waitForMcpToolListed(path, 'echo')
    setTimeout(writeListing, 50, {}, ['echo'])
    await expect(waited).resolves.toBeUndefined()
  })

  it('refuses a server that lists no such tool', async () => {
    writeListing({}, ['ask'])
    await expect(waitForMcpToolListed(path, 'echo')).rejects.toThrow('lists the tool echo')
  })

  it('refuses a server that never answered initialize', async () => {
    writeListing(null, ['echo'])
    await expect(waitForMcpToolListed(path, 'echo')).rejects.toThrow('answered initialize')
  })

  it('refuses an absent receipt', async () => {
    await expect(waitForMcpToolListed(path, 'echo')).rejects.toThrow('lists the tool echo')
  })

  it.each(['', ' '])('refuses the tool name %j before it reads the receipt', async (toolName) => {
    await expect(waitForMcpToolListed(path, toolName)).rejects.toThrow('needs the name of a tool')
  })
})

describe('mcpCallArguments', () => {
  it('keeps exact native zero, false, empty, and Unicode values in call order', () => {
    const argumentsList = [
      { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } },
      { name: 'inspect_한글', arguments: { text: '출력' } },
    ]
    const receipt = exchangeReceipt([
      { received: { method: 'tools/list', params: {} } },
      { sent: { id: 0, result: {} } },
      ...argumentsList.map(params => ({ received: { method: 'tools/call', params } })),
    ])
    expect(mcpCallArguments(receipt)).toEqual(argumentsList)
  })

  it('accepts an empty actual call inventory', () => {
    expect(mcpCallArguments(emptyReceipt())).toEqual([])
  })

  it('reads no call from a message that the server sent', () => {
    expect(mcpCallArguments(exchangeReceipt([{ sent: { method: 'tools/call', params: { name: 'inspect', arguments: {} } } }]))).toEqual([])
  })

  it('keeps exact arguments when the native transport adds progress metadata', () => {
    const argumentsRecord = { count: 0, enabled: false, text: '' }
    const receipt = exchangeReceipt([{ received: { method: 'tools/call', params: { name: 'inspect', arguments: argumentsRecord, _meta: { progressToken: 2 } } } }])
    expect(mcpCallArguments(receipt)).toEqual([{ name: 'inspect', arguments: argumentsRecord }])
  })

  it.each([
    { label: 'no params', message: { method: 'tools/call' } },
    { label: 'null arguments', message: { method: 'tools/call', params: { name: 'inspect', arguments: null } } },
    { label: 'no tool name', message: { method: 'tools/call', params: { arguments: {} } } },
  ])('refuses a call with $label', ({ message }) => {
    expect(() => mcpCallArguments(exchangeReceipt([{ received: message }]))).toThrow('exact tool arguments')
  })

  it('reads the receipt file through readMcpCallArguments', () => {
    writeFileSync(path, JSON.stringify(exchangeReceipt([{ received: { method: 'tools/call', params: { name: 'inspect', arguments: { count: 0 } } } }])))
    expect(readMcpCallArguments(path)).toEqual([{ name: 'inspect', arguments: { count: 0 } }])
  })
})

function nativeExchange(id: string | number = 0, result: unknown = { structuredContent: { count: 0, enabled: false, text: '' } }): McpExchangeEntry[] {
  return [
    { received: { jsonrpc: '2.0', id, method: 'tools/call', params: { name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, _meta: { progressToken: 0 } } } },
    { sent: { jsonrpc: '2.0', id, result } },
  ]
}

describe('mcpCallExchange', () => {
  it('pairs one actual request and reply and preserves their original fields', () => {
    const entries = nativeExchange()
    const before = structuredClone(entries)
    const exchange = mcpCallExchange(exchangeReceipt(entries))
    expect(exchange).toMatchObject({ id: 0, name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, result: { structuredContent: { count: 0, enabled: false, text: '' } } })
    expect(exchange.request).toBe(messageOf(entries[0]))
    expect(exchange.reply).toBe(messageOf(entries[1]))
    expect(entries).toEqual(before)
  })

  it.each(['', '0', 'native-call-한글', -1, Number.MAX_SAFE_INTEGER])('keeps a native request ID and its exact type: %j', (id) => {
    expect(mcpCallExchange(exchangeReceipt(nativeExchange(id))).id).toBe(id)
  })

  it.each([0, false, null, '', { structuredContent: { count: 0, enabled: false, nullable: null, text: '' } }])('preserves explicit native result presence: %j', (result) => {
    expect(mcpCallExchange(exchangeReceipt(nativeExchange(0, result))).result).toBe(result)
  })

  it('ignores unrelated initialize and catalog replies, and the requests that the server sends', () => {
    const entries: McpExchangeEntry[] = [
      { received: { id: 1, method: 'initialize', params: {} } },
      { sent: { id: 1, result: { capabilities: {} } } },
      { received: { id: 2, method: 'tools/list', params: {} } },
      { sent: { id: 2, result: { tools: [] } } },
      { sent: { id: 0, method: 'elicitation/create', params: {} } },
      ...nativeExchange(),
    ]
    expect(mcpCallExchange(exchangeReceipt(entries)).id).toBe(0)
  })

  const [call, reply] = nativeExchange() as [McpExchangeEntry, McpExchangeEntry]
  it.each([
    { label: 'no tools/call', error: 'The native MCP receipt must contain exactly one tool call.', entries: [{ received: { id: 0, method: 'tools/list' } }] },
    { label: 'a tools/call that the server sent', error: 'The native MCP receipt must contain exactly one tool call.', entries: [{ sent: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: {} } } }, reply] },
    { label: 'two tools/call records', error: 'The native MCP receipt must contain exactly one tool call.', entries: [call, reply, call] },
    { label: 'a missing reply', error: 'The native MCP tool call must have exactly one matching reply.', entries: [call] },
    { label: 'duplicate matching replies', error: 'The native MCP tool call must have exactly one matching reply.', entries: [call, reply, reply] },
    { label: 'a foreign reply', error: 'The native MCP tool call must have exactly one matching reply.', entries: [call, { sent: { id: 1, result: {} } }] },
    { label: 'a different ID type', error: 'The native MCP tool call must have exactly one matching reply.', entries: [call, { sent: { id: '0', result: {} } }] },
    { label: 'an absent request ID', error: 'The MCP receipt tool request ID must be a string or a safe integer.', entries: [{ received: { method: 'tools/call', params: { name: 'inspect', arguments: {} } } }, reply] },
    { label: 'an unsafe request ID', error: 'The MCP receipt tool request ID must be a string or a safe integer.', entries: nativeExchange(Number.MAX_SAFE_INTEGER + 1) },
    { label: 'an absent params object', error: 'The native MCP call receipt lacks exact tool arguments.', entries: [{ received: { id: 0, method: 'tools/call' } }, reply] },
    { label: 'a blank tool name', error: 'The native MCP call receipt lacks exact tool arguments.', entries: [{ received: { id: 0, method: 'tools/call', params: { name: ' ', arguments: {} } } }, reply] },
    { label: 'null arguments', error: 'The native MCP call receipt lacks exact tool arguments.', entries: [{ received: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: null } } }, reply] },
    { label: 'array arguments', error: 'The native MCP call receipt lacks exact tool arguments.', entries: [{ received: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: [] } } }, reply] },
    { label: 'an absent result', error: 'The native MCP tool reply contains no result.', entries: [call, { sent: { id: 0 } }] },
    { label: 'an undefined result', error: 'The native MCP tool reply contains no result.', entries: [call, { sent: { id: 0, result: undefined } }] },
    { label: 'a protocol error', error: 'The native MCP tool call returned a protocol error.', entries: [call, { sent: { id: 0, error: { code: -32602, message: 'Invalid params' } } }] },
    { label: 'a contradictory result and error', error: 'The native MCP tool call returned a protocol error.', entries: [call, { sent: { id: 0, result: {}, error: { code: -32602, message: 'Invalid params' } } }] },
    { label: 'an invalid reply ID', error: 'The MCP receipt reply ID must be a string or a safe integer.', entries: [call, { sent: { id: null, result: {} } }] },
  ] satisfies Array<{ label: string, error: string, entries: McpExchangeEntry[] }>)('refuses $label as one actual tool exchange', ({ entries, error }) => {
    expect(() => mcpCallExchange(exchangeReceipt(entries))).toThrow(error)
  })

  it('reads the receipt file through readMcpCallExchange', () => {
    const entries = nativeExchange()
    writeFileSync(path, JSON.stringify(exchangeReceipt(entries)))
    expect(readMcpCallExchange(path)).toEqual({ id: 0, name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, result: { structuredContent: { count: 0, enabled: false, text: '' } }, request: messageOf(entries[0]), reply: messageOf(entries[1]) })
  })

  it('preserves file-read and malformed-JSON failures of readMcpCallExchange', () => {
    expect(() => readMcpCallExchange(path)).toThrow(expect.objectContaining({ code: 'ENOENT' }))
    writeFileSync(path, '{')
    expect(() => readMcpCallExchange(path)).toThrow(SyntaxError)
  })
})
