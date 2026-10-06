import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { MCP_PERMISSION_SERVER_NAME, writeMcpPermissionServer } from './mcpPermissionServer'

function execute(method: string, params: unknown) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-permission-unit-'))
  try {
    const server = writeMcpPermissionServer(directory)
    const request = { jsonrpc: '2.0', id: 0, method, params }
    const reply = JSON.parse(execFileSync(server.command, [...server.args], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
    return { server, reply, called: existsSync(server.called), ready: existsSync(server.ready) }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

function callTool(name: string, args: unknown) {
  return execute('tools/call', { name, arguments: args })
}

describe('writeMcpPermissionServer', () => {
  it('reports the name that it returns', () => {
    const { server, reply } = execute('initialize', { protocolVersion: '2025-03-26' })
    expect(server.name).toBe(MCP_PERMISSION_SERVER_NAME)
    expect(reply.result.serverInfo.name).toBe(server.name)
  })

  it('writes its ready file when an agent lists its tools, and not before', () => {
    expect(execute('initialize', { protocolVersion: '2025-03-26' }).ready).toBe(false)
    const listed = execute('tools/list', {})
    expect(listed.ready).toBe(true)
    expect(listed.reply.result.tools.map((tool: { name: string }) => tool.name)).toEqual(['touch'])
  })

  it('records only its exact native tool', () => {
    const { reply, called } = callTool('touch', {})
    expect(called).toBe(true)
    expect(reply.id).toBe(0)
    expect(reply.result.content[0].text).toBe('MCP_PERMISSION_TOOL_CALLED')
  })

  it.each([{ name: 'wrong', args: {} }, { name: 'touch', args: null }, { name: 'touch', args: { extra: true } }])('rejects a wrong call before writing its marker: %j', ({ name, args }) => {
    const { reply, called } = callTool(name, args)
    expect(reply.error?.code).toBe(-32602)
    expect(called).toBe(false)
  })
})
