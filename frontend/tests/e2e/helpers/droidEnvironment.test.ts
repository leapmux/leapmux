import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createDroidEnvironment } from './droidEnvironment'

let homeDir: string
function options() {
  return {
    homeDir,
    baseURL: 'http://127.0.0.1:4567/v1',
    modelKey: 'unit-key',
    primary: { handle: 'custom:Unit-0', model: 'unit-model' },
    alternate: { handle: 'custom:Unit-1', model: 'unit-alt' },
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'droid-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createDroidEnvironment', () => {
  // The override names the directory that contains `.factory`, not `.factory` itself.
  it('points the override at the home that holds .factory, and the service at the mock', () => {
    expect(createDroidEnvironment(options())).toMatchObject({
      FACTORY_HOME_OVERRIDE: homeDir,
      FACTORY_API_BASE_URL: 'http://127.0.0.1:4567/v1',
      FACTORY_API_KEY: 'unit-key',
      FACTORY_DROID_AUTO_UPDATE_ENABLED: '0',
      FACTORY_TELEMETRY_INGEST_BASE_URL: 'http://127.0.0.1:9',
      FACTORY_DISABLE_KEYRING: '1',
    })
  })

  it('registers both BYOK models under their handles, and starts on the first one', () => {
    createDroidEnvironment(options())
    const settings = JSON.parse(readFileSync(join(homeDir, '.factory', 'settings.json'), 'utf8'))
    expect(settings.customModels).toEqual([
      expect.objectContaining({ id: 'custom:Unit-0', model: 'unit-model', index: 0, baseUrl: 'http://127.0.0.1:4567/v1', apiKey: 'unit-key', displayName: 'Mock Model' }),
      expect.objectContaining({ id: 'custom:Unit-1', model: 'unit-alt', index: 1, baseUrl: 'http://127.0.0.1:4567/v1', apiKey: 'unit-key', displayName: 'Alternate Mock Model' }),
    ])
    expect(settings.sessionDefaultSettings).toEqual({ model: 'custom:Unit-0', reasoningEffort: 'none', autonomyMode: 'normal' })
  })
})
