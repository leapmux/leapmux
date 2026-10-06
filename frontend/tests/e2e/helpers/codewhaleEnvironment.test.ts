import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createCodewhaleEnvironment } from './codewhaleEnvironment'
import { mcpProbeServer } from './mcpProbeServer'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', visionModelID: 'unit-vision', mcpEchoServer: echoServer })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'codewhale-environment-test-'))
})

afterEach(() => {
  vi.useRealTimers()
  rmSync(homeDir, { recursive: true, force: true })
})

describe('createCodewhaleEnvironment', () => {
  it('points the built-in route at the mock with the given models, and closes each unscripted request', () => {
    const env = createCodewhaleEnvironment(options())
    expect(env).toEqual({ CODEWHALE_HOME: join(homeDir, '.codewhale'), CODEWHALE_TELEMETRY: '0', CODEWHALE_NO_UPDATE_CHECK: '1', CODEWHALE_ALLOW_SHELL: '1' })
    const config = readFileSync(join(env.CODEWHALE_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('provider = "deepseek"\ndefault_text_model = "unit-model"')
    expect(config).toContain('[providers.deepseek]\nbase_url = "http://127.0.0.1:4567/v1"\napi_key = "unit-key"\nauth_mode = "api-key"\nmodel = "unit-vision"')
    expect(config).toContain('[retry]\nenabled = false')
    expect(JSON.parse(readFileSync(join(env.CODEWHALE_HOME!, 'mcp.json'), 'utf8'))).toEqual({ servers: { echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'] } } })
  })

  // Codewhale accepts image input only from a catalog that is fresh for this exact endpoint.
  it('writes a fresh catalog for the fingerprint of the given base URL', () => {
    vi.useFakeTimers({ now: new Date('2026-10-06T00:00:00Z'), toFake: ['Date'] })
    const env = createCodewhaleEnvironment(options())
    const catalog = JSON.parse(readFileSync(join(env.CODEWHALE_HOME!, 'catalog', 'provider-catalogs.json'), 'utf8'))
    const fingerprint = createHash('sha256').update('http://127.0.0.1:4567/v1').digest('hex')
    const entry = catalog.cache.entries[`deepseek:deepseek\x1F${fingerprint}`]
    const fetchedAt = Date.parse('2026-10-06T00:00:00Z') / 1000
    expect(entry).toMatchObject({ provider: 'deepseek:deepseek', base_url_fingerprint: fingerprint, fetched_at: fetchedAt, status: { state: 'fresh' } })
    expect(entry.offerings).toEqual([
      expect.objectContaining({ wire_model_id: 'unit-model', endpoint_key: 'responses', default_for_provider: true, modalities: { input: ['text'], output: ['text'] } }),
      expect.objectContaining({ wire_model_id: 'unit-vision', endpoint_key: 'chat', default_for_provider: false, modalities: { input: ['text', 'image'], output: ['text'] } }),
    ])
  })
})
