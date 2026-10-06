import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from './mcpProbeServer'
import { createQwenEnvironment, QWEN_AUTH_TYPE } from './qwenEnvironment'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelID: 'unit-model', alternateModelID: 'unit-alt', mcpEchoServer: echoServer })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'qwen-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createQwenEnvironment', () => {
  it('registers both models under the selected auth type and pins the first one', () => {
    const env = createQwenEnvironment(options())
    expect(env.QWEN_HOME).toBe(join(homeDir, '.qwen'))
    const settings = JSON.parse(readFileSync(join(env.QWEN_HOME!, 'settings.json'), 'utf8'))
    expect(settings.security.auth.selectedType).toBe(QWEN_AUTH_TYPE)
    expect(settings.model.name).toBe('unit-model')
    expect(settings.modelProviders[QWEN_AUTH_TYPE].map((model: { id: string }) => model.id)).toEqual(['unit-model', 'unit-alt'])
    expect(settings.modelProviders[QWEN_AUTH_TYPE][1]).toMatchObject({ baseUrl: 'http://127.0.0.1:4567/v1', envKey: 'LEAPMUX_E2E_MODEL_API_KEY', name: 'Qwen E2E Alternate' })
    expect(settings.mcpServers).toEqual({ echo_probe: { command: echoServer.command, args: ['/srv/echo.mjs'] } })
  })

  it('asks the reader before a tool runs, and stops every background model call and the update', () => {
    const env = createQwenEnvironment(options())
    const settings = JSON.parse(readFileSync(join(env.QWEN_HOME!, 'settings.json'), 'utf8'))
    expect(settings.tools.approvalMode).toBe('default')
    expect(settings).toMatchObject({ memory: { enableManagedAutoMemory: false }, ui: { enableFollowupSuggestions: false }, general: { enableAutoUpdate: false } })
    expect(env).toMatchObject({ QWEN_DISABLE_AUTO_TITLE: '1', QWEN_CODE_SKIP_UPDATE_CHECK_ONCE: 'true' })
  })
})
