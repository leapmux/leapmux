import { describe, expect, it } from 'vitest'
import { opencodeMcpFormConfiguration, opencodeMcpServerConfiguration } from './opencodeMcpLimit'

describe('opencodeMcpServerConfiguration', () => {
  it('uses an exact caller-supplied server name and retains zero and false provider values', () => {
    const original = { provider: { mock: { options: { enabled: false, maximum: 0 } } }, mcp: { old: { type: 'local', command: ['old'] } } }
    const result = JSON.parse(opencodeMcpServerConfiguration(JSON.stringify(original), 'result_probe', ['node', 'actual-results.mjs']))
    expect(result.provider).toEqual(original.provider)
    expect(result.mcp).toEqual({ ...original.mcp, result_probe: { type: 'local', command: ['node', 'actual-results.mjs'] } })
  })

  it.each(['', '../outside', 'server.name', 'server name'])('rejects an invalid native server identity: %s', (serverName) => {
    expect(() => opencodeMcpServerConfiguration('{"provider":{}}', serverName, ['node'])).toThrow('exact server name')
  })
})

describe('opencodeMcpFormConfiguration', () => {
  it('preserves the actual isolated provider and other native MCP servers', () => {
    const source = { provider: { isolated: { options: { baseURL: 'http://127.0.0.1/v1' } } }, mcp: { echo_probe: { type: 'local', command: ['node', 'echo.mjs'] } } }
    const result = JSON.parse(opencodeMcpFormConfiguration(JSON.stringify(source), ['node', 'form.mjs']))
    expect(result.provider).toEqual(source.provider)
    expect(result.mcp.echo_probe).toEqual(source.mcp.echo_probe)
    expect(result.mcp.form_probe).toEqual({ type: 'local', command: ['node', 'form.mjs'] })
  })

  it('copies command ownership and supports an initially absent server map', () => {
    const command = ['node', 'form.mjs']
    const configuration = opencodeMcpFormConfiguration('{"provider":{}}', command)
    command.push('later')
    expect(JSON.parse(configuration).mcp.form_probe.command).toEqual(['node', 'form.mjs'])
  })

  it.each(['null', '{}', '{"provider":{},"mcp":[]}', '{invalid'])('refuses the invalid native configuration %s', (configuration) => {
    expect(() => opencodeMcpFormConfiguration(configuration, ['node', 'form.mjs'])).toThrow()
  })

  it.each([{ command: [] }, { command: [''] }, { command: ['node', ''] }])('refuses the incomplete native server command $command', ({ command }) => {
    expect(() => opencodeMcpFormConfiguration('{"provider":{}}', command)).toThrow('executable command')
  })
})
