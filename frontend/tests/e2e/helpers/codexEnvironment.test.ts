import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createCodexEnvironment } from './codexEnvironment'

let homeDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'codex-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('createCodexEnvironment', () => {
  it('writes a provider for the given base URL and the form server that the configuration starts', () => {
    const env = createCodexEnvironment({ homeDir, baseURL: 'http://127.0.0.1:4567/v1' })
    expect(env).toEqual({ CODEX_HOME: join(homeDir, '.codex') })
    const config = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('[model_providers.leapmux-e2e]\nname = "LeapMux E2E"\nbase_url = "http://127.0.0.1:4567/v1"')
    expect(config).toContain('env_key = "LEAPMUX_E2E_MODEL_API_KEY"')
    const formServer = join(env.CODEX_HOME!, 'form-server.mjs')
    expect(config).toContain(`[mcp_servers.form_probe]\ncommand = ${JSON.stringify(process.execPath)}\nargs = [${JSON.stringify(formServer)}]`)
    expect(readFileSync(formServer, 'utf8')).toContain('elicitation/create')
  })

  it('turns off the background memory turn and the update notice', () => {
    const env = createCodexEnvironment({ homeDir, baseURL: 'http://127.0.0.1:4567/v1' })
    const config = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('[memories]\ngenerate_memories = false\nuse_memories = false')
    expect(config).toMatch(/^check_for_update_on_startup = false$/m)
  })
})
