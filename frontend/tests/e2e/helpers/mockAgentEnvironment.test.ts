import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createMockAgentEnvironment, MOCK_MODELS, MOCK_PROVIDER_IDS } from './mockAgentEnvironment'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'mock-agent-env-test-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('createMockAgentEnvironment', () => {
  it.each([
    'https://127.0.0.1:43210',
    'http://example.com:43210',
    'http://127.0.0.1:43210/prefix',
  ])('refuses a non-local server URL before it writes configuration: %s', (url) => {
    expect(() => createMockAgentEnvironment(directory, url)).toThrow('loopback HTTP origin')
  })

  it('routes direct endpoint providers to the mock server', () => {
    const { env, homeDir, piAgentDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env).toMatchObject({
      HOME: homeDir,
      ANTHROPIC_BASE_URL: 'http://127.0.0.1:43210',
      OPENAI_BASE_URL: 'http://127.0.0.1:43210/v1',
      COPILOT_API_URL: 'http://127.0.0.1:43210',
      COPILOT_GITHUB_TOKEN: 'github_pat_leapmuxe2e000000000000000000000000000000000000000000',
      GITHUB_COPILOT_API_TOKEN: 'leapmux-e2e-model-key',
    })
    expect(env.PI_CODING_AGENT_DIR).toBe(piAgentDir)
  })

  // Cursor's configuration used to live in the REAL home directory, because it
  // was the one provider that still needed a live account. It answers to the
  // mock now, so a run must neither read nor write the developer's own Cursor
  // configuration -- and must never reach for the macOS keychain, which raises
  // a modal dialog that nothing on a test machine answers.
  it('isolates Cursor from the real home directory and the keychain', () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    expect(env.CURSOR_CONFIG_DIR).toBe(join(homeDir, '.cursor'))
    expect(env.CURSOR_CONFIG_DIR!.startsWith(realHomeDir)).toBe(false)
    expect(env.CURSOR_API_ENDPOINT).toBe('http://127.0.0.1:43210')
    expect(env.CURSOR_AUTH_TOKEN).toBe('leapmux-e2e-model-key')
    expect(env.AGENT_CLI_CREDENTIAL_STORE).toBe('memory')
  })

  it('writes Codex, Pi, Reasonix, and ZCode provider files', () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    const codex = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(codex).toContain('model_provider = "leapmux-e2e"')
    expect(codex).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(codex).toContain('wire_api = "responses"')

    const pi = JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'models.json'), 'utf8'))
    expect(pi.providers.zai).toMatchObject({
      baseUrl: 'http://127.0.0.1:43210/v1',
      api: 'openai-completions',
      apiKey: 'leapmux-e2e-model-key',
    })
    expect(pi.providers.zai.models[0].id).toBe('glm-5.3')
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8')).packages).toContain(
      join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules', 'pi-goal-x'),
    )

    const reasonix = readFileSync(join(env.REASONIX_HOME!, 'config.toml'), 'utf8')
    expect(reasonix).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(reasonix).toContain('api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"')

    const zcodePath = join(homeDir, '.zcode', 'v2', 'config.json')
    const zcodePersonalPath = join(homeDir, '.zcode', 'v2', 'provider_config.json')
    expect(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE).toBe(zcodePersonalPath)
    const zcode = JSON.parse(readFileSync(zcodePath, 'utf8'))
    expect(zcode.provider[MOCK_PROVIDER_IDS.zcode].options).toEqual({
      apiKey: 'leapmux-e2e-model-key',
      baseURL: 'http://127.0.0.1:43210/v1',
    })
    const zcodePersonal = JSON.parse(readFileSync(zcodePersonalPath, 'utf8'))
    expect(zcodePersonal).toMatchObject({
      schemaVersion: 1,
      config: {
        defaultModelSelection: { providerId: MOCK_PROVIDER_IDS.zcode, modelId: MOCK_MODELS.zai },
      },
    })
  })

  it('supplies equivalent inline OpenCode and Kilo providers', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const openCode = JSON.parse(env.OPENCODE_CONFIG_CONTENT!)
    const kilo = JSON.parse(env.KILO_CONFIG_CONTENT!)

    expect(openCode).toEqual(kilo)
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].options).toEqual({
      apiKey: 'leapmux-e2e-model-key',
      baseURL: 'http://127.0.0.1:43210/v1',
    })
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].models[MOCK_MODELS.zai]).toBeDefined()
    expect(openCode.model).toBe(`${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`)
  })

  /**
   * OpenCode and Kilo merge a configured provider onto the built-in catalog
   * entry of the same id, and Kilo's gateway then keeps its own base URL. The
   * agent answered from the real endpoint and the mock saw no request at all,
   * while the test still passed because a real model does arithmetic correctly.
   */
  it('uses a provider id that no public catalog holds', () => {
    expect(MOCK_PROVIDER_IDS.openCode).toBe('leapmux-e2e')
    for (const id of Object.values(MOCK_PROVIDER_IDS))
      expect(id).toContain('leapmux-e2e')
  })

  it('disables the Codex background memory turn, which carries no scenario marker', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const codex = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(codex).toContain('[memories]')
    expect(codex).toContain('generate_memories = false')
    expect(codex).toContain('use_memories = false')
  })
})
