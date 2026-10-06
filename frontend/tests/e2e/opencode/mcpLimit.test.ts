import { describe, expect, it } from 'vitest'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { opencodeMcpServerConfiguration } from './mcpLimit'

describe('opencodeMcpServerConfiguration', () => {
  it('adds the server under its own name and keeps zero and false provider values', () => {
    const original = { provider: { mock: { options: { enabled: false, maximum: 0 } } }, mcp: { old: { type: 'local', command: ['old'] } } }
    const server = mcpProbeServer('result_probe', '/run/actual-results.mjs')
    const result = JSON.parse(opencodeMcpServerConfiguration(JSON.stringify(original), server))
    expect(result.provider).toEqual(original.provider)
    expect(result.mcp).toEqual({ ...original.mcp, result_probe: { type: 'local', command: [server.command, '/run/actual-results.mjs'] } })
  })

  it('keeps the other native MCP servers and supports an initially absent server map', () => {
    const server = mcpProbeServer('form_probe', '/run/form.mjs')
    expect(JSON.parse(opencodeMcpServerConfiguration('{"provider":{}}', server)).mcp).toEqual({ form_probe: { type: 'local', command: [server.command, '/run/form.mjs'] } })
  })

  it('copies the command, so a later change of the server arguments leaves the configuration unchanged', () => {
    const args = ['/run/form.mjs']
    const server = { ...mcpProbeServer('form_probe', '/run/form.mjs'), args }
    const configuration = opencodeMcpServerConfiguration('{"provider":{}}', server)
    args.push('later')
    expect(JSON.parse(configuration).mcp.form_probe.command).toEqual([server.command, '/run/form.mjs'])
  })

  it.each(['null', '{}', '{"provider":{},"mcp":[]}', '{invalid'])('refuses the invalid native configuration %s', (configuration) => {
    expect(() => opencodeMcpServerConfiguration(configuration, mcpProbeServer('form_probe', '/run/form.mjs'))).toThrow()
  })

  it('refuses an incomplete server command', () => {
    const server = { ...mcpProbeServer('form_probe', '/run/form.mjs'), args: ['/run/form.mjs', ''] }
    expect(() => opencodeMcpServerConfiguration('{"provider":{}}', server)).toThrow('executable command')
  })
})
