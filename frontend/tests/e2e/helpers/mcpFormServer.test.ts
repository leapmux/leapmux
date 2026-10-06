import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { MCP_FORM_SERVER_NAME, writeMcpFormServer } from './mcpFormServer'
import { mcpCallArguments, readMcpServerReceipt } from './mcpServerReceipt'

const expectedEchoArguments = { query: 'probe', limit: 0, tail: 'END_MCP_ARGUMENTS' }

function echoResult(args: unknown, configureExpected = true): string | undefined {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-form-server-'))
  try {
    const options = configureExpected ? { expectedEchoArguments } : {}
    const { script } = writeMcpFormServer(directory, 'server.mjs', options)
    const request = { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'echo', arguments: args } }
    const output = execFileSync(process.execPath, [script], {
      input: `${JSON.stringify(request)}\n`,
      encoding: 'utf8',
      timeout: 30000,
    })
    const reply = JSON.parse(output) as { result?: { content?: Array<{ text?: string }> } }
    return reply.result?.content?.[0]?.text
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

function formRoundTrip(reply: Record<string, unknown>, options: { concurrent?: boolean, initialize?: boolean } = {}) {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-form-receipt-'))
  try {
    const receiptLog = join(directory, 'receipt.json')
    const { script } = writeMcpFormServer(directory, 'server.mjs', { receiptLog })
    const requests = [
      ...(options.initialize === false ? [] : [{ jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: { roots: { listChanged: false } } } }]),
      { jsonrpc: '2.0', id: 'catalog', method: 'tools/list', params: {} },
      { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'ask', arguments: {} } },
      ...(options.concurrent
        ? [
            { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'ask', arguments: {} } },
            { jsonrpc: '2.0', id: 'probe-form-2', result: { action: 'decline' } },
          ]
        : []),
      { jsonrpc: '2.0', id: 'probe-form', ...reply },
    ]
    const output = execFileSync(process.execPath, [script], { input: `${requests.map(request => JSON.stringify(request)).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
    return {
      replies: output.trim().split('\n').map(line => JSON.parse(line) as { id?: string | number, method?: string, result?: { content?: Array<{ text?: string }>, isError?: boolean } }),
      receipt: readMcpServerReceipt(receiptLog),
    }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpFormServer', () => {
  it('keeps a native elicitation error distinct from a malformed accepted result', () => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'mcp-form-refusal-'))
    try {
      const { script } = writeMcpFormServer(directory, 'server.mjs')
      const requests = [
        { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {} } },
        { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'ask', arguments: {} } },
        { jsonrpc: '2.0', id: 'probe-form', error: { code: -32601, message: 'Method not found' } },
      ]
      const output = execFileSync(process.execPath, [script], { input: `${requests.map(request => JSON.stringify(request)).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
      const replies = output.trim().split('\n').map(line => JSON.parse(line))
      const result = replies.find(reply => reply.id === 2)
      expect(result.result.content[0].text).toBe('FORM_ROUND_TRIP_REFUSED: -32601 Method not found')
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('reports the name that it returns in its launch shape', () => {
    const { replies } = formRoundTrip({ result: { action: 'decline' } })
    const initialized = replies.find(reply => reply.id === 0) as { result?: { serverInfo?: { name?: string } } } | undefined
    expect(initialized?.result?.serverInfo?.name).toBe(MCP_FORM_SERVER_NAME)
  })

  it('returns a launch through the Node.js runtime of the test process', () => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'mcp-form-launch-'))
    try {
      const server = writeMcpFormServer(directory, 'server.mjs')
      expect(server).toEqual({ name: MCP_FORM_SERVER_NAME, script: join(directory, 'server.mjs'), command: process.execPath, args: [join(directory, 'server.mjs')] })
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('records the raw exchange of each received and sent message in order', () => {
    const { receipt } = formRoundTrip({ result: { action: 'decline' } })
    const directions = receipt.exchange.map(entry => 'received' in entry ? `received ${String(entry.received.id)}` : `sent ${String(entry.sent.id)}`)
    expect(directions).toEqual(['received 0', 'sent 0', 'received catalog', 'sent catalog', 'received 2', 'sent probe-form', 'received probe-form', 'sent 2'])
    expect(mcpCallArguments(receipt)).toEqual([{ name: 'ask', arguments: {} }])
  })

  it('records actual initialization, catalog, and accepted zero and false form values', () => {
    const { replies, receipt } = formRoundTrip({ result: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } })
    expect(replies.find(reply => reply.id === 2)?.result).toEqual({ content: [{ type: 'text', text: 'FORM_ROUND_TRIP_OK' }] })
    expect(receipt.initializeCapabilities).toEqual({ roots: { listChanged: false } })
    expect(receipt.toolCatalogs).toMatchObject([{ id: 'catalog', tools: [{ name: 'ask', inputSchema: { type: 'object' } }, { name: 'echo' }] }])
    expect(receipt.elicitationRequests).toMatchObject([{ id: 'probe-form', toolRequestId: 2, params: { mode: 'form' } }])
    expect(receipt.elicitationReplies).toEqual([{ id: 'probe-form', kind: 'result', result: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } }])
    expect(receipt.toolResults).toEqual([{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false }])
  })

  it.each([
    { action: 'decline', expected: 'FORM_ROUND_TRIP_DECLINED' },
    { action: 'cancel', expected: 'FORM_ROUND_TRIP_CANCELLED' },
  ])('keeps an actual $action outcome separate from an error', ({ action, expected }) => {
    const { receipt } = formRoundTrip({ result: { action } })
    expect(receipt.elicitationReplies).toEqual([{ id: 'probe-form', kind: 'result', result: { action } }])
    expect(receipt.toolResults).toEqual([{ id: 2, tool: 'ask', text: expected, isError: false }])
  })

  it('records the exact native refusal code, message, data, and originating tool result', () => {
    const error = { code: -32601, message: 'Server does not support elicitation/create', data: { supported: false } }
    const { receipt } = formRoundTrip({ error })
    expect(receipt.elicitationReplies).toEqual([{ id: 'probe-form', kind: 'error', error }])
    expect(receipt.toolResults).toEqual([{ id: 2, tool: 'ask', text: `FORM_ROUND_TRIP_REFUSED: ${error.code} ${error.message}`, isError: true }])
  })

  it('correlates concurrent form replies that arrive in reverse order', () => {
    const { receipt } = formRoundTrip({ result: { action: 'accept', content: { count: 0, enabled: false, color: 'b' } } }, { concurrent: true })
    expect(receipt.elicitationRequests.map(request => [request.id, request.toolRequestId])).toEqual([['probe-form', 2], ['probe-form-2', 3]])
    expect(receipt.toolResults).toEqual([
      { id: 3, tool: 'ask', text: 'FORM_ROUND_TRIP_DECLINED', isError: false },
      { id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_OK', isError: false },
    ])
  })

  it('does not finish a pending tool from a reply with a different ID', () => {
    const { replies, receipt } = formRoundTrip({ id: 'unrelated', error: { code: -32601, message: 'Method not found' } })
    expect(receipt.elicitationRequests).toHaveLength(1)
    expect(receipt.elicitationReplies).toEqual([])
    expect(receipt.toolResults).toEqual([])
    expect(replies.some(reply => reply.id === 2)).toBe(false)
  })

  it('keeps absent initialization distinct from empty capabilities', () => {
    expect(formRoundTrip({ result: { action: 'decline' } }, { initialize: false }).receipt.initializeCapabilities).toBeNull()
  })

  it.each([
    { count: 1, enabled: false, color: 'b' },
    { count: -1, enabled: false, color: 'b' },
    { count: 1000000, enabled: false, color: 'b' },
    { count: 0, enabled: true, color: 'b' },
    { count: 0, enabled: false, color: 'r' },
    { count: 0, color: 'b' },
  ])('rejects invalid accepted content $count $enabled $color', (content) => {
    expect(formRoundTrip({ result: { action: 'accept', content } }).receipt.toolResults).toEqual([{ id: 2, tool: 'ask', text: 'FORM_ROUND_TRIP_FAILED', isError: true }])
  })

  it('accepts the exact echo arguments', () => {
    expect(echoResult(expectedEchoArguments)).toBe('PERMISSION_ACCEPTED')
  })

  it.each([
    { label: 'a missing query', args: { limit: 0, tail: 'END_MCP_ARGUMENTS' } },
    { label: 'a changed query', args: { ...expectedEchoArguments, query: 'different' } },
    { label: 'a changed limit', args: { ...expectedEchoArguments, limit: 1 } },
    { label: 'a changed tail', args: { ...expectedEchoArguments, tail: 'different' } },
    { label: 'an extra field', args: { ...expectedEchoArguments, extra: true } },
    { label: 'null arguments', args: null },
  ])('rejects $label', ({ args }) => {
    expect(echoResult(args)).toBe('PERMISSION_ARGUMENTS_FAILED')
  })

  it('rejects an echo call without configured expected arguments', () => {
    expect(echoResult(expectedEchoArguments, false)).toBe('PERMISSION_ARGUMENTS_FAILED')
  })
})
