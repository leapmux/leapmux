import type { McpNoArgumentTool, McpNoArgumentToolServerOptions } from './mcpNoArgumentToolServer'
import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { writeMcpNoArgumentToolServer } from './mcpNoArgumentToolServer'

const directories: string[] = []

afterEach(() => {
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

function scratchDirectory(): string {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-no-argument-unit-'))
  directories.push(directory)
  return directory
}

const PNG_BYTES = Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3])

/** Write the server with `tool` in a new directory, send it one request, and read its one reply. */
function exchange(tool: Partial<McpNoArgumentTool>, method: string, params?: unknown) {
  const directory = scratchDirectory()
  writeFileSync(join(directory, 'probe.png'), PNG_BYTES)
  const server = writeMcpNoArgumentToolServer(directory, {
    name: 'no_argument_probe',
    fileBase: 'probe-server',
    tool: { name: 'probe', description: 'Run the probe.', text: 'PROBE_RAN', ...tool },
  })
  const request = { jsonrpc: '2.0', id: 7, method, ...(params === undefined ? {} : { params }) }
  const reply = JSON.parse(execFileSync(server.command, [...server.args], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
  return { server, reply, ready: existsSync(server.ready) }
}

describe('writeMcpNoArgumentToolServer', () => {
  it('reports its name, and states the default protocol version when the initialize request has no params', () => {
    const { server, reply } = exchange({}, 'initialize')
    expect(server.name).toBe('no_argument_probe')
    expect(reply.result.serverInfo.name).toBe(server.name)
    expect(reply.result.protocolVersion).toBe('2025-03-26')
  })

  it('answers the protocol version of the initialize request', () => {
    expect(exchange({}, 'initialize', { protocolVersion: '2024-11-05' }).reply.result.protocolVersion).toBe('2024-11-05')
  })

  it('writes its ready file when an agent lists its tools, and lists the one tool with no arguments', () => {
    expect(exchange({}, 'initialize', {}).ready).toBe(false)
    const listed = exchange({}, 'tools/list', {})
    expect(listed.ready).toBe(true)
    expect(listed.server.ready).toBe(join(listed.server.script, '..', 'probe-server-ready'))
    expect(listed.reply.result.tools).toEqual([{ name: 'probe', description: 'Run the probe.', inputSchema: { type: 'object', properties: {} } }])
  })

  it('returns the text alone for a tool with no image and no call record', () => {
    const { reply, ready } = exchange({}, 'tools/call', { name: 'probe', arguments: {} })
    expect(reply.id).toBe(7)
    expect(reply.result.content).toEqual([{ type: 'text', text: 'PROBE_RAN' }])
    expect(ready).toBe(false)
  })

  it('returns the PNG file of the script directory as the second content item', () => {
    const { reply } = exchange({ pngFile: 'probe.png' }, 'tools/call', { name: 'probe', arguments: {} })
    expect(reply.result.content).toEqual([
      { type: 'text', text: 'PROBE_RAN' },
      { type: 'image', mimeType: 'image/png', data: PNG_BYTES.toString('base64') },
    ])
  })

  it('writes the call record when the tool runs', () => {
    const directory = scratchDirectory()
    const calledFile = join(directory, 'probe-called')
    const server = writeMcpNoArgumentToolServer(directory, {
      name: 'no_argument_probe',
      fileBase: 'probe-server',
      tool: { name: 'probe', description: 'Run the probe.', text: 'PROBE_RAN', calledFile },
    })
    const request = { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'probe', arguments: {} } }
    expect(existsSync(calledFile)).toBe(false)
    const reply = JSON.parse(execFileSync(server.command, [...server.args], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
    expect(reply.result.content).toEqual([{ type: 'text', text: 'PROBE_RAN' }])
    expect(readFileSync(calledFile, 'utf8')).toBe('called')
  })

  it.each([
    { label: 'another tool', params: { name: 'other', arguments: {} } },
    { label: 'no arguments field', params: { name: 'probe' } },
    { label: 'null arguments', params: { name: 'probe', arguments: null } },
    { label: 'array arguments', params: { name: 'probe', arguments: [] } },
    { label: 'string arguments', params: { name: 'probe', arguments: 'x' } },
    { label: 'an extra argument', params: { name: 'probe', arguments: { extra: true } } },
    { label: 'no params', params: undefined },
  ])('refuses a call with $label before the tool has any effect', ({ params }) => {
    const directory = scratchDirectory()
    const calledFile = join(directory, 'probe-called')
    const server = writeMcpNoArgumentToolServer(directory, {
      name: 'no_argument_probe',
      fileBase: 'probe-server',
      tool: { name: 'probe', description: 'Run the probe.', text: 'PROBE_RAN', calledFile },
    })
    const request = { jsonrpc: '2.0', id: 3, method: 'tools/call', ...(params === undefined ? {} : { params }) }
    const reply = JSON.parse(execFileSync(server.command, [...server.args], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
    expect(reply).toEqual({ jsonrpc: '2.0', id: 3, error: { code: -32602, message: 'The native probe tool requires empty arguments.' } })
    expect(existsSync(calledFile)).toBe(false)
  })

  it('refuses a method that it does not serve', () => {
    const { reply } = exchange({}, 'resources/list', {})
    expect(reply).toEqual({ jsonrpc: '2.0', id: 7, error: { code: -32601, message: 'Method not supported' } })
  })

  it.each<{ label: string, options: Partial<McpNoArgumentToolServerOptions>, tool?: Partial<McpNoArgumentTool>, message: string }>([
    { label: 'an empty script base name', options: { fileBase: '' }, message: 'plain base name' },
    { label: 'a script base name with a directory', options: { fileBase: 'nested/probe' }, message: 'plain base name' },
    { label: 'an invalid server name', options: { name: 'no argument' }, message: 'An MCP server name holds only' },
    { label: 'an empty tool name', options: {}, tool: { name: '' }, message: 'needs a name' },
    { label: 'an empty image name', options: {}, tool: { pngFile: '' }, message: 'file name in the script directory' },
    { label: 'an image outside the script directory', options: {}, tool: { pngFile: '../probe.png' }, message: 'file name in the script directory' },
    { label: 'a relative call record', options: {}, tool: { calledFile: 'probe-called' }, message: 'absolute path' },
  ])('refuses $label and writes no script', ({ options, tool, message }) => {
    const directory = scratchDirectory()
    expect(() => writeMcpNoArgumentToolServer(directory, {
      name: 'no_argument_probe',
      fileBase: 'probe-server',
      ...options,
      tool: { name: 'probe', description: 'Run the probe.', text: 'PROBE_RAN', ...tool },
    })).toThrow(message)
    expect(readdirSync(directory)).toEqual([])
  })
})
