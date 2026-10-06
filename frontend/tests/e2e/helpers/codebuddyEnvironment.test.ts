import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createCodebuddyEnvironment } from './codebuddyEnvironment'

let homeDir: string
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', alternateModelID: 'unit-alt', startModel: 'custom-local:unit-model' })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'codebuddy-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createCodebuddyEnvironment', () => {
  it('writes both models with the chat completions URL that CodeBuddy requires, and starts on the given model', () => {
    const env = createCodebuddyEnvironment(options())
    expect(env.CODEBUDDY_CONFIG_DIR).toBe(join(homeDir, '.codebuddy'))
    const models = JSON.parse(readFileSync(join(env.CODEBUDDY_CONFIG_DIR!, 'models.json'), 'utf8'))
    expect(models.models).toEqual([
      expect.objectContaining({ id: 'unit-model', name: 'Mock Model', apiKey: 'unit-key', url: 'http://127.0.0.1:4567/v1/chat/completions', supportsImages: true }),
      expect.objectContaining({ id: 'unit-alt', name: 'Alternate Mock Model', url: 'http://127.0.0.1:4567/v1/chat/completions' }),
    ])
    expect(models.availableModels).toEqual(['unit-model', 'unit-alt'])
    expect(JSON.parse(readFileSync(join(env.CODEBUDDY_CONFIG_DIR!, 'settings.json'), 'utf8'))).toEqual({ model: 'custom-local:unit-model' })
  })

  it('turns off telemetry, trace collection, and the updater', () => {
    expect(createCodebuddyEnvironment(options())).toMatchObject({ DISABLE_TELEMETRY: '1', DISABLE_GALILEO: '1', DISABLE_AUTOUPDATER: '1', CODEBUDDY_DISABLE_TRACE_COLLECTOR: '1' })
  })
})
