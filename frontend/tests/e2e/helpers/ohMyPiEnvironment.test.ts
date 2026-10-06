import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createOhMyPiEnvironment, ohMyPiAgentDirectory } from './ohMyPiEnvironment'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
function options() {
  return {
    homeDir,
    origin: 'http://127.0.0.1:4567',
    baseURL: 'http://127.0.0.1:4567/v1',
    profile: 'unit-profile',
    providerID: 'unit-provider',
    modelID: 'unit-model',
    alternateModelID: 'unit-alt',
    mcpEchoServer: echoServer,
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'ohmypi-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('ohMyPiAgentDirectory', () => {
  it('lies inside the profile, which Pi\'s variables cannot reach', () => {
    expect(ohMyPiAgentDirectory('/home/unit', 'unit-profile')).toBe(join('/home/unit', '.omp', 'profiles', 'unit-profile', 'agent'))
  })
})

describe('createOhMyPiEnvironment', () => {
  it('selects the profile, and sends every request that is not loopback to the refusing mock', () => {
    expect(createOhMyPiEnvironment(options())).toMatchObject({
      OMP_PROFILE: 'unit-profile',
      PI_PROXY: 'http://127.0.0.1:4567',
      PI_CONFIG_FILES: '',
      PI_CONFIG_DIR: '',
      OMP_AUTH_BROKER_URL: '',
      OMP_AUTH_BROKER_TOKEN: '',
    })
  })

  it('writes the provider of the two models, with the key read from the run variable', () => {
    createOhMyPiEnvironment(options())
    const models = JSON.parse(readFileSync(join(ohMyPiAgentDirectory(homeDir, 'unit-profile'), 'models.yml'), 'utf8'))
    expect(models.providers['unit-provider']).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', apiKey: 'LEAPMUX_E2E_MODEL_API_KEY', api: 'openai-completions' })
    expect(models.providers['unit-provider'].models.map((model: { id: string }) => model.id)).toEqual(['unit-model', 'unit-alt'])
  })

  it('pins both roles to the provider model, disables every bundled provider but its own, and turns off its updates', () => {
    createOhMyPiEnvironment(options())
    const config = JSON.parse(readFileSync(join(ohMyPiAgentDirectory(homeDir, 'unit-profile'), 'config.yml'), 'utf8'))
    expect(config.modelRoles).toEqual({ default: 'unit-provider/unit-model', task: 'unit-provider/unit-model:off' })
    expect(config.disabledProviders).toEqual(expect.arrayContaining(['anthropic', 'openai', 'zai']))
    expect(config.disabledProviders).not.toContain('unit-provider')
    expect(config).toMatchObject({ startup: { checkUpdate: false }, marketplace: { autoUpdate: 'off' }, edit: { mode: 'replace' } })
  })

  it('starts the echo server over stdio', () => {
    createOhMyPiEnvironment(options())
    expect(JSON.parse(readFileSync(join(ohMyPiAgentDirectory(homeDir, 'unit-profile'), 'mcp.json'), 'utf8'))).toEqual({
      mcpServers: { echo_probe: { type: 'stdio', command: echoServer.command, args: ['/srv/echo.mjs'] } },
    })
  })
})
