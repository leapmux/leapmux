import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { describe, expect, it } from 'vitest'
import { writeMcpStdioRuntime } from './mcpStdioRuntime'

function execute(lines: readonly string[]) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-stdio-unit-'))
  try {
    const runtime = writeMcpStdioRuntime(directory)
    const script = join(directory, 'server.mjs')
    writeFileSync(script, `
import { readMcpMessages, sendMcpMessage } from ${JSON.stringify(pathToFileURL(runtime).href)};
for await (const request of readMcpMessages()) {
  if (request.method === 'echo') sendMcpMessage({ jsonrpc: '2.0', id: request.id, result: request.params });
  else sendMcpMessage({ jsonrpc: '2.0', id: request.id, error: { code: -32601, message: 'Method not supported.' } });
}
`)
    const output = execFileSync(process.execPath, [script], { input: `${lines.join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
    return output.trim() ? output.trim().split('\n').map(line => JSON.parse(line)) : []
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpStdioRuntime', () => {
  it('preserves ID zero, empty values, Unicode, and reply order', () => {
    const requests = [
      { jsonrpc: '2.0', id: 0, method: 'echo', params: { count: 0, enabled: false, text: '' } },
      { jsonrpc: '2.0', id: '출력', method: 'echo', params: { text: '실제 결과' } },
    ]
    expect(execute(requests.map(request => JSON.stringify(request)))).toEqual(requests.map(request => ({ jsonrpc: '2.0', id: request.id, result: request.params })))
  })

  it('reports malformed JSON and continues with the next valid request', () => {
    const replies = execute(['{broken', JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'echo', params: '' })])
    expect(replies[0].error.code).toBe(-32700)
    expect(replies[1]).toEqual({ jsonrpc: '2.0', id: 1, result: '' })
  })

  it('ignores notifications and non-object input without invoking a scenario', () => {
    expect(execute(['null', '[]', '0', JSON.stringify({ method: 'notifications/initialized' })])).toEqual([])
  })

  it.each([null, false, {}, 0.5, Number.MAX_SAFE_INTEGER + 1])('refuses an invalid request ID: %j', (id) => {
    expect(execute([JSON.stringify({ jsonrpc: '2.0', id, method: 'echo' })])[0].error.code).toBe(-32600)
  })

  it('keeps unsupported method handling in the scenario', () => {
    expect(execute([JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'unknown' })])[0].error.code).toBe(-32601)
  })

  it.each([
    { jsonrpc: '1.0', id: 1, method: 'echo' },
    { jsonrpc: '2.0', id: 1, method: 0 },
    { jsonrpc: '2.0', id: 1, method: 'echo', result: {} },
    { jsonrpc: '2.0', id: 1, result: {}, error: { code: -1, message: 'failed' } },
    { jsonrpc: '2.0', id: 1 },
    { jsonrpc: '2.0', id: 1, error: null },
    { jsonrpc: '2.0', id: 1, error: false },
    { jsonrpc: '2.0', id: 1, error: { message: 'failed' } },
    { jsonrpc: '2.0', id: 1, error: { code: -1, message: 0 } },
  ])('refuses a malformed JSON-RPC envelope: %j', (message) => {
    expect(execute([JSON.stringify(message)])[0].error.code).toBe(-32600)
  })
})
