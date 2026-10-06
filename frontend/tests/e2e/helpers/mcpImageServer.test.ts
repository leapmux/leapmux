import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { MCP_IMAGE_SERVER_NAME, writeMcpImageServer } from './mcpImageServer'
import { writeToolImage } from './toolImages'

function run(method: string, params: unknown) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-image-unit-'))
  try {
    const image = writeToolImage(directory, 'actual')
    const server = writeMcpImageServer(directory, image)
    const request = { jsonrpc: '2.0', id: 0, method, params }
    const reply = JSON.parse(execFileSync(server.command, [...server.args], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
    return { server, reply, ready: existsSync(server.ready) }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

function execute(name: string, args: unknown) {
  return run('tools/call', { name, arguments: args }).reply
}

describe('writeMcpImageServer', () => {
  it('reports the name that it returns', () => {
    const { server, reply } = run('initialize', { protocolVersion: '2025-03-26' })
    expect(server.name).toBe(MCP_IMAGE_SERVER_NAME)
    expect(reply.result.serverInfo.name).toBe(server.name)
  })

  it('writes its ready file when an agent lists its tools, and not before', () => {
    expect(run('initialize', { protocolVersion: '2025-03-26' }).ready).toBe(false)
    const listed = run('tools/list', {})
    expect(listed.ready).toBe(true)
    expect(listed.reply.result.tools.map((tool: { name: string }) => tool.name)).toEqual(['show'])
  })

  it('returns the actual image for its exact native tool', () => {
    const reply = execute('show', {})
    expect(reply.id).toBe(0)
    expect(reply.result.content[1]).toMatchObject({ type: 'image', mimeType: 'image/png' })
  })

  it.each([{ name: 'wrong', args: {} }, { name: 'show', args: null }, { name: 'show', args: { extra: true } }])('rejects a wrong call before returning an image: %j', ({ name, args }) => {
    const reply = execute(name, args)
    expect(reply.error?.code).toBe(-32602)
    expect(reply.result).toBeUndefined()
  })
})
