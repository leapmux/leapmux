import type { ClineEnvironmentOptions } from './clineEnvironment'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createClineEnvironment } from './clineEnvironment'
import { mcpProbeServer } from './mcpProbeServer'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', providerID: 'unit-provider', modelID: 'unit-model', mcpEchoServer: echoServer })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'cline-environment-test-'))
})

afterEach(() => {
  vi.useRealTimers()
  rmSync(homeDir, { recursive: true, force: true })
})

describe('createClineEnvironment', () => {
  it.each([false, true])('writes the configured thinking preference %s and effort into the private provider profile', (enabled) => {
    const configured = { ...options(), reasoning: { enabled, effort: 'low' } }
    const env = createClineEnvironment(configured)
    const providers = JSON.parse(readFileSync(join(env.CLINE_DATA_DIR!, 'settings', 'providers.json'), 'utf8'))
    expect(providers.providers['unit-provider'].settings.reasoning).toEqual({ enabled, effort: 'low' })
  })

  it('selects the given provider and model with a UTC update time, which Cline requires', () => {
    vi.useFakeTimers({ now: new Date('2026-10-06T01:02:03.004Z'), toFake: ['Date'] })
    const env = createClineEnvironment(options())
    const settings = join(env.CLINE_DATA_DIR!, 'settings')
    expect(JSON.parse(readFileSync(join(settings, 'providers.json'), 'utf8'))).toEqual({
      version: 1,
      lastUsedProvider: 'unit-provider',
      modes: {},
      providers: {
        'unit-provider': {
          settings: { provider: 'unit-provider', apiKey: 'unit-key', model: 'unit-model', baseUrl: 'http://127.0.0.1:4567/v1' },
          updatedAt: '2026-10-06T01:02:03.004Z',
          tokenSource: 'manual',
        },
      },
    })
    expect(JSON.parse(readFileSync(join(env.CLINE_DATA_DIR!, 'cache', 'feature-flags.json'), 'utf8'))).toEqual({
      version: 2,
      updatedAt: Date.parse('2026-10-06T01:02:03.004Z'),
      userId: null,
      flagsPayload: { featureFlags: {}, featureFlagPayloads: {} },
    })
  })

  it('turns telemetry and the update off, and lists the echo server', () => {
    const settings = join(createClineEnvironment(options()).CLINE_DATA_DIR!, 'settings')
    expect(JSON.parse(readFileSync(join(settings, 'global-settings.json'), 'utf8'))).toEqual({ telemetryOptOut: true, autoUpdateEnabled: false })
    expect(JSON.parse(readFileSync(join(settings, 'cline_mcp_settings.json'), 'utf8'))).toEqual({
      mcpServers: { echo_probe: { transport: { type: 'stdio', command: echoServer.command, args: ['/srv/echo.mjs'] } } },
    })
  })

  it('writes no server when the private workspace needs no echo tool', () => {
    const configured: ClineEnvironmentOptions = options()
    delete configured.mcpEchoServer
    const env = createClineEnvironment(configured)
    expect(JSON.parse(readFileSync(join(env.CLINE_DATA_DIR!, 'settings', 'cline_mcp_settings.json'), 'utf8')))
      .toEqual({ mcpServers: {} })
  })

  it('keeps Cline under the isolated home, and empties every path override so each follows CLINE_DATA_DIR', () => {
    const env = createClineEnvironment(options())
    expect(env.CLINE_DIR).toBe(join(homeDir, '.cline'))
    expect(env.CLINE_DATA_DIR).toBe(join(homeDir, '.cline', 'data'))
    const overrides = Object.keys(env).filter(key => key !== 'CLINE_DIR' && key !== 'CLINE_DATA_DIR' && /_(?:DIR|PATH)$/.test(key))
    expect(overrides.length).toBeGreaterThan(10)
    for (const key of overrides)
      expect(env[key], key).toBe('')
    expect(env).not.toHaveProperty('CLINE_PROVIDER')
    expect(env).not.toHaveProperty('CLINE_MODEL')
  })
})
