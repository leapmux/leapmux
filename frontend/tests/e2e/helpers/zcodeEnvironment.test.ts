import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createZcodeEnvironment } from './zcodeEnvironment'

let runDirectory: string
const options = () => ({ runDirectory, homeDir: join(runDirectory, 'home'), baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', providerID: 'personal:unit', modelID: 'unit-model', alternateModelID: 'unit-alt' })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'zcode-environment-test-'))
})

afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('createZcodeEnvironment', () => {
  it('writes the provider of both models into the legacy configuration, in priority order', () => {
    createZcodeEnvironment(options())
    const config = JSON.parse(readFileSync(join(runDirectory, 'home', '.zcode', 'v2', 'config.json'), 'utf8'))
    const provider = config.provider['personal:unit']
    expect(provider.options).toEqual({ apiKey: 'unit-key', baseURL: 'http://127.0.0.1:4567/v1' })
    expect(Object.keys(provider.models)).toEqual(['unit-model', 'unit-alt'])
    expect(provider.models['unit-model'].zcode).toEqual({ priority: 0 })
    expect(provider.models['unit-alt'].zcode).toEqual({ priority: 1 })
  })

  it('writes the personal provider configuration, which selects the first model by default', () => {
    const env = createZcodeEnvironment(options())
    expect(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE).toBe(join(runDirectory, 'home', '.zcode', 'v2', 'provider_config.json'))
    const personal = JSON.parse(readFileSync(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE!, 'utf8'))
    expect(personal.config.defaultModelSelection).toEqual({ providerId: 'personal:unit', modelId: 'unit-model', options: { reasoningLevel: 'high' } })
    const rule = personal.config.providerConfigRules.providerRules[0]
    expect(rule.config.access).toEqual({ type: 'api-key', apiKey: 'unit-key' })
    expect(rule.config.personalModelIds).toEqual(['unit-model', 'unit-alt'])
    expect(personal.config.modelConfigRules.providerModelRules.map((model: { modelId: string }) => model.modelId)).toEqual(['unit-model', 'unit-alt'])
  })

  it('keeps its storage in the run, with telemetry off', () => {
    const env = createZcodeEnvironment(options())
    expect(env.ZCODE_STORAGE_DIR).toBe(join(runDirectory, 'zcode-storage'))
    expect(statSync(env.ZCODE_STORAGE_DIR!).isDirectory()).toBe(true)
    expect(env.ZCODE_MODEL_TELEMETRY_ENABLED).toBe('false')
  })
})
