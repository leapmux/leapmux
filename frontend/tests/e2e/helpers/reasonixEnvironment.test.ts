import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createReasonixEnvironment } from './reasonixEnvironment'

let homeDir: string
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', alternateProviderID: 'unit-alt', alternateModelID: 'unit-alt-model' })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'reasonix-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createReasonixEnvironment', () => {
  it('writes the default provider and the alternate provider of the given models', () => {
    const env = createReasonixEnvironment(options())
    expect(env).toEqual({ REASONIX_HOME: join(homeDir, '.reasonix') })
    const config = readFileSync(join(env.REASONIX_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('default_model = "deepseek/unit-model"')
    expect(config).toContain('name = "deepseek"\nkind = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\nmodel = "unit-model"')
    expect(config).toContain('vision_models = ["unit-model"]')
    expect(config).toContain('name = "unit-alt"\nkind = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\nmodel = "unit-alt-model"')
  })

  // Reasonix reads the key variable from this file alone, never from the process environment.
  it('binds the key variable in its credential file, readable by its owner alone', () => {
    const env = createReasonixEnvironment(options())
    const credentials = join(env.REASONIX_HOME!, '.env')
    expect(readFileSync(credentials, 'utf8')).toBe('LEAPMUX_E2E_MODEL_API_KEY=unit-key\n')
    if (process.platform !== 'win32')
      expect(statSync(credentials).mode & 0o777).toBe(0o600)
  })
})
