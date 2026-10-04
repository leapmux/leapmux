import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpPermissionServer } from './mcpPermissionServer'

function execute(name: string, args: unknown) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-permission-unit-'))
  try {
    const server = writeMcpPermissionServer(directory)
    const request = { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name, arguments: args } }
    const reply = JSON.parse(execFileSync(process.execPath, [server.script], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
    return { reply, called: existsSync(server.called) }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpPermissionServer', () => {
  it('records only its exact native tool', () => {
    const { reply, called } = execute('touch', {})
    expect(called).toBe(true)
    expect(reply.id).toBe(0)
    expect(reply.result.content[0].text).toBe('MCP_PERMISSION_TOOL_CALLED')
  })

  it.each([{ name: 'wrong', args: {} }, { name: 'touch', args: null }, { name: 'touch', args: { extra: true } }])('rejects a wrong call before writing its marker: %j', ({ name, args }) => {
    const { reply, called } = execute(name, args)
    expect(reply.error?.code).toBe(-32602)
    expect(called).toBe(false)
  })
})
