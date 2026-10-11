import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { writeMcpNameFormServer } from './mcpNameFormServer'

let directory: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'mcp-name-form-unit-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

interface Reply {
  id: string | number
  method?: string
  params?: { requestedSchema?: { properties?: Record<string, unknown> } }
  result?: { serverInfo?: { name: string }, tools?: Array<{ name: string }>, content?: Array<{ text: string }> }
  error?: { code: number }
}

/** Run the server over stdio with `messages`, and return its replies and whether it wrote its ready file. */
function run(messages: unknown[], options = { serverName: 'probe', expectedName: 'kiro-e2e' }) {
  const server = writeMcpNameFormServer(directory, options)
  const output = execFileSync(server.command, [...server.args], { input: `${messages.map(message => JSON.stringify(message)).join('\n')}\n`, encoding: 'utf8', timeout: 30000 })
  return { server, replies: output.trim().split('\n').map(line => JSON.parse(line) as Reply), ready: existsSync(server.ready) }
}

const call = { jsonrpc: '2.0', id: 7, method: 'tools/call', params: { name: 'ask', arguments: {} } }

describe('writeMcpNameFormServer', () => {
  it('reports the configured name and lists its one tool, which writes the ready file', () => {
    const { server, replies, ready } = run([
      { jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-03-26' } },
      { jsonrpc: '2.0', id: 1, method: 'tools/list' },
    ], { serverName: 'form_probe', expectedName: 'grok-e2e' })
    expect(server.name).toBe('form_probe')
    expect(replies.find(reply => reply.id === 0)?.result?.serverInfo?.name).toBe('form_probe')
    expect(replies.find(reply => reply.id === 1)?.result?.tools?.map(tool => tool.name)).toEqual(['ask'])
    expect(ready).toBe(true)
  })

  it('writes no ready file before an agent lists its tools', () => {
    expect(run([{ jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-03-26' } }]).ready).toBe(false)
  })

  it('asks for one name field and accepts the expected name', () => {
    const { replies } = run([call, { jsonrpc: '2.0', id: 'probe-form', result: { action: 'accept', content: { name: 'kiro-e2e' } } }])
    const form = replies.find(reply => reply.method === 'elicitation/create')
    expect(form?.id).toBe('probe-form')
    expect(Object.keys(form?.params?.requestedSchema?.properties ?? {})).toEqual(['name'])
    expect(replies.find(reply => reply.id === 7)?.result?.content?.[0]?.text).toBe('FORM_ROUND_TRIP_OK')
  })

  it.each([
    { label: 'another name', result: { action: 'accept', content: { name: 'other' } } },
    { label: 'a decline', result: { action: 'decline' } },
    { label: 'a cancel', result: { action: 'cancel' } },
  ])('fails the round trip for $label', ({ result }) => {
    const { replies } = run([call, { jsonrpc: '2.0', id: 'probe-form', result }])
    expect(replies.find(reply => reply.id === 7)?.result?.content?.[0]?.text).toBe('FORM_ROUND_TRIP_FAILED')
  })

  it('refuses an unsupported method', () => {
    expect(run([{ jsonrpc: '2.0', id: 3, method: 'resources/list' }]).replies[0]?.error?.code).toBe(-32601)
  })

  it('refuses an empty expected name and an invalid server name before it writes a script', () => {
    expect(() => writeMcpNameFormServer(directory, { serverName: 'probe', expectedName: '' })).toThrow('name that the reader types')
    expect(() => writeMcpNameFormServer(directory, { serverName: 'form probe', expectedName: 'x' })).toThrow('MCP server name')
    expect(existsSync(join(directory, 'mcp-name-form.mjs'))).toBe(false)
  })
})
