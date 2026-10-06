import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createQoderEnvironment } from './qoderEnvironment'
import { qoderEndpointCacheRecords } from './qoderSurface'

let runDirectory: string
let previousPath: string | undefined
const options = () => ({ homeDir: join(runDirectory, 'home'), shimsDirectory: join(runDirectory, 'shims'), origin: 'http://127.0.0.1:4567', baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', alternateModelID: 'unit-alt' })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'qoder-environment-test-'))
  mkdirSync(join(runDirectory, 'shims'))
  previousPath = process.env.PATH
  // An empty search path holds no `qodercli`, so no launch wrapper is written unless a test puts one there.
  process.env.PATH = join(runDirectory, 'no-binaries')
})

afterEach(() => {
  vi.useRealTimers()
  if (previousPath === undefined)
    delete process.env.PATH
  else
    process.env.PATH = previousPath
  rmSync(runDirectory, { recursive: true, force: true })
})

describe('createQoderEnvironment', () => {
  it('registers both models under one provider, beside the form server, and no second catalog entry', () => {
    createQoderEnvironment(options())
    const qoderHome = join(runDirectory, 'home', '.qoder')
    const settings = JSON.parse(readFileSync(join(qoderHome, 'settings.json'), 'utf8'))
    expect(Object.keys(settings.providers)).toEqual(['mockprov'])
    expect(settings.providers.mockprov).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', apiKey: 'unit-key' })
    expect(settings.providers.mockprov.models.map((model: { model: string }) => model.model)).toEqual(['unit-model', 'unit-alt'])
    expect(settings.mcpServers.form_probe).toEqual({ command: process.execPath, args: [join(qoderHome, 'form-server.mjs')] })
    expect(existsSync(join(qoderHome, 'form-server.mjs'))).toBe(true)
    expect(settings.modelConfigs).toBeUndefined()
  })

  it('writes both endpoint cache formats for the given origin, with the current time', () => {
    vi.useFakeTimers({ now: new Date('2026-10-06T00:00:00Z'), toFake: ['Date'] })
    createQoderEnvironment(options())
    const cacheDir = join(runDirectory, 'home', '.qoder', '.cache')
    const expected = qoderEndpointCacheRecords('http://127.0.0.1:4567', Date.parse('2026-10-06T00:00:00Z'))
    expect(JSON.parse(readFileSync(join(cacheDir, 'endpoint-cache.json'), 'utf8'))).toEqual(expected.v1)
    for (const file of ['qoder-client-endpoint-cache.json', 'qoder-client-endpoint-cache-public.json'])
      expect(JSON.parse(readFileSync(join(cacheDir, file), 'utf8')), file).toEqual(expected.v2)
  })

  it('supplies the mocked account through the SDK payload, and empties the developer\'s own token', () => {
    const env = createQoderEnvironment(options())
    expect(env.QODER_SDK_AUTH_PAYLOAD_FILE).toBe(join(runDirectory, 'home', '.qoder', 'qoder-sdk-auth.json'))
    expect(JSON.parse(readFileSync(env.QODER_SDK_AUTH_PAYLOAD_FILE!, 'utf8'))).toEqual({ type: 'accessToken', accessToken: 'unit-key' })
    expect(env).toMatchObject({
      QODER_AGENT_SDK_ENTRYPOINT: '1',
      QODER_SDK_CUSTOM_BASE_URL_BYOK: '1',
      QODER_PERSONAL_ACCESS_TOKEN: '',
      QODER_CONFIG_SERVICE_URL: 'http://127.0.0.1:4567',
      QODER_SERVER_ENDPOINT: '',
      QODERSEC_SKIP_ASYNC_UPDATE: '1',
    })
  })

  it('writes no launch wrapper when the search path holds no qodercli', () => {
    createQoderEnvironment(options())
    expect(existsSync(join(runDirectory, 'shims', 'qodercli'))).toBe(false)
  })

  // The CLI consumes the payload once, so the wrapper writes it again before each start.
  it.runIf(process.platform !== 'win32')('writes a launch wrapper that restores the payload before it starts the installed qodercli', () => {
    const binaries = join(runDirectory, 'binaries')
    mkdirSync(binaries)
    const installed = join(binaries, 'qodercli')
    writeFileSync(installed, `#!/bin/sh\nprintf '%s' "$*" > ${JSON.stringify(join(runDirectory, 'started'))}\n`, { mode: 0o755 })
    process.env.PATH = binaries
    const env = createQoderEnvironment(options())
    unlinkSync(env.QODER_SDK_AUTH_PAYLOAD_FILE!)
    execFileSync(join(runDirectory, 'shims', 'qodercli'), ['--version'], { env: { ...process.env, ...env } })
    expect(JSON.parse(readFileSync(env.QODER_SDK_AUTH_PAYLOAD_FILE!, 'utf8'))).toEqual({ type: 'accessToken', accessToken: 'unit-key' })
    expect(readFileSync(join(runDirectory, 'started'), 'utf8')).toBe('--version')
  })
})
