import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createReasonixEnvironment } from './reasonixEnvironment'

let homeDir: string
const options = () => ({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', alternateProviderID: 'unit-alt', alternateModelID: 'unit-alt-model', plainProviderID: 'unit-plain', plainModelID: 'unit-plain-model' })

/** The `[[providers]]` table of `name` in the configuration, up to the next table or the end. */
function providerTable(config: string, name: string): string {
  const table = config.split('[[providers]]').find(section => section.includes(`name = "${name}"`))
  if (table === undefined)
    throw new Error(`The configuration has no provider ${name}.`)
  return table
}

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
    expect(config).toContain('name = "unit-plain"\nkind = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\nmodel = "unit-plain-model"')
  })

  // A spec switches to the plain provider to reach a model that offers no effort.
  it('states an effort ladder for the default and the alternate provider, and none for the plain one', () => {
    const config = readFileSync(join(createReasonixEnvironment(options()).REASONIX_HOME!, 'config.toml'), 'utf8')
    for (const name of ['deepseek', 'unit-alt'])
      expect(providerTable(config, name)).toContain('reasoning_protocol = "openai"\nsupported_efforts = ["low", "medium", "high"]\ndefault_effort = "high"')
    const plain = providerTable(config, 'unit-plain')
    expect(plain).not.toContain('reasoning_protocol')
    expect(plain).not.toContain('supported_efforts')
    expect(plain).not.toContain('default_effort')
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
