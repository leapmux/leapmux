import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpFormServer } from './mcpFormServer'

const expectedEchoArguments = { query: 'probe', limit: 0, tail: 'END_MCP_ARGUMENTS' }

function echoResult(args: unknown, configureExpected = true): string | undefined {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-form-server-'))
  try {
    const options = configureExpected ? { expectedEchoArguments } : {}
    const script = writeMcpFormServer(directory, 'server.mjs', options)
    const request = { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'echo', arguments: args } }
    const output = execFileSync(process.execPath, [script], {
      input: `${JSON.stringify(request)}\n`,
      encoding: 'utf8',
      timeout: 5000,
    })
    const reply = JSON.parse(output) as { result?: { content?: Array<{ text?: string }> } }
    return reply.result?.content?.[0]?.text
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpFormServer', () => {
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
