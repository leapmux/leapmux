import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpEchoServer } from './mcpEchoServer'
import { readMcpServerReceipt } from './mcpServerReceipt'

function executeEcho(argumentsValue: unknown) {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-echo-receipt-'))
  try {
    const receiptLog = join(directory, 'receipt.json')
    const script = writeMcpEchoServer(directory, { receiptLog })
    const requests = [
      { jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: { roots: { listChanged: false } } } },
      { jsonrpc: '2.0', id: 1, method: 'tools/list' },
      { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'echo', arguments: argumentsValue } },
    ]
    const output = execFileSync(process.execPath, [script], { input: `${requests.map(request => JSON.stringify(request)).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
    return { receipt: readMcpServerReceipt(receiptLog), replies: output.trim().split('\n').map(line => JSON.parse(line) as { id: number, result?: { content: Array<{ text: string }> }, error?: { code: number } }) }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpEchoServer', () => {
  it.each(['ACTUAL_NATIVE_ECHO', '', 'Native UTF-8 零 🔒'])('records the actual catalog and computed native result for %s', (value) => {
    const { receipt, replies } = executeEcho({ value })
    expect(receipt.initializeCapabilities).toEqual({ roots: { listChanged: false } })
    expect(receipt.toolCatalogs).toMatchObject([{ id: 1, tools: [{ name: 'echo', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }] }])
    expect(receipt.toolResults).toEqual([{ id: 2, tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }])
    expect(replies.find(reply => reply.id === 2)?.result?.content).toEqual([{ type: 'text', text: `MCP_ECHO:${value}` }])
    expect(receipt.elicitationRequests).toEqual([])
    expect(receipt.elicitationReplies).toEqual([])
  })

  it.each([null, {}, { value: 0 }, { value: false }])('refuses invalid echo arguments %j without creating a success result', (args) => {
    const { receipt, replies } = executeEcho(args)
    expect(replies.find(reply => reply.id === 2)?.error?.code).toBe(-32602)
    expect(receipt.toolResults).toEqual([])
  })
})
