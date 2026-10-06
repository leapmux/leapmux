import { describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createOpenCodeEnvironment, openCodeFamilyConfig } from './openCodeEnvironment'

const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const provider = {
  baseURL: 'http://127.0.0.1:4567/v1',
  modelKey: 'unit-key',
  providerID: 'unit-provider',
  models: [{ id: 'first-model', name: 'First' }, { id: 'second-model', name: 'Second' }],
}

describe('openCodeFamilyConfig', () => {
  it('selects the first model as the default, and lists every model with its variants', () => {
    const config = openCodeFamilyConfig(provider, echoServer) as {
      model: string
      provider: Record<string, { options: unknown, models: Record<string, { name: string, variants: Record<string, unknown> }> }>
      mcp: Record<string, unknown>
    }
    expect(config.model).toBe('unit-provider/first-model')
    expect(config.provider['unit-provider']!.options).toEqual({ apiKey: 'unit-key', baseURL: 'http://127.0.0.1:4567/v1' })
    expect(Object.keys(config.provider['unit-provider']!.models)).toEqual(['first-model', 'second-model'])
    expect(config.provider['unit-provider']!.models['second-model']!.name).toBe('Second')
    expect(Object.keys(config.provider['unit-provider']!.models['first-model']!.variants)).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
    expect(config.mcp).toEqual({ echo_probe: { type: 'local', command: [echoServer.command, '/srv/echo.mjs'] } })
  })

  it('refuses a provider with no model, which leaves no default', () => {
    expect(() => openCodeFamilyConfig({ ...provider, models: [] }, echoServer)).toThrow('The OpenCode family provider needs at least one model.')
  })
})

describe('createOpenCodeEnvironment', () => {
  it('gives OpenCode and Kilo one configuration that keeps two recent turns', () => {
    const env = createOpenCodeEnvironment({ ...provider, mcpEchoServer: echoServer })
    expect(env.KILO_CONFIG_CONTENT).toBe(env.OPENCODE_CONFIG_CONTENT)
    expect(JSON.parse(env.OPENCODE_CONFIG_CONTENT!)).toEqual({ ...openCodeFamilyConfig(provider, echoServer), compaction: { tail_turns: 2 } })
  })

  it('closes the project configuration, the telemetry, and the update of each', () => {
    expect(createOpenCodeEnvironment({ ...provider, mcpEchoServer: echoServer })).toMatchObject({
      OPENCODE_DISABLE_PROJECT_CONFIG: 'true',
      KILO_DISABLE_PROJECT_CONFIG: 'true',
      KILO_TELEMETRY_LEVEL: 'off',
      OPENCODE_DISABLE_AUTOUPDATE: 'true',
      KILO_DISABLE_AUTOUPDATE: 'true',
    })
  })
})
