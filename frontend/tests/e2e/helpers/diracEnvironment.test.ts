import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createDiracEnvironment } from './diracEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'dirac-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createDiracEnvironment', () => {
  it('points Dirac\'s OpenAI provider at the given endpoint and model, with its update off', () => {
    expect(createDiracEnvironment({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model' })).toEqual({
      DIRAC_PROVIDER: 'openai',
      DIRAC_BASE_URL: 'http://127.0.0.1:4567/v1',
      DIRAC_API_KEY: 'unit-key',
      DIRAC_MODEL: 'unit-model',
      DIRAC_DIR: join(homeDir, '.dirac'),
      DIRAC_NO_AUTO_UPDATE: '1',
    })
  })

  it('turns telemetry off and grants every approval in its global state', () => {
    const env = createDiracEnvironment({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model' })
    expect(JSON.parse(readFileSync(join(env.DIRAC_DIR!, 'data', 'globalState.json'), 'utf8'))).toEqual({ telemetrySetting: 'disabled', autoApproveAllToggled: true, yoloModeToggled: true })
    expect(statSync(join(env.DIRAC_DIR!, 'data', 'state')).isDirectory()).toBe(true)
  })
})
