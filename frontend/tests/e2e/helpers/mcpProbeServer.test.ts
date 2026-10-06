import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { assertMcpServerName, mcpProbeServer, mcpServersConfig } from './mcpProbeServer'

describe('mcpProbeServer', () => {
  it('starts the script through the Node.js runtime of the test process', () => {
    expect(mcpProbeServer('form_probe', '/run/form.mjs')).toEqual({ name: 'form_probe', script: '/run/form.mjs', command: process.execPath, args: ['/run/form.mjs'] })
  })

  it.each(['echo_probe', 'probe', 'Probe-2'])('accepts the server name %j', (name) => {
    expect(mcpProbeServer(name, '/run/server.mjs').name).toBe(name)
  })

  it.each(['', 'form probe', 'form.probe', 'form/probe', 'probé'])('refuses the server name %j', (name) => {
    expect(() => mcpProbeServer(name, '/run/server.mjs')).toThrow('MCP server name')
  })

  it.each(['', ' '])('refuses an empty script path: %j', (script) => {
    expect(() => mcpProbeServer('form_probe', script)).toThrow('path of its script')
  })
})

describe('assertMcpServerName', () => {
  it.each(['echo_probe', 'Probe-2'])('accepts %j', (name) => {
    expect(() => assertMcpServerName(name)).not.toThrow()
  })

  it.each(['', 'form probe', 'form.probe'])('refuses %j', (name) => {
    expect(() => assertMcpServerName(name)).toThrow('MCP server name')
  })
})

describe('mcpServersConfig', () => {
  it('refuses a server object whose name a configuration cannot hold', () => {
    const server = { ...mcpProbeServer('form_probe', '/run/form.mjs'), name: 'form probe' }
    expect(() => mcpServersConfig(server)).toThrow('MCP server name')
  })

  it('keys each server by its name with its launch', () => {
    const echo = mcpProbeServer('echo_probe', '/run/echo.mjs')
    const form = mcpProbeServer('form_probe', '/run/form.mjs')
    expect(mcpServersConfig(echo, form)).toEqual({
      mcpServers: {
        echo_probe: { command: process.execPath, args: ['/run/echo.mjs'] },
        form_probe: { command: process.execPath, args: ['/run/form.mjs'] },
      },
    })
  })

  it('copies the arguments, so a change of the configuration leaves the server unchanged', () => {
    const server = mcpProbeServer('echo_probe', '/run/echo.mjs')
    mcpServersConfig(server).mcpServers.echo_probe!.args.push('--changed')
    expect(server.args).toEqual(['/run/echo.mjs'])
  })

  it('refuses an empty server list', () => {
    expect(() => mcpServersConfig()).toThrow('at least one server')
  })

  it('refuses two servers with one name', () => {
    expect(() => mcpServersConfig(mcpProbeServer('echo_probe', '/run/a.mjs'), mcpProbeServer('echo_probe', '/run/b.mjs'))).toThrow('two servers named echo_probe')
  })
})
