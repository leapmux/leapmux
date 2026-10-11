import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createGooseEnvironment } from './gooseEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'goose-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createGooseEnvironment', () => {
  it('selects the OpenAI provider with the given model, under its own root', () => {
    expect(createGooseEnvironment({ homeDir, modelID: 'unit-model' })).toEqual({
      GOOSE_PROVIDER: 'openai',
      GOOSE_MODEL: 'unit-model',
      GOOSE_PATH_ROOT: join(homeDir, '.goose'),
    })
  })

  it('turns the Todo extension on and lists the form server that it writes', () => {
    const env = createGooseEnvironment({ homeDir, modelID: 'unit-model' })
    const config = readFileSync(join(env.GOOSE_PATH_ROOT!, 'config', 'config.yaml'), 'utf8')
    expect(config).toContain('todo:\n    enabled: true\n    type: platform')
    const formServer = join(env.GOOSE_PATH_ROOT!, 'form-server.mjs')
    expect(config).toContain(`form_probe:\n    enabled: true\n    type: stdio\n    name: form_probe`)
    expect(config).toContain(`cmd: ${JSON.stringify(process.execPath)}\n    args:\n      - ${JSON.stringify(formServer)}\n`)
    expect(readFileSync(formServer, 'utf8')).toContain('elicitation/create')
  })
})
