import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createFastAgentEnvironment } from './fastAgentEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'fast-agent-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createFastAgentEnvironment', () => {
  it('routes the default model through the openai block and the reasoning models through the zai block', () => {
    const env = createFastAgentEnvironment({ homeDir, baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', zaiModelID: 'unit-zai' })
    expect(env).toEqual({ FAST_AGENT_HOME: join(homeDir, '.fast-agent') })
    expect(readFileSync(join(env.FAST_AGENT_HOME!, 'fast-agent.yaml'), 'utf8')).toBe([
      'default_model: "unit-model"',
      'openai:',
      '  api_key: "unit-key"',
      '  base_url: "http://127.0.0.1:4567/v1"',
      'zai:',
      '  api_key: "unit-key"',
      '  base_url: "http://127.0.0.1:4567/v1"',
      '  default_model: "unit-zai"',
      '',
    ].join('\n'))
  })
})
