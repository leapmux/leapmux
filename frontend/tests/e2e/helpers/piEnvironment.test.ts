import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createPiEnvironment, piAgentDirectory } from './piEnvironment'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const options = (realHomeDir?: string) => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', flashModelID: 'unit-flash', mcpEchoServer: echoServer, realHomeDir })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'pi-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createPiEnvironment', () => {
  it('writes a provider of the two models, the second of which takes an effort', () => {
    const env = createPiEnvironment(options())
    expect(env.PI_CODING_AGENT_DIR).toBe(piAgentDirectory(homeDir))
    const models = JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'models.json'), 'utf8'))
    expect(models.providers.zai).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', api: 'openai-completions', apiKey: 'unit-key' })
    expect(models.providers.zai.models.map((model: { id: string }) => model.id)).toEqual(['unit-model', 'unit-flash'])
    expect(models.providers.zai.models[0].compat).toBeUndefined()
    expect(models.providers.zai.models[1].compat).toEqual({ supportsReasoningEffort: true })
  })

  it('selects the default model and states no package without a real home', () => {
    const env = createPiEnvironment(options())
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8'))).toEqual({
      defaultProvider: 'zai',
      defaultModel: 'unit-model',
      compaction: { keepRecentTokens: 32 },
      packages: [],
    })
  })

  it('loads the packages of the real home', () => {
    const realHomeDir = join(homeDir, 'real-home')
    const settings = JSON.parse(readFileSync(join(createPiEnvironment(options(realHomeDir)).PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8'))
    expect(settings.packages).toContain(join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules', 'pi-goal-x'))
    expect(settings.packages.every((path: string) => path.startsWith(join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules')))).toBe(true)
  })

  it('exposes the echo server directly, and turns off every management request', () => {
    const env = createPiEnvironment(options())
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'mcp.json'), 'utf8'))).toEqual({
      mcpServers: { echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'], exposure: 'direct' } },
    })
    expect(env).toMatchObject({ PI_OFFLINE: '1', PI_SKIP_VERSION_CHECK: '1', PI_CODING_AGENT_SESSION_DIR: '' })
  })
})
