import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createKimiEnvironment } from './kimiEnvironment'
import { mcpProbeServer } from './mcpProbeServer'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
function options() {
  return {
    homeDir,
    baseURL: 'http://127.0.0.1:4567/v1',
    providerID: 'unit-provider',
    thinking: { alias: 'unit-provider/thinking', model: 'thinking-model' },
    plain: { alias: 'unit-provider/plain', model: 'plain-model' },
    mcpEchoServer: echoServer,
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'kimi-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createKimiEnvironment', () => {
  it('maps each alias onto its model, and selects the thinking alias by default', () => {
    const env = createKimiEnvironment(options())
    const config = readFileSync(join(env.KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('default_model = "unit-provider/thinking"')
    expect(config).toContain('[providers.unit-provider]\ntype = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"')
    expect(config).toContain('[models."unit-provider/thinking"]\nprovider = "unit-provider"\nmodel = "thinking-model"')
    expect(config).toContain('[models."unit-provider/plain"]\nprovider = "unit-provider"\nmodel = "plain-model"')
    expect(config).toContain('auto_session_title = false')
  })

  it('lists the echo server, and turns off telemetry and the update', () => {
    const env = createKimiEnvironment(options())
    expect(env).toEqual({ KIMI_CODE_HOME: join(homeDir, '.kimi-code'), KIMI_DISABLE_TELEMETRY: '1', KIMI_CODE_NO_AUTO_UPDATE: '1' })
    expect(JSON.parse(readFileSync(join(env.KIMI_CODE_HOME!, 'mcp.json'), 'utf8'))).toEqual({ mcpServers: { echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'] } } })
  })
})
