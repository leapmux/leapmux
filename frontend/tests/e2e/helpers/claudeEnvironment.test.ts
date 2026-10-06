import { mkdirSync, mkdtempSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createClaudeEnvironment } from './claudeEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'claude-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createClaudeEnvironment', () => {
  it('points Claude Code at the given endpoint and key, and creates its configuration directory', () => {
    const env = createClaudeEnvironment({ homeDir, modelURL: 'http://127.0.0.1:4567', modelKey: 'unit-key' })
    expect(env).toEqual({
      ANTHROPIC_API_KEY: 'unit-key',
      ANTHROPIC_BASE_URL: 'http://127.0.0.1:4567',
      CLAUDE_CONFIG_DIR: join(homeDir, '.claude'),
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
      DISABLE_TELEMETRY: '1',
    })
    expect(statSync(env.CLAUDE_CONFIG_DIR!).isDirectory()).toBe(true)
  })
})
