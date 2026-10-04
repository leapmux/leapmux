import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { parseMcpCallExchange, readMcpCallArguments, readMcpCallExchange } from './mcpRequestReceipt'

let directory: string
let path: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'mcp-call-receipt-'))
  path = join(directory, 'receipt.json')
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('readMcpCallArguments', () => {
  it('keeps exact native zero, false, empty, and Unicode values in call order', () => {
    const argumentsList = [
      { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } },
      { name: 'inspect_한글', arguments: { text: '출력' } },
    ]
    writeFileSync(path, JSON.stringify([
      { request: { method: 'tools/list', params: {} } },
      { reply: { id: 0, result: {} } },
      ...argumentsList.map(params => ({ request: { method: 'tools/call', params } })),
    ]))
    expect(readMcpCallArguments(path)).toEqual(argumentsList)
  })

  it('accepts an empty actual call inventory', () => {
    writeFileSync(path, '[]')
    expect(readMcpCallArguments(path)).toEqual([])
  })

  it('keeps exact arguments when the native transport adds progress metadata', () => {
    const argumentsRecord = { count: 0, enabled: false, text: '' }
    writeFileSync(path, JSON.stringify([{ request: { method: 'tools/call', params: { name: 'inspect', arguments: argumentsRecord, _meta: { progressToken: 2 } } } }]))
    expect(readMcpCallArguments(path)).toEqual([{ name: 'inspect', arguments: argumentsRecord }])
  })

  it.each([null, {}, [null], [{ request: false }], [{ request: { method: 'tools/call' } }], [{ request: { method: 'tools/call', params: { name: 'inspect', arguments: null } } }]])('rejects a malformed actual receipt: %j', (receipt) => {
    writeFileSync(path, JSON.stringify(receipt))
    expect(() => readMcpCallArguments(path)).toThrow()
  })

  it('preserves file-read and malformed-JSON failures', () => {
    expect(() => readMcpCallArguments(path)).toThrow()
    writeFileSync(path, '{')
    expect(() => readMcpCallArguments(path)).toThrow()
  })
})

function nativeExchange(id: string | number = 0, result: unknown = { structuredContent: { count: 0, enabled: false, text: '' } }): Array<Record<string, unknown>> {
  return [
    { request: { jsonrpc: '2.0', id, method: 'tools/call', params: { name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, _meta: { progressToken: 0 } } } },
    { reply: { jsonrpc: '2.0', id, result } },
  ]
}

describe('parseMcpCallExchange', () => {
  it('pairs one actual request and reply and preserves their original fields', () => {
    const input = nativeExchange()
    const before = structuredClone(input)
    const exchange = parseMcpCallExchange(input)
    expect(exchange).toMatchObject({ id: 0, name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, result: { structuredContent: { count: 0, enabled: false, text: '' } } })
    expect(exchange.request).toBe(input[0]!.request)
    expect(exchange.reply).toBe(input[1]!.reply)
    expect(input).toEqual(before)
  })

  it.each(['', '0', 'native-call-한글', -1, Number.MAX_SAFE_INTEGER])('keeps a native request ID and its exact type: %j', (id) => {
    expect(parseMcpCallExchange(nativeExchange(id)).id).toBe(id)
  })

  it.each([0, false, null, '', { structuredContent: { count: 0, enabled: false, nullable: null, text: '' } }])('preserves explicit native result presence: %j', (result) => {
    expect(parseMcpCallExchange(nativeExchange(0, result)).result).toBe(result)
  })

  it('ignores unrelated initialize and catalog replies when it pairs the tool request', () => {
    const input = [
      { request: { id: 1, method: 'initialize', params: {} } },
      { reply: { id: 1, result: { capabilities: {} } } },
      { request: { id: 2, method: 'tools/list', params: {} } },
      { reply: { id: 2, result: { tools: [] } } },
      ...nativeExchange(),
    ]
    expect(parseMcpCallExchange(input).id).toBe(0)
  })

  it.each([
    { label: 'an absent list', value: undefined },
    { label: 'a non-array root', value: {} },
    { label: 'a malformed entry', value: [null] },
    { label: 'an invalid request', value: [{ request: false }] },
    { label: 'an invalid reply', value: [...nativeExchange(), { reply: false }] },
    { label: 'no tools/call', value: [{ request: { id: 0, method: 'tools/list' } }] },
    { label: 'two tools/call records', value: [...nativeExchange(), nativeExchange()[0]] },
    { label: 'a missing reply', value: [nativeExchange()[0]] },
    { label: 'duplicate matching replies', value: [...nativeExchange(), nativeExchange()[1]] },
    { label: 'a foreign reply', value: [nativeExchange()[0], { reply: { id: 1, result: {} } }] },
    { label: 'a different ID type', value: [nativeExchange()[0], { reply: { id: '0', result: {} } }] },
    { label: 'an absent request ID', value: [{ request: { method: 'tools/call', params: { name: 'inspect', arguments: {} } } }, nativeExchange()[1]] },
    { label: 'an unsafe request ID', value: nativeExchange(Number.MAX_SAFE_INTEGER + 1) },
    { label: 'an absent params object', value: [{ request: { id: 0, method: 'tools/call' } }, nativeExchange()[1]] },
    { label: 'a blank tool name', value: [{ request: { id: 0, method: 'tools/call', params: { name: ' ', arguments: {} } } }, nativeExchange()[1]] },
    { label: 'null arguments', value: [{ request: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: null } } }, nativeExchange()[1]] },
    { label: 'array arguments', value: [{ request: { id: 0, method: 'tools/call', params: { name: 'inspect', arguments: [] } } }, nativeExchange()[1]] },
    { label: 'an absent result', value: [nativeExchange()[0], { reply: { id: 0 } }] },
    { label: 'an undefined result', value: [nativeExchange()[0], { reply: { id: 0, result: undefined } }] },
    { label: 'a protocol error', value: [nativeExchange()[0], { reply: { id: 0, error: { code: -32602, message: 'Invalid params' } } }] },
    { label: 'a contradictory result and error', value: [nativeExchange()[0], { reply: { id: 0, result: {}, error: { code: -32602, message: 'Invalid params' } } }] },
    { label: 'an invalid reply ID', value: [nativeExchange()[0], { reply: { id: null, result: {} } }] },
  ])('refuses $label as one actual tool exchange', ({ value }) => {
    expect(() => parseMcpCallExchange(value)).toThrow()
  })
})

describe('readMcpCallExchange', () => {
  it('reads the existing flat native server receipt', () => {
    const input = nativeExchange()
    writeFileSync(path, JSON.stringify(input))
    expect(readMcpCallExchange(path)).toEqual({ id: 0, name: 'inspect', arguments: { count: 0, enabled: false, text: '' }, result: { structuredContent: { count: 0, enabled: false, text: '' } }, request: input[0]!.request, reply: input[1]!.reply })
  })

  it('preserves file-read and malformed-JSON failures', () => {
    expect(() => readMcpCallExchange(path)).toThrow()
    writeFileSync(path, '{')
    expect(() => readMcpCallExchange(path)).toThrow()
  })
})
