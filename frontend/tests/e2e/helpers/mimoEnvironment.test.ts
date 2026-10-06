import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createMimoEnvironment } from './mimoEnvironment'
import { openCodeFamilyConfig } from './openCodeEnvironment'

let runDirectory: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const provider = {
  baseURL: 'http://127.0.0.1:4567/v1',
  modelKey: 'unit-key',
  providerID: 'unit-provider',
  models: [{ id: 'first-model', name: 'First' }, { id: 'second-model', name: 'Second' }],
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'mimo-environment-test-'))
})

afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('createMimoEnvironment', () => {
  it('reads the OpenCode family provider, hides every other provider, and adds the confirmation server', () => {
    const env = createMimoEnvironment({ ...provider, runDirectory, mcpEchoServer: echoServer })
    expect(env.MIMOCODE_HOME).toBe(join(runDirectory, 'mimocode-home'))
    const config = JSON.parse(env.MIMOCODE_CONFIG_CONTENT!)
    const family = openCodeFamilyConfig(provider, echoServer)
    expect(config.provider).toEqual(family.provider)
    expect(config.model).toBe(family.model)
    expect(config.enabled_providers).toEqual(['unit-provider'])
    const confirmationServer = join(env.MIMOCODE_HOME!, 'mcp-confirmation.mjs')
    expect(config.mcp).toEqual({
      form_probe: { type: 'local', command: [process.execPath, confirmationServer] },
      echo_probe: { type: 'local', command: [echoServer.command, '/srv/echo.mjs'] },
    })
    expect(readFileSync(confirmationServer, 'utf8')).toContain('elicitation/create')
  })

  it('makes each failed request fail once, and stops the title request', () => {
    const config = JSON.parse(createMimoEnvironment({ ...provider, runDirectory, mcpEchoServer: echoServer }).MIMOCODE_CONFIG_CONTENT!)
    expect(Object.keys(config.retry)).toEqual(['request', 'stream', 'network', 'server', 'rateLimit', 'unknown'])
    for (const retry of Object.values(config.retry))
      expect(retry).toEqual({ mode: 'bounded', maxRetries: 0 })
    expect(config.agent).toEqual({ title: { disable: true } })
    expect(config).toMatchObject({ share: 'disabled', snapshot: false })
  })

  it('closes every request that no test scripts through MiMo\'s own switches', () => {
    expect(createMimoEnvironment({ ...provider, runDirectory, mcpEchoServer: echoServer })).toMatchObject({
      MIMOCODE_DISABLE_PROJECT_CONFIG: 'true',
      MIMOCODE_ENABLE_ANALYSIS: 'false',
      MIMOCODE_DISABLE_MODELS_FETCH: 'true',
      MIMOCODE_DISABLE_AUTOUPDATE: 'true',
      MIMOCODE_EXPERIMENTAL_CRON: 'false',
      MIMOCODE_DISABLE_CHECKPOINT: 'true',
      MIMOCODE_DISABLE_PROVIDER_ENV: 'true',
    })
  })
})
