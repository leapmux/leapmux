import { describe, expect, it } from 'vitest'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { opencodeMcpServerConfiguration } from './mcpLimit'

describe('opencodeMcpServerConfiguration', () => {
  it('preserves native provider configuration and existing servers', () => {
    const server = mcpProbeServer('form_probe', '/isolated/server.mjs')
    const original = {
      providers: {
        mock: {
          settings: { apiKey: 'synthetic' },
        },
      },
      mcp: {
        servers: {
          existing: {
            type: 'local',
            command: ['existing'],
          },
        },
      },
    }
    const configured = JSON.parse(opencodeMcpServerConfiguration(JSON.stringify(original), server))
    expect(configured.providers).toEqual(original.providers)
    expect(configured.mcp.servers.existing).toEqual(original.mcp.servers.existing)
    expect(configured.mcp.servers.form_probe).toEqual({
      type: 'local',
      command: [server.command, ...server.args],
    })
    expect(Object.hasOwn(configured, 'provider')).toBe(false)
  })
})
