import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createQoderEnvironment, QODER_PROBE_OUTPUT_STYLE } from './qoderEnvironment'
import { qoderEndpointCacheRecords } from './qoderSurface'

let runDirectory: string
function options() {
  return {
    homeDir: join(runDirectory, 'home'),
    origin: 'http://127.0.0.1:4567',
    baseURL: 'http://127.0.0.1:4567/v1',
    modelKey: 'unit-key',
    identityToken: 'unit-account-key',
    modelID: 'unit-model',
    alternateModelID: 'unit-alt',
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'qoder-environment-test-'))
  mkdirSync(join(runDirectory, 'shims'))
})

afterEach(() => {
  vi.useRealTimers()
  rmSync(runDirectory, { recursive: true, force: true })
})

describe('createQoderEnvironment', () => {
  it('overrides an inherited service-account credential beside the job token', () => {
    const inherited = { QODER_SERVICE_ACCOUNT_KEY: 'controlled-foreign-account' }
    const merged = {
      ...inherited,
      ...createQoderEnvironment(options()),
    }
    expect(merged.QODER_SERVICE_ACCOUNT_KEY).toBe('')
  })

  it('uses the current job credential without the removed SDK worker mode', () => {
    const env = createQoderEnvironment(options())
    expect(env).toHaveProperty('QODER_JOB_TOKEN')
    expect(JSON.parse(env.QODER_JOB_TOKEN!)).toEqual({ token: 'unit-account-key' })
    expect(env.QODER_AGENT_SDK_ENTRYPOINT).toBeUndefined()
    expect(env.QODER_SDK_AUTH_PAYLOAD_FILE).toBeUndefined()
    expect(env.QODER_SDK_CUSTOM_BASE_URL_BYOK).toBeUndefined()
  })

  it('registers both models under one provider, beside the form server, and no second catalog entry', () => {
    createQoderEnvironment(options())
    const qoderHome = join(runDirectory, 'home', '.qoder')
    const settings = JSON.parse(readFileSync(join(qoderHome, 'settings.json'), 'utf8'))
    expect(settings.autoMode).toEqual({ classifier: 'llm' })
    expect(Object.keys(settings.providers)).toEqual(['mockprov'])
    expect(settings.providers.mockprov).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', apiKey: 'unit-key' })
    expect(settings.providers.mockprov.models.map((model: { model: string }) => model.model)).toEqual(['unit-model', 'unit-alt'])
    expect(settings.mcpServers.form_probe).toEqual({
      command: process.execPath,
      args: [
        join(qoderHome, 'form-server.mjs'),
      ],
    })
    expect(existsSync(join(qoderHome, 'form-server.mjs'))).toBe(true)
    expect(settings.modelConfigs).toBeUndefined()
  })

  it('writes both endpoint cache formats for the given origin, with the current time', () => {
    vi.useFakeTimers({
      now: new Date('2026-10-06T00:00:00Z'),
      toFake: ['Date'],
    })
    createQoderEnvironment(options())
    const cacheDir = join(runDirectory, 'home', '.qoder', '.cache')
    const expected = qoderEndpointCacheRecords('http://127.0.0.1:4567', Date.parse('2026-10-06T00:00:00Z'))
    expect(JSON.parse(readFileSync(join(cacheDir, 'endpoint-cache.json'), 'utf8'))).toEqual(expected.v1)
    for (const file of ['qoder-client-endpoint-cache.json', 'qoder-client-endpoint-cache-public.json'])
      expect(JSON.parse(readFileSync(join(cacheDir, file), 'utf8')), file).toEqual(expected.v2)
  })

  it('supplies a current job token and empties the developer\'s own token', () => {
    const env = createQoderEnvironment(options())
    expect(JSON.parse(env.QODER_JOB_TOKEN!)).toEqual({ token: 'unit-account-key' })
    expect(env).toMatchObject({
      QODER_PERSONAL_ACCESS_TOKEN: '',
      QODER_CONFIG_SERVICE_URL: 'http://127.0.0.1:4567',
      QODER_SERVER_ENDPOINT: '',
      QODERSEC_SKIP_ASYNC_UPDATE: '1',
    })
  })

  it('writes the probe output style where Qoder reads user styles', () => {
    createQoderEnvironment(options())
    const style = readFileSync(join(runDirectory, 'home', '.qoder', 'output-styles', 'leapmux-probe.md'), 'utf8')
    expect(style).toMatch(new RegExp(`^---\\nname: ${QODER_PROBE_OUTPUT_STYLE.name}\\n`))
    expect(style).toContain(QODER_PROBE_OUTPUT_STYLE.marker)
  })

  it('writes no launch wrapper for current job authentication', () => {
    createQoderEnvironment(options())
    expect(existsSync(join(runDirectory, 'shims', 'qodercli'))).toBe(false)
  })

  it('keeps credentials in the environment without a consumed payload file', () => {
    createQoderEnvironment(options())
    expect(existsSync(join(runDirectory, 'home', '.qoder', 'qoder-sdk-auth.json'))).toBe(false)
    expect(existsSync(join(runDirectory, 'shims', 'qodercli'))).toBe(false)
  })
})
