import { mkdirSync, mkdtempSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createCopilotEnvironment } from './copilotEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'copilot-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createCopilotEnvironment', () => {
  it('points both Copilot APIs at the given origin, with its state in the isolated home and its updater off', () => {
    const env = createCopilotEnvironment({ homeDir, origin: 'http://127.0.0.1:4567', modelKey: 'unit-key', githubToken: 'github_pat_unit' })
    expect(env).toEqual({
      COPILOT_API_URL: 'http://127.0.0.1:4567',
      COPILOT_DEBUG_GITHUB_API_URL: 'http://127.0.0.1:4567',
      COPILOT_GITHUB_TOKEN: 'github_pat_unit',
      COPILOT_HOME: join(homeDir, '.copilot'),
      GITHUB_COPILOT_API_TOKEN: 'unit-key',
      COPILOT_AUTO_UPDATE: 'false',
    })
    expect(statSync(env.COPILOT_HOME!).isDirectory()).toBe(true)
  })
})
