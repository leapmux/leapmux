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
    alternateThinking: { alias: 'unit-provider/wide', model: 'wide-model' },
    mcpEchoServer: echoServer,
  }
}

/** The text of the `[models."<alias>"]` table, up to the next table or the end. */
function modelTable(config: string, alias: string): string {
  const start = config.indexOf(`[models."${alias}"]`)
  if (start < 0)
    throw new Error(`The configuration has no model ${alias}.`)
  const next = config.indexOf('\n[', start + 1)
  return config.slice(start, next < 0 ? undefined : next)
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
    expect(config).toContain('[models."unit-provider/wide"]\nprovider = "unit-provider"\nmodel = "wide-model"')
    expect(config).toContain('auto_session_title = false')
  })

  // A switch that keeps a level must differ from a switch that takes the default of the new model.
  it('gives the two thinking aliases one ladder with different defaults, and the plain alias no thinking', () => {
    const config = readFileSync(join(createKimiEnvironment(options()).KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    const thinking = modelTable(config, 'unit-provider/thinking')
    const alternateThinking = modelTable(config, 'unit-provider/wide')
    const plain = modelTable(config, 'unit-provider/plain')
    for (const table of [thinking, alternateThinking]) {
      expect(table).toContain('capabilities = ["tool_use", "thinking"')
      expect(table).toContain('support_efforts = ["low", "medium", "high"]')
    }
    expect(thinking).toContain('default_effort = "high"')
    expect(alternateThinking).toContain('default_effort = "medium"')
    expect(plain).toContain('capabilities = ["tool_use"]')
    expect(plain).not.toContain('support_efforts')
  })

  it('lists the echo server, and turns off telemetry and the update', () => {
    const env = createKimiEnvironment(options())
    expect(env).toEqual({ KIMI_CODE_HOME: join(homeDir, '.kimi-code'), KIMI_DISABLE_TELEMETRY: '1', KIMI_CODE_NO_AUTO_UPDATE: '1' })
    expect(JSON.parse(readFileSync(join(env.KIMI_CODE_HOME!, 'mcp.json'), 'utf8'))).toEqual({ mcpServers: { echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'] } } })
  })
})
