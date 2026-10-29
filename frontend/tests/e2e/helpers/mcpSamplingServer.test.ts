import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { MCP_SAMPLING_SERVER_NAME, writeMcpSamplingServer } from './mcpSamplingServer'
import { readMcpServerReceipt } from './mcpServerReceipt'

function exchange(requests: Record<string, unknown>[]) {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-sampling-'))
  try {
    const receiptLog = join(directory, 'receipt.json')
    const server = writeMcpSamplingServer(directory, { receiptLog, prompt: 'SCRIPTED_SAMPLING_PROMPT' })
    const output = execFileSync(server.command, server.args, { input: `${requests.map(request => JSON.stringify({ jsonrpc: '2.0', ...request })).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
    return {
      replies: output.trim().split('\n').filter(Boolean).map(line => JSON.parse(line)),
      receipt: readMcpServerReceipt(receiptLog),
    }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpSamplingServer', () => {
  it('refuses a blank prompt before writing a server', () => {
    expect(() => writeMcpSamplingServer(resolve(import.meta.dirname, '../../../..', '.tmp', 'unused-sampling'), { prompt: ' ' })).toThrow('nonempty prompt')
  })

  it('announces its tool and preserves the negotiated protocol', () => {
    const { replies } = exchange([
      {
        id: 0,
        method: 'initialize',
        params: {
          protocolVersion: '2025-06-18',
          capabilities: { sampling: {} },
        },
      },
      { id: 1, method: 'tools/list' },
    ])
    expect(replies[0].result).toMatchObject({
      protocolVersion: '2025-06-18',
      serverInfo: { name: MCP_SAMPLING_SERVER_NAME },
    })
    expect(replies[1].result.tools).toEqual([
      expect.objectContaining({
        name: 'sample',
        inputSchema: { type: 'object', properties: {}, additionalProperties: false },
      }),
    ])
  })

  it('returns the model response through the original tool call, including id zero', () => {
    const { replies, receipt } = exchange([
      {
        id: 0,
        method: 'tools/call',
        params: { name: 'sample', arguments: {} },
      },
      {
        id: 'sampling-1',
        result: {
          role: 'assistant',
          content: { type: 'text', text: 'SAMPLED' },
          model: 'scripted',
        },
      },
    ])
    expect(replies[0]).toMatchObject({
      method: 'sampling/createMessage',
      params: {
        messages: [
          {
            role: 'user',
            content: { type: 'text', text: 'SCRIPTED_SAMPLING_PROMPT' },
          },
        ],
        maxTokens: 32,
      },
    })
    expect(replies[1]).toEqual({
      jsonrpc: '2.0',
      id: 0,
      result: {
        content: [
          { type: 'text', text: 'SAMPLED' },
        ],
      },
    })
    expect(receipt.toolResults).toEqual([
      {
        id: 0,
        tool: 'sample',
        text: 'SAMPLED',
        isError: false,
      },
    ])
  })

  it('returns a refusal as a failed tool result', () => {
    const { replies } = exchange([
      {
        id: 'tool',
        method: 'tools/call',
        params: { name: 'sample' },
      },
      {
        id: 'sampling-1',
        error: { code: -32000, message: 'The reader refused sampling.' },
      },
    ])
    expect(replies[1].result).toEqual({
      isError: true,
      content: [
        { type: 'text', text: 'MCP_SAMPLING_REFUSED: The reader refused sampling.' },
      ],
    })
  })

  it('treats a native cancelled sampling result as a refusal', () => {
    // copilot/round2/mcp-server.log records this cancelled sampling result.
    const { replies } = exchange([
      {
        id: 'tool',
        method: 'tools/call',
        params: { name: 'sample' },
      },
      {
        id: 'sampling-1',
        result: {
          model: '',
          stopReason: 'cancelled',
          role: 'user',
          content: { type: 'text', text: 'The user cancelled the request.' },
        },
      },
    ])
    expect(replies[1].result).toEqual({
      isError: true,
      content: [
        { type: 'text', text: 'MCP_SAMPLING_REFUSED: The user cancelled the request.' },
      ],
    })
  })

  it('correlates concurrent requests and ignores a repeated response', () => {
    const { replies } = exchange([
      {
        id: 'first',
        method: 'tools/call',
        params: { name: 'sample' },
      },
      {
        id: 'second',
        method: 'tools/call',
        params: { name: 'sample' },
      },
      {
        id: 'sampling-2',
        result: {
          content: [
            { type: 'text', text: 'SECOND' },
          ],
        },
      },
      {
        id: 'sampling-1',
        result: {
          content: { type: 'text', text: 'FIRST' },
        },
      },
      {
        id: 'sampling-1',
        result: {
          content: { type: 'text', text: 'DUPLICATE' },
        },
      },
    ])
    expect(replies.slice(2)).toEqual([
      {
        jsonrpc: '2.0',
        id: 'second',
        result: {
          content: [
            { type: 'text', text: 'SECOND' },
          ],
        },
      },
      {
        jsonrpc: '2.0',
        id: 'first',
        result: {
          content: [
            { type: 'text', text: 'FIRST' },
          ],
        },
      },
    ])
  })

  it.each([
    {},
    { content: null },
    {
      content: { type: 'text', text: '' },
    },
  ])('rejects a response with no text: %j', (result) => {
    const { replies } = exchange([
      {
        id: 1,
        method: 'tools/call',
        params: { name: 'sample' },
      },
      { id: 'sampling-1', result },
    ])
    expect(replies[1].result.isError).toBe(true)
    expect(replies[1].result.content[0].text).toContain('MCP_SAMPLING_FAILED')
  })

  it.each([
    { name: 'other' },
    { name: 'sample', arguments: null },
    {
      name: 'sample',
      arguments: { unexpected: true },
    },
  ])('rejects invalid tool arguments: %j', (params) => {
    const { replies } = exchange([
      { id: 1, method: 'tools/call', params },
    ])
    expect(replies).toEqual([
      {
        jsonrpc: '2.0',
        id: 1,
        error: { code: -32602, message: 'The sampling tool or its arguments are invalid.' },
      },
    ])
  })
})
