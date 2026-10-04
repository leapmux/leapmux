import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpConfirmationServer } from './mcpConfirmationServer'

function execute(requests: unknown[]) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-confirmation-unit-'))
  try {
    const script = writeMcpConfirmationServer(directory)
    const output = execFileSync(process.execPath, [script], { input: `${requests.map(request => JSON.stringify(request)).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
    return output.trim().split('\n').map(line => JSON.parse(line))
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpConfirmationServer', () => {
  it.each([{}, { action: 'typo' }, { action: 0 }])('refuses a malformed confirmation action: %j', (result) => {
    const replies = execute([
      { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'ask', arguments: {} } },
      { jsonrpc: '2.0', id: 'probe-confirm', result },
    ])
    const completed = replies.find(reply => reply.result)
    expect(completed?.result.isError).toBe(true)
    expect(completed?.result.content[0].text).toBe('FORM_ROUND_TRIP_FAILED')
  })

  it('correlates two concurrent confirmations that return in reverse order', () => {
    const replies = execute([
      { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'ask', arguments: {} } },
      { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'ask', arguments: {} } },
      { jsonrpc: '2.0', id: 'probe-confirm-2', result: { action: 'decline' } },
      { jsonrpc: '2.0', id: 'probe-confirm', result: { action: 'accept' } },
    ])
    expect(replies.filter(reply => reply.method === 'elicitation/create').map(reply => reply.id)).toEqual(['probe-confirm', 'probe-confirm-2'])
    expect(replies.filter(reply => reply.result).map(reply => ({ id: reply.id, text: reply.result.content[0].text }))).toEqual([
      { id: 1, text: 'MCP_CONFIRM_DECLINED' },
      { id: 0, text: 'MCP_CONFIRM_ACCEPTED' },
    ])
  })

  it('does not complete a tool for an absent or repeated confirmation ID', () => {
    const replies = execute([
      { jsonrpc: '2.0', id: 'unknown', result: { action: 'accept' } },
      { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name: 'ask', arguments: {} } },
      { jsonrpc: '2.0', id: 'probe-confirm', result: { action: 'accept' } },
      { jsonrpc: '2.0', id: 'probe-confirm', result: { action: 'accept' } },
    ])
    expect(replies.filter(reply => reply.result)).toHaveLength(1)
    expect(replies.find(reply => reply.result)?.id).toBe(0)
  })
})
