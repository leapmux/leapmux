import { mkdirSync, mkdtempSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createCursorEnvironment } from './cursorEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'cursor-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createCursorEnvironment', () => {
  it('points the CLI at the given backend, with its configuration in the isolated home and its credential in memory', () => {
    const env = createCursorEnvironment({ homeDir, origin: 'http://127.0.0.1:4567', modelKey: 'unit-key' })
    expect(env).toEqual({
      CURSOR_CONFIG_DIR: join(homeDir, '.cursor'),
      CURSOR_API_ENDPOINT: 'http://127.0.0.1:4567',
      CURSOR_AUTH_TOKEN: 'unit-key',
      AGENT_CLI_CREDENTIAL_STORE: 'memory',
    })
    expect(statSync(env.CURSOR_CONFIG_DIR!).isDirectory()).toBe(true)
  })
})
