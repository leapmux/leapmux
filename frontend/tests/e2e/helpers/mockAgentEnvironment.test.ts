import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CLINE_PROVIDER_ID, createMockAgentEnvironment, KIMI_MOCK_MODELS, KIRO_E2E_API_KEY, MOCK_MODEL_IDS, MOCK_MODELS, MOCK_PROVIDER_IDS, OH_MY_PI_PROFILE, QWEN_MODEL_ID } from './mockAgentEnvironment'

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
    'http://user:secret@127.0.0.1:43210',
    'http://127.0.0.1:43210/?key=value',
    'http://127.0.0.1:43210/#fragment',
  ])('refuses a server URL that is not a loopback HTTP origin, before it writes configuration: %s', (url) => {
    expect(() => createMockAgentEnvironment(directory, url)).toThrow('loopback HTTP origin')
    expect(existsSync(join(directory, 'agent-home'))).toBe(false)
    expect(existsSync(join(directory, 'mimocode-home'))).toBe(false)
  })

  it.each([
    ['http://localhost:43210', 'http://localhost:43210'],
    ['http://[::1]:43210/', 'http://[::1]:43210'],
  ])('accepts every loopback name of the mock: %s', (url, origin) => {
    const { env } = createMockAgentEnvironment(directory, url)
    expect(env.ANTHROPIC_BASE_URL).toBe(origin)
    expect(env.OPENAI_BASE_URL).toBe(`${origin}/v1`)
    expect(env.HTTPS_PROXY).toBe(origin)
  })

  it('advertises each pinned model identifier once', () => {
    expect(new Set(MOCK_MODEL_IDS).size).toBe(MOCK_MODEL_IDS.length)
    expect([...MOCK_MODEL_IDS].sort()).toEqual([...new Set(Object.values(MOCK_MODELS))].sort())
  })

  it('states no Pi package without a real home directory to find them in', () => {
    const { piAgentDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    expect(JSON.parse(readFileSync(join(piAgentDir, 'settings.json'), 'utf8'))).toEqual({
      defaultProvider: 'zai',
      defaultModel: MOCK_MODELS.pi,
      packages: [],
    })
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

  it('writes a Codewhale configuration that reaches the mock and nothing else', () => {
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env).toMatchObject({
      CODEWHALE_HOME: join(homeDir, '.codewhale'),
      CODEWHALE_TELEMETRY: '0',
      CODEWHALE_NO_UPDATE_CHECK: '1',
      CODEWHALE_ALLOW_SHELL: '1',
    })
    const codewhale = readFileSync(join(env.CODEWHALE_HOME!, 'config.toml'), 'utf8')
    expect(codewhale).toContain('provider = "deepseek"')
    expect(codewhale).toContain(`default_text_model = "${MOCK_MODELS.deepseek}"`)
    expect(codewhale).toContain('[providers.deepseek]\nbase_url = "http://127.0.0.1:43210/v1"\napi_key = "leapmux-e2e-model-key"')
    expect(codewhale).toContain('telemetry = false')
    // Each of these stops a model request that no test scripts, or a wait that
    // would deny an approval on its own.
    expect(codewhale).toContain('[retry]\nenabled = false')
    expect(codewhale).toContain('[reasoning_only]\nmax_reprompts = 0')
    expect(codewhale).toContain('[update]\ncheck_for_updates = false')
    expect(codewhale).toContain('[snapshots]\nenabled = false')
    expect(codewhale).toContain('[tools]\nuser_input_timeout_seconds = 0')
  })

  it('writes a Kimi Code configuration that reaches only the mock', () => {
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env).toMatchObject({
      KIMI_CODE_HOME: join(homeDir, '.kimi-code'),
      KIMI_DISABLE_TELEMETRY: '1',
      KIMI_CODE_NO_AUTO_UPDATE: '1',
    })
    const kimi = readFileSync(join(env.KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    expect(kimi).toContain(`default_model = "${KIMI_MOCK_MODELS.thinking}"`)
    expect(kimi).toContain('telemetry = false')
    // A model-written title would be a request with no scenario marker.
    expect(kimi).toContain('auto_session_title = false')
    expect(kimi).toContain(`[providers.${MOCK_PROVIDER_IDS.kimi}]`)
    expect(kimi).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(kimi).toContain('api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"')
    // The key stays out of the file, as it does for every other provider that
    // can read it from the environment.
    expect(kimi).not.toContain('leapmux-e2e-model-key')
    expect(kimi).toContain(`[models."${KIMI_MOCK_MODELS.thinking}"]`)
    expect(kimi).toContain('support_efforts = ["low", "medium", "high"]')
    expect(kimi).toContain(`[models."${KIMI_MOCK_MODELS.plain}"]`)
  })

  it('maps each Kimi Code alias onto a model the catalog route already lists', () => {
    // A new identifier would change the catalog every provider reads.
    for (const alias of Object.values(KIMI_MOCK_MODELS)) {
      const [provider, model] = alias.split('/')
      expect(provider).toBe(MOCK_PROVIDER_IDS.kimi)
      expect(MOCK_MODEL_IDS).toContain(model)
    }
  })

  it('isolates Oh My Pi in its own profile, which Pi\'s variables cannot reach', () => {
    const { env, homeDir, ohMyPiAgentDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(ohMyPiAgentDir).toBe(join(homeDir, '.omp', 'profiles', OH_MY_PI_PROFILE, 'agent'))
    expect(env.OMP_PROFILE).toBe(OH_MY_PI_PROFILE)
    // Both CLIs read this name, and an empty value is unset for each of them.
    expect(env.PI_CODING_AGENT_SESSION_DIR).toBe('')
    // Only omp reads its proxy variable. It points at the refusing mock, which
    // records each host that omp tries to reach.
    expect(env.PI_PROXY).toBe('http://127.0.0.1:43210')
    // omp reads an empty value as unset, so the developer's own value cannot reach it.
    for (const name of ['PI_CONFIG_FILES', 'PI_CONFIG_DIR', 'OMP_AUTH_BROKER_URL', 'OMP_AUTH_BROKER_TOKEN'])
      expect(env[name], name).toBe('')

    const models = JSON.parse(readFileSync(join(ohMyPiAgentDir, 'models.yml'), 'utf8'))
    expect(models.providers[MOCK_PROVIDER_IDS.ohMyPi]).toMatchObject({
      baseUrl: 'http://127.0.0.1:43210/v1',
      api: 'openai-completions',
      apiKey: 'LEAPMUX_E2E_MODEL_API_KEY',
    })
    expect(models.providers[MOCK_PROVIDER_IDS.ohMyPi].models[0].id).toBe(MOCK_MODELS.ohMyPi)
  })

  it('stops every Oh My Pi request that no test scripts', () => {
    const { ohMyPiAgentDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const config = JSON.parse(readFileSync(join(ohMyPiAgentDir, 'config.yml'), 'utf8'))
    const model = `${MOCK_PROVIDER_IDS.ohMyPi}/${MOCK_MODELS.ohMyPi}`

    expect(config.modelRoles).toEqual({ default: model, task: `${model}:off` })
    expect(config.disabledProviders).toEqual(expect.arrayContaining(['anthropic', 'openai', 'ollama', 'lm-studio', 'llama.cpp', 'web']))
    expect(config.disabledProviders).not.toContain(MOCK_PROVIDER_IDS.ohMyPi)
    expect(config).toMatchObject({
      todo: { reminders: false },
      retry: { enabled: false },
      async: { enabled: false },
      edit: { mode: 'replace' },
    })
  })

  // The mock refuses the request anyway. The run must not depend on that.
  it('turns off Kilo telemetry', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    expect(env.KILO_TELEMETRY_LEVEL).toBe('off')
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

  // MiMo takes the OpenCode family's provider block under its own variable names,
  // and each switch below stops a request that no test scripts.
  it('points MiMo Code at the same inline provider and closes its other requests', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const mimo = JSON.parse(env.MIMOCODE_CONFIG_CONTENT!)

    const openCode = JSON.parse(env.OPENCODE_CONFIG_CONTENT!).provider[MOCK_PROVIDER_IDS.openCode]
    const provider = mimo.provider[MOCK_PROVIDER_IDS.openCode]
    expect(provider.options).toEqual(openCode.options)
    // The second model and the reasoning options of each variant are MiMo's alone,
    // for the settings spec that reads both off the next request.
    expect(Object.keys(provider.models)).toEqual([MOCK_MODELS.zai, MOCK_MODELS.pi])
    expect(Object.keys(openCode.models)).toEqual([MOCK_MODELS.zai])
    expect(provider.models[MOCK_MODELS.pi].variants.low).toEqual({ reasoningEffort: 'low' })
    expect(openCode.models[MOCK_MODELS.zai].variants.low).toEqual({})
    expect(Object.keys(provider.models[MOCK_MODELS.zai].variants)).toEqual(Object.keys(openCode.models[MOCK_MODELS.zai].variants))
    expect(mimo.model).toBe(`${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`)
    expect(mimo.enabled_providers).toEqual([MOCK_PROVIDER_IDS.openCode])
    expect(mimo.agent.title).toEqual({ disable: true })
    for (const retry of Object.values(mimo.retry))
      expect(retry).toEqual({ mode: 'bounded', maxRetries: 0 })
    expect(env.MIMOCODE_HOME).toBe(join(directory, 'mimocode-home'))
    expect(env).toMatchObject({
      MIMOCODE_DISABLE_PROJECT_CONFIG: 'true',
      MIMOCODE_ENABLE_QUESTION_TOOL: '1',
      MIMOCODE_ENABLE_ANALYSIS: 'false',
      MIMOCODE_DISABLE_MODELS_FETCH: 'true',
      MIMOCODE_EXPERIMENTAL_CRON: 'false',
      MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL: 'true',
      MIMOCODE_DISABLE_CHECKPOINT: 'true',
      MIMOCODE_DISABLE_CLAUDE_CODE: 'true',
      MIMOCODE_DISABLE_PROVIDER_ENV: 'true',
    })
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

  // Grok answers from its own `config.toml`, and every model request it makes
  // outside a scripted turn -- a turn summary, a title refresh, a recap, a memory
  // pass, a prompt suggestion -- would reach the mock with no test to answer it.
  it('points Grok Build at the mock and turns off its own model calls', () => {
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env.GROK_HOME).toBe(join(homeDir, '.grok'))
    const grok = readFileSync(join(env.GROK_HOME!, 'config.toml'), 'utf8')
    expect(grok).toContain('remote_fetch = false')
    expect(grok).toContain(`default = "${MOCK_MODELS.grok}"`)
    expect(grok).toContain(`session_summary = "${MOCK_MODELS.grok}"`)
    expect(grok).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(grok).toContain('api_backend = "chat_completions"')
    expect(grok).toContain('[model."grok-4.6"]\nhidden = true')
    expect(env).toMatchObject({
      GROK_DISABLE_AUTOUPDATER: '1',
      GROK_TELEMETRY_ENABLED: 'false',
      GROK_TURN_SUMMARY: '0',
      GROK_TITLE_REFRESH: '0',
      GROK_SESSION_RECAP: '0',
      GROK_PROMPT_SUGGESTIONS: '0',
      GROK_MEMORY: '0',
      GROK_LOGIN_ENV: '0',
    })
    expect(env.GROK_FILE_LOCK_SLOT_DIR!.startsWith(directory)).toBe(true)
  })

  // The mock's own origin is plain HTTP. Cursor's HTTP/2 pool reads HTTP_PROXY for an
  // `http:` URL and never reads NO_PROXY, so a plain-HTTP proxy would carry even
  // Cursor's calls to the mock into the refusing proxy, and every Cursor turn would
  // fail. Every real host is HTTPS, so the HTTPS proxy alone keeps each of them
  // closed.
  it('routes HTTPS alone through the refusing proxy, and keeps loopback direct', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env.HTTPS_PROXY).toBe('http://127.0.0.1:43210')
    expect(env.https_proxy).toBe('http://127.0.0.1:43210')
    for (const name of ['HTTP_PROXY', 'http_proxy'])
      expect(env[name], name).toBeUndefined()
    for (const name of ['NO_PROXY', 'no_proxy'])
      expect(env[name]!.split(','), name).toEqual(['127.0.0.1', 'localhost', '::1'])
  })

  // Kiro reads every endpoint from its own `cli.json`, and it opens remote sessions
  // on its own host whatever the settings state. The refusing proxy stops those.
  it('points Kiro at the mock, closes every other host, and turns off its own model calls', () => {
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env.KIRO_HOME).toBe(join(homeDir, '.kiro'))
    expect(env.KIRO_DATA_DIR!.startsWith(directory)).toBe(true)
    expect(env.KIRO_API_KEY).toMatch(/^ksk_/)
    // The mock refuses any other bearer, so the key that the agent sends is the one
    // key that the mock takes.
    expect(env.KIRO_API_KEY).toBe(KIRO_E2E_API_KEY)
    // No AWS profile or credential of the developer reaches the agent.
    expect(env).toMatchObject({
      AWS_PROFILE: 'default',
      AWS_CONFIG_FILE: join(homeDir, '.aws', 'config'),
      AWS_SHARED_CREDENTIALS_FILE: join(homeDir, '.aws', 'credentials'),
      AWS_ACCESS_KEY_ID: '',
      AWS_SECRET_ACCESS_KEY: '',
      AWS_SESSION_TOKEN: '',
    })
    const settings = JSON.parse(readFileSync(join(env.KIRO_HOME!, 'settings', 'cli.json'), 'utf8'))
    const service = { endpoint: 'http://127.0.0.1:43210', region: 'us-east-1' }
    expect(settings).toEqual({
      'api.krs.service': service,
      'api.cps.service': service,
      'api.codewhisperer.service': service,
      'api.q.service': 'http://127.0.0.1:43210',
      'api.kiroauth.service': 'http://127.0.0.1:43210',
      'telemetry.enabled': false,
      'app.disableAutoupdates': true,
    })
    expect(env).toMatchObject({
      HTTPS_PROXY: 'http://127.0.0.1:43210',
      https_proxy: 'http://127.0.0.1:43210',
      KIRO_DISABLE_SESSION_TITLE_LLM: 'true',
      KIRO_DISABLE_RECAP: 'true',
      KIRO_DISABLE_EXPERIMENT_CONFIG: 'true',
      KIRO_DISABLE_TELEMETRY: '1',
      KIRO_NO_AUTO_UPDATE: '1',
      KIRO_NO_REMOTE_CHANGELOG: '1',
    })
  })

  it('points Amp at the mock\'s own service with a fake key, and keeps its data in the isolated home', () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    expect(env).toMatchObject({
      AMP_URL: 'http://127.0.0.1:43210',
      AMP_API_KEY: 'leapmux-e2e-model-key',
      RIVET_PUBLIC_ENDPOINT: 'http://127.0.0.1:43210/actors',
      RIVET_POOL: '',
      AMP_SETTINGS_FILE: '',
      AMP_SKIP_UPDATE_CHECK: '1',
      XDG_CONFIG_HOME: join(homeDir, '.config'),
      XDG_DATA_HOME: join(homeDir, '.local', 'share'),
      XDG_CACHE_HOME: join(homeDir, '.cache'),
      XDG_STATE_HOME: join(homeDir, '.local', 'state'),
    })
    for (const key of ['XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME', 'XDG_STATE_HOME'])
      expect(env[key]!.startsWith(realHomeDir), key).toBe(false)
  })

  it('points Cline at the mock through settings in the isolated home, with telemetry off', () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    const clineDir = join(homeDir, '.cline')
    const dataDir = join(clineDir, 'data')
    expect(env).toMatchObject({
      CLINE_DIR: clineDir,
      CLINE_DATA_DIR: dataDir,
      CLINE_PROVIDER_SETTINGS_PATH: '',
      CLINE_GLOBAL_SETTINGS_PATH: '',
      CLINE_DB_DATA_DIR: '',
      CLINE_SESSION_DATA_DIR: '',
      CLINE_TEAM_DATA_DIR: '',
      CLINE_MCP_SETTINGS_PATH: '',
      CLINE_API_KEY: '',
      CLINE_NO_AUTO_UPDATE: '1',
    })
    expect(env.CLINE_DIR!.startsWith(realHomeDir)).toBe(false)
    // Cline reads these two with `??`, where an empty value is a value.
    expect(env).not.toHaveProperty('CLINE_PROVIDER')
    expect(env).not.toHaveProperty('CLINE_MODEL')

    const providers = JSON.parse(readFileSync(join(dataDir, 'settings', 'providers.json'), 'utf8'))
    expect(providers).toEqual({
      version: 1,
      lastUsedProvider: CLINE_PROVIDER_ID,
      modes: {},
      providers: {
        [CLINE_PROVIDER_ID]: {
          settings: { provider: CLINE_PROVIDER_ID, apiKey: 'leapmux-e2e-model-key', model: MOCK_MODELS.cline, baseUrl: 'http://127.0.0.1:43210/v1' },
          // Cline ignores a file whose entry states no UTC update time.
          updatedAt: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/),
          tokenSource: 'manual',
        },
      },
    })
    expect(MOCK_MODEL_IDS).toContain(MOCK_MODELS.cline)
    expect(JSON.parse(readFileSync(join(dataDir, 'settings', 'global-settings.json'), 'utf8'))).toEqual({ telemetryOptOut: true, autoUpdateEnabled: false })
    const flags = JSON.parse(readFileSync(join(dataDir, 'cache', 'feature-flags.json'), 'utf8'))
    expect(flags).toMatchObject({ version: 2, userId: null, flagsPayload: { featureFlags: {}, featureFlagPayloads: {} } })
    expect(flags.updatedAt).toBeGreaterThan(0)
  })

  it('states every Cline variable that moves one part of the data, so no developer value reaches a daemon', () => {
    const { env } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    // Every `CLINE_*_DIR` and `CLINE_*_PATH` that sdk/packages/shared/src/storage/paths.ts
    // of Cline 3.0.64 reads, the hook and log files, and the build environment,
    // which decides the owner of the shared stores.
    const moved = [
      'CLINE_DIR',
      'CLINE_DATA_DIR',
      'CLINE_SESSION_DATA_DIR',
      'CLINE_TEAM_DATA_DIR',
      'CLINE_CONNECTOR_DATA_DIR',
      'CLINE_CONNECTOR_SETTINGS_PATH',
      'CLINE_DB_DATA_DIR',
      'CLINE_CONNECTORS_DB_PATH',
      'CLINE_CRON_DB_PATH',
      'CLINE_TASKS_DB_PATH',
      'CLINE_PROVIDER_SETTINGS_PATH',
      'CLINE_GLOBAL_SETTINGS_PATH',
      'CLINE_MCP_SETTINGS_PATH',
      'CLINE_HOOKS_DIR',
      'CLINE_HOOKS_LOG_PATH',
      'CLINE_LOG_PATH',
      'CLINE_CAPTURE_DIR',
      'CLINE_TOOL_APPROVAL_DIR',
    ]
    for (const key of moved)
      expect(env, key).toHaveProperty(key)
    for (const key of moved.filter(key => key !== 'CLINE_DIR' && key !== 'CLINE_DATA_DIR'))
      expect(env[key], `${key} follows CLINE_DATA_DIR`).toBe('')
    // An explicit build environment wins over a developer's NODE_ENV, which would
    // otherwise move a daemon to Cline's development stores.
    expect(env.CLINE_BUILD_ENV).toBe('production')
  })

  it('points Qwen Code at the mock with the safe approval mode and no background calls', () => {
    const { env, homeDir } = createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env.QWEN_HOME).toBe(join(homeDir, '.qwen'))
    const qwen = JSON.parse(readFileSync(join(env.QWEN_HOME!, 'settings.json'), 'utf8'))
    expect(qwen.modelProviders.openai[0]).toMatchObject({ id: MOCK_MODELS.qwen, baseUrl: 'http://127.0.0.1:43210/v1', envKey: 'LEAPMUX_E2E_MODEL_API_KEY' })
    expect(qwen.model.name).toBe(MOCK_MODELS.qwen)
    expect(qwen.tools.approvalMode).toBe('default')
    expect(qwen.memory).toEqual({ enableManagedAutoMemory: false, enableManagedAutoDream: false })
    expect(qwen.ui.enableFollowupSuggestions).toBe(false)
    expect(qwen.privacy.usageStatisticsEnabled).toBe(false)
    expect(env).toMatchObject({ QWEN_DISABLE_AUTO_TITLE: '1', QWEN_USAGE_STATISTICS_ENABLED: 'false', LEAPMUX_E2E_MODEL_API_KEY: 'leapmux-e2e-model-key' })
    expect(QWEN_MODEL_ID).toBe(`${MOCK_MODELS.qwen}(openai)`)
  })
})
