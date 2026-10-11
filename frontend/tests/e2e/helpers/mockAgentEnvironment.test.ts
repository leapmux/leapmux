import type { ExecFileSyncOptions } from 'node:child_process'
import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { basename, delimiter, isAbsolute, join, resolve, sep } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { agentSearchPathEnv, findBinary } from './binaryOnPath'
import { createMockAgentEnvironment, KIMI_MOCK_MODELS, MOCK_MODEL_IDS, MOCK_MODELS, MOCK_PROVIDER_IDS, PI_PLAIN_MODEL_ID, REASONIX_ALT_MODEL_ID, REASONIX_ALT_PROVIDER_ID, REASONIX_PLAIN_MODEL_ID, REASONIX_PLAIN_PROVIDER_ID, ZCODE_PLAIN_MODEL_WIRE_ID } from './mockAgentEnvironment'

const setupBoundary = vi.hoisted(() => {
  const original: { execute?: typeof import('node:child_process').execFileSync } = {}
  return { execute: vi.fn<typeof import('node:child_process').execFileSync>(), original }
})

/**
 * Run real subprocesses, but answer the installed Letta setup commands with no output.
 * A real `letta` spawn depends on the installed CLI and on the machine load, and these tests check the environment only.
 * The Letta setup test supplies its own controlled binary through mockImplementationOnce.
 */
function executeWithoutInstalledLetta(actual: typeof import('node:child_process').execFileSync) {
  return (file: string, args?: readonly string[], options?: ExecFileSyncOptions) => basename(file) === 'letta' ? Buffer.alloc(0) : actual(file, args, options)
}

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  setupBoundary.original.execute = actual.execFileSync
  setupBoundary.execute.mockImplementation(executeWithoutInstalledLetta(actual.execFileSync))
  return { ...actual, execFileSync: setupBoundary.execute }
})

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'mock-agent-env-test-'))
})

afterEach(() => {
  const original = setupBoundary.original.execute
  setupBoundary.execute.mockReset()
  if (original)
    setupBoundary.execute.mockImplementation(executeWithoutInstalledLetta(original))
  rmSync(directory, { recursive: true, force: true })
})

describe('createMockAgentEnvironment', () => {
  it('replaces all inherited temporary roots with one created private directory', async () => {
    const keys = ['TMPDIR', 'TEMP', 'TMP'] as const
    const before = keys.map(key => process.env[key])
    for (const key of keys)
      process.env[key] = join(directory, 'foreign-native-temp')
    try {
      const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
      const expected = join(directory, 'tmp')
      for (const key of keys)
        expect(env[key], key).toBe(expected)
      expect(isAbsolute(expected)).toBe(true)
      const info = statSync(expected)
      expect(info.isDirectory()).toBe(true)
      if (process.platform !== 'win32')
        expect(info.mode & 0o777).toBe(0o700)
    }
    finally {
      for (const [index, key] of keys.entries()) {
        const value = before[index]
        if (value === undefined)
          delete process.env[key]
        else
          process.env[key] = value
      }
    }
  })

  it('keeps independent native temporary roots for two mock runs', async () => {
    const firstRoot = join(directory, 'first-run')
    const secondRoot = join(directory, 'second-run')
    const [first, second] = await Promise.all([
      createMockAgentEnvironment(firstRoot, 'http://127.0.0.1:43210'),
      createMockAgentEnvironment(secondRoot, 'http://127.0.0.1:43210'),
    ])
    expect(first.env.TMPDIR).toBe(join(firstRoot, 'tmp'))
    expect(second.env.TMPDIR).toBe(join(secondRoot, 'tmp'))
    expect(first.env.TMPDIR).not.toBe(second.env.TMPDIR)
    expect(statSync(join(firstRoot, 'tmp')).isDirectory()).toBe(true)
    expect(statSync(join(secondRoot, 'tmp')).isDirectory()).toBe(true)
  })

  // Letta Code runs `npm install -g` of its newest release from its startup path
  // unless DISABLE_AUTOUPDATER is exactly "1". Both setup commands are subcommands,
  // which exit before that path today. They run outside the worker, which pins the
  // switch for each agent, and without the refusing proxy, so the switch is the
  // only guard if a later release moves the update before the subcommands.

  it('passes its private temporary root to an actual Node subprocess', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const output = execFileSync(process.execPath, ['-e', 'process.stdout.write(require("node:os").tmpdir())'], {
      env: { ...process.env, ...env },
      encoding: 'utf8',
    })
    expect(output).toBe(join(directory, 'tmp'))
  })

  it.runIf(process.platform !== 'win32')('keeps mise install directories after inserting credential-store shims', async () => {
    const installDir = join(directory, 'mise-install')
    const miseDir = join(directory, 'mise-bin')
    const shimDir = join(directory, 'mise-shims')
    mkdirSync(installDir)
    mkdirSync(miseDir)
    mkdirSync(shimDir)
    const mise = join(miseDir, 'mise')
    writeFileSync(mise, `#!/bin/sh\nif [ "$1" = bin-paths ]; then\n  printf '%s\\n' '${installDir}'\nfi\n`, { mode: 0o755 })
    symlinkSync(mise, join(shimDir, 'sample-agent'))

    const before = process.env.PATH
    process.env.PATH = shimDir
    try {
      const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
      expect(env.PATH?.split(delimiter)).toEqual([join(directory, 'cli-shims'), installDir, shimDir])
    }
    finally {
      if (before === undefined)
        delete process.env.PATH
      else
        process.env.PATH = before
    }
  })

  it.each([
    'https://127.0.0.1:43210',
    'http://example.com:43210',
    'http://127.0.0.1:43210/prefix',
    'http://user:secret@127.0.0.1:43210',
    'http://127.0.0.1:43210/?key=value',
    'http://127.0.0.1:43210/#fragment',
  ])('refuses a server URL that is not a loopback HTTP origin, before it writes configuration: %s', async (url) => {
    await expect(createMockAgentEnvironment(directory, url)).rejects.toThrow('loopback HTTP origin')
    expect(existsSync(join(directory, 'agent-home'))).toBe(false)
    expect(existsSync(join(directory, 'mimocode-home'))).toBe(false)
  })

  it.each([
    ['http://localhost:43210', 'http://localhost:43210'],
    ['http://[::1]:43210/', 'http://[::1]:43210'],
  ])('accepts every loopback name of the mock: %s', async (url, origin) => {
    const { env } = await createMockAgentEnvironment(directory, url)
    expect(env.ANTHROPIC_BASE_URL).toBe(origin)
    expect(env.OPENAI_BASE_URL).toBe(`${origin}/v1`)
    expect(env.HTTPS_PROXY).toBe(origin)
  })

  it('advertises each pinned model identifier once', async () => {
    expect(new Set(MOCK_MODEL_IDS).size).toBe(MOCK_MODEL_IDS.length)
    expect([...MOCK_MODEL_IDS].sort()).toEqual([...new Set(Object.values(MOCK_MODELS))].sort())
  })

  it('states no Pi package without a real home directory to find them in', async () => {
    const { piAgentDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    expect(JSON.parse(readFileSync(join(piAgentDir, 'settings.json'), 'utf8'))).toEqual({
      defaultProvider: 'zai',
      defaultModel: MOCK_MODELS.pi,
      compaction: { keepRecentTokens: 32 },
      packages: [],
    })
  })

  it('configures native Pi MCP without the removed adapter package', async () => {
    const realHomeDir = join(directory, 'real-home')
    const { piAgentDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })
    const settings = JSON.parse(readFileSync(join(piAgentDir, 'settings.json'), 'utf8'))
    expect(settings.packages).not.toContain(join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules', 'pi-mcp-adapter'))
    expect(JSON.parse(readFileSync(join(piAgentDir, 'mcp.json'), 'utf8'))).toEqual({
      mcpServers: { echo_probe: { command: process.execPath, args: [join(directory, 'mcp-echo.mjs')], exposure: 'direct' } },
    })
  })

  it('disables native Pi management requests in the private profile', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    expect(env.PI_OFFLINE).toBe('1')
    expect(env.PI_SKIP_VERSION_CHECK).toBe('1')
  })

  // Pi reads AGENTS.md and its relatives from each directory above its working directory, up to the root of the file
  // system, and only a flag turns that off. Each agent finds Pi through the PATH of the run.
  it.runIf(process.platform !== 'win32')('gives each agent the private Pi wrapper, which turns off the context files', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const wrapper = join(directory, 'cli-shims', 'pi')
    const installed = findBinary('pi', { ...process.env, ...agentSearchPathEnv() })
    if (installed === null) {
      expect(existsSync(wrapper)).toBe(false)
      return
    }
    expect(findBinary('pi', { PATH: env.PATH })).toBe(wrapper)
    expect(readFileSync(wrapper, 'utf8')).toContain(`exec '${installed}' --no-context-files "$@"`)
  })

  it('routes direct endpoint providers to the mock server', async () => {
    const { env, homeDir, piAgentDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env).toMatchObject({
      HOME: homeDir,
      ANTHROPIC_BASE_URL: 'http://127.0.0.1:43210',
      OPENAI_BASE_URL: 'http://127.0.0.1:43210/v1',
      COPILOT_API_URL: 'http://127.0.0.1:43210',
      COPILOT_GITHUB_TOKEN: 'github_pat_leapmuxe2e000000000000000000000000000000000000000000',
      GITHUB_COPILOT_API_TOKEN: 'leapmux-e2e-model-key',
    })
    expect(env.PI_CODING_AGENT_DIR).toBe(piAgentDir)
    expect(MOCK_MODELS.goose).toBe('gpt-4o')
    expect(MOCK_MODELS.gooseReasoning).toBe('gpt-5.4')
    expect(env.GOOSE_MODEL).toBe(MOCK_MODELS.goose)
    expect(MOCK_MODEL_IDS).toContain(MOCK_MODELS.gooseReasoning)
    expect(env.GOOSE_PATH_ROOT).toBe(join(homeDir, '.goose'))
    const gooseConfig = readFileSync(join(env.GOOSE_PATH_ROOT!, 'config', 'config.yaml'), 'utf8')
    expect(gooseConfig).toContain('todo:\n    enabled: true\n    type: platform\n    name: todo')
    const gooseFormServer = join(env.GOOSE_PATH_ROOT!, 'form-server.mjs')
    expect(gooseConfig).toContain('form_probe:\n    enabled: true\n    type: stdio\n    name: form_probe')
    expect(gooseConfig).toContain(`cmd: ${JSON.stringify(process.execPath)}`)
    expect(gooseConfig).toContain(`- ${JSON.stringify(gooseFormServer)}`)
    expect(readFileSync(gooseFormServer, 'utf8')).toContain('elicitation/create')
  })

  // Cursor's configuration used to live in the REAL home directory, because it
  // was the one provider that still needed a live account. It answers to the
  // mock now, so a run must neither read nor write the developer's own Cursor
  // configuration -- and must never reach for the macOS keychain, which raises
  // a modal dialog that nothing on a test machine answers.
  it('isolates Cursor from the real home directory and the keychain', async () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    expect(env.CURSOR_CONFIG_DIR).toBe(join(homeDir, '.cursor'))
    expect(env.CURSOR_CONFIG_DIR!.startsWith(realHomeDir)).toBe(false)
    expect(env.CURSOR_API_ENDPOINT).toBe('http://127.0.0.1:43210')
    expect(env.CURSOR_AUTH_TOKEN).toBe('leapmux-e2e-model-key')
    expect(env.AGENT_CLI_CREDENTIAL_STORE).toBe('memory')
  })

  it('creates each directory that a variable of the run gives', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    // A spec that asserts a private path needs the directory before the agent writes to it.
    // A spec that runs first on a shard sees no earlier agent write. The Claude Code credential spec failed that way.
    const directoryVariables = Object.entries(env).filter(([name]) => /^HOME$|_DIR$|_HOME$|_ROOT$|_OVERRIDE$/.test(name))
    for (const name of ['CLAUDE_CONFIG_DIR', 'ZCODE_STORAGE_DIR', 'XDG_DATA_HOME'])
      expect(directoryVariables.map(([variable]) => variable), `${name} is a directory variable of the run`).toContain(name)
    for (const [name, path] of directoryVariables) {
      if (!path?.startsWith(`${directory}${sep}`))
        continue
      expect(existsSync(path) && statSync(path).isDirectory(), `${name} exists as a directory`).toBe(true)
    }
  })

  it('writes Codex, Pi, Reasonix, and ZCode provider files', async () => {
    const realHomeDir = join(directory, 'real-home')
    const { env, homeDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210', { realHomeDir })

    const codex = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(codex).toContain('model_provider = "leapmux-e2e"')
    expect(codex).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(codex).toContain('wire_api = "responses"')
    expect(codex).toContain('[mcp_servers.form_probe]')
    expect(codex).toContain(`args = [${JSON.stringify(join(env.CODEX_HOME!, 'form-server.mjs'))}]`)
    expect(readFileSync(join(env.CODEX_HOME!, 'form-server.mjs'), 'utf8')).toContain('elicitation/create')

    const pi = JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'models.json'), 'utf8'))
    expect(pi.providers.zai).toMatchObject({
      baseUrl: 'http://127.0.0.1:43210/v1',
      api: 'openai-completions',
      apiKey: 'leapmux-e2e-model-key',
    })
    expect(pi.providers.zai.models[0].id).toBe('glm-5.3')
    expect(pi.providers.zai.models[0].input).toEqual(['text', 'image'])
    expect(pi.providers.zai.models[1].id).toBe(MOCK_MODELS.zai)
    expect(pi.providers.zai.models[1].compat.supportsReasoningEffort).toBe(true)
    expect(pi.providers.zai.models[2]).toMatchObject({ id: PI_PLAIN_MODEL_ID, reasoning: false })
    expect(JSON.parse(readFileSync(join(env.PI_CODING_AGENT_DIR!, 'settings.json'), 'utf8')).packages).toContain(
      join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules', 'pi-goal-x'),
    )

    const reasonix = readFileSync(join(env.REASONIX_HOME!, 'config.toml'), 'utf8')
    expect(reasonix).toContain('base_url = "http://127.0.0.1:43210/v1"')
    expect(reasonix).toContain('api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"')
    const reasonixCredentials = join(env.REASONIX_HOME!, '.env')
    expect(readFileSync(reasonixCredentials, 'utf8')).toBe('LEAPMUX_E2E_MODEL_API_KEY=leapmux-e2e-model-key\n')
    expect(statSync(reasonixCredentials).mode & 0o777).toBe(0o600)
    expect(reasonix).toContain(`vision_models = ["${MOCK_MODELS.deepseek}"]`)
    expect(reasonix).toContain(`default_model = "deepseek/${MOCK_MODELS.deepseek}"`)
    expect(reasonix).toContain(`name = "${REASONIX_ALT_PROVIDER_ID}"\nkind = "openai"\nbase_url = "http://127.0.0.1:43210/v1"\nmodel = "${MOCK_MODELS.pi}"`)
    expect(REASONIX_ALT_MODEL_ID).toBe(`${REASONIX_ALT_PROVIDER_ID}/${MOCK_MODELS.pi}`)
    expect(reasonix).toContain(`name = "${REASONIX_PLAIN_PROVIDER_ID}"\nkind = "openai"\nbase_url = "http://127.0.0.1:43210/v1"\nmodel = "${MOCK_MODELS.zai}"`)
    expect(REASONIX_PLAIN_MODEL_ID).toBe(`${REASONIX_PLAIN_PROVIDER_ID}/${MOCK_MODELS.zai}`)

    const zcodePath = join(homeDir, '.zcode', 'v2', 'config.json')
    const zcodePersonalPath = join(homeDir, '.zcode', 'v2', 'provider_config.json')
    expect(env.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE).toBe(zcodePersonalPath)
    const zcode = JSON.parse(readFileSync(zcodePath, 'utf8'))
    expect(zcode.provider[MOCK_PROVIDER_IDS.zcode].options).toEqual({
      apiKey: 'leapmux-e2e-model-key',
      baseURL: 'http://127.0.0.1:43210/v1',
    })
    expect(Object.keys(zcode.provider[MOCK_PROVIDER_IDS.zcode].models)).toEqual([MOCK_MODELS.zai, MOCK_MODELS.pi, ZCODE_PLAIN_MODEL_WIRE_ID])
    const zcodePersonal = JSON.parse(readFileSync(zcodePersonalPath, 'utf8'))
    expect(zcodePersonal).toMatchObject({
      schemaVersion: 1,
      config: {
        defaultModelSelection: { providerId: MOCK_PROVIDER_IDS.zcode, modelId: MOCK_MODELS.zai },
      },
    })
    const personalProvider = zcodePersonal.config.providerConfigRules.providerRules[0]
    expect(personalProvider.config.personalModelIds).toEqual([MOCK_MODELS.zai, MOCK_MODELS.pi, ZCODE_PLAIN_MODEL_WIRE_ID])
    expect(personalProvider.config.modelOrder).toEqual([MOCK_MODELS.zai, MOCK_MODELS.pi, ZCODE_PLAIN_MODEL_WIRE_ID])
    expect(zcodePersonal.config.modelConfigRules.providerModelRules.map((rule: { modelId: string }) => rule.modelId)).toEqual([MOCK_MODELS.zai, MOCK_MODELS.pi, ZCODE_PLAIN_MODEL_WIRE_ID])
  })

  it('maps each Kimi Code alias onto a model the catalog route already lists', async () => {
    // A new identifier would change the catalog every provider reads.
    for (const alias of Object.values(KIMI_MOCK_MODELS)) {
      const [provider, model] = alias.split('/')
      expect(provider).toBe(MOCK_PROVIDER_IDS.kimi)
      expect(MOCK_MODEL_IDS).toContain(model)
    }
  })

  // The mock refuses the request anyway. The run must not depend on that.
  it('turns off Kilo telemetry', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    expect(env.KILO_TELEMETRY_LEVEL).toBe('off')
  })

  it('supplies equivalent inline OpenCode and Kilo providers', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const openCode = JSON.parse(env.OPENCODE_CONFIG_CONTENT!)
    const kilo = JSON.parse(env.KILO_CONFIG_CONTENT!)

    expect(openCode).toEqual(kilo)
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].options).toEqual({
      apiKey: 'leapmux-e2e-model-key',
      baseURL: 'http://127.0.0.1:43210/v1',
    })
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].models[MOCK_MODELS.zai]).toBeDefined()
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].models[MOCK_MODELS.pi]).toBeDefined()
    expect(openCode.provider[MOCK_PROVIDER_IDS.openCode].models[MOCK_MODELS.zai].variants.low).toEqual({ reasoningEffort: 'low' })
    expect(openCode.model).toBe(`${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`)
    expect(openCode.enabled_providers).toEqual([MOCK_PROVIDER_IDS.openCode])
    expect(openCode.compaction).toEqual({ tail_turns: 2 })
    expect(openCode.mcp.echo_probe).toEqual({ type: 'local', command: [process.execPath, join(directory, 'mcp-echo.mjs')] })
  })

  // MiMo takes the OpenCode family's provider block under its own variable names.
  // Each switch below stops a request that no test scripts.

  /**
   * OpenCode and Kilo merge configured providers into built-in entries with the same ID.
   * Kilo then retains its real gateway URL.
   * A correct answer alone cannot prove that a model request reached the mock.
   */
  it('uses a provider id that no public catalog holds', async () => {
    expect(MOCK_PROVIDER_IDS.openCode).toBe('leapmux-e2e')
    for (const id of Object.values(MOCK_PROVIDER_IDS))
      expect(id).toContain('leapmux-e2e')
  })

  it('disables the Codex background memory turn, which carries no scenario marker', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const codex = readFileSync(join(env.CODEX_HOME!, 'config.toml'), 'utf8')
    expect(codex).toContain('[memories]')
    expect(codex).toContain('generate_memories = false')
    expect(codex).toContain('use_memories = false')
    expect(codex).toContain('[tools.update_plan]\nenabled = true')
  })

  // Grok answers from its own `config.toml`, and every model request it makes
  // outside a scripted turn -- a turn summary, a title refresh, a recap, a memory
  // pass, a prompt suggestion -- would reach the mock with no test to answer it.

  // The effort specs switch among the three models: one without an effort, and two whose ladders share a level.

  // The mock's own origin is plain HTTP. Cursor's HTTP/2 pool reads HTTP_PROXY for an
  // `http:` URL and never reads NO_PROXY, so a plain-HTTP proxy would carry even
  // Cursor's calls to the mock into the refusing proxy, and every Cursor turn would
  // fail. Every real host is HTTPS, so the HTTPS proxy alone keeps each of them
  // closed.
  it('routes HTTPS alone through the refusing proxy, and keeps loopback direct', async () => {
    const { env } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')

    expect(env.HTTPS_PROXY).toBe('http://127.0.0.1:43210')
    expect(env.https_proxy).toBe('http://127.0.0.1:43210')
    for (const name of ['HTTP_PROXY', 'http_proxy'])
      expect(env[name], name).toBeUndefined()
    for (const name of ['NO_PROXY', 'no_proxy'])
      expect(env[name]!.split(','), name).toEqual(['127.0.0.1', 'localhost', '::1'])
  })

  // Kiro reads its model endpoints from cli.json and its remote endpoint from the environment.
  // The proxy refuses every real host if a native endpoint lacks an override.

  it.runIf(process.platform === 'darwin' && existsSync('/bin/zsh'))('keeps the credential stub ahead of the keychain after shell startup', async () => {
    const hostileStartup = join(directory, 'hostile-zdotdir')
    mkdirSync(hostileStartup)
    writeFileSync(join(hostileStartup, '.zshenv'), 'export PATH=/usr/bin:/bin\n')
    const { env, homeDir } = await createMockAgentEnvironment(directory, 'http://127.0.0.1:43210')
    const shimDir = env.PATH?.split(delimiter)[0]
    expect(shimDir).toBeTruthy()
    for (const flags of ['-lic', '-ic']) {
      const resolved = execFileSync('/bin/zsh', [flags, 'command -v security || true; command -v junie || true'], {
        cwd: directory,
        env: { ...process.env, ZDOTDIR: hostileStartup, ...env },
        encoding: 'utf8',
      }).trim().split('\n')
      expect(resolved[0]).toBe(join(shimDir!, 'security'))
    }
    expect(env.ZDOTDIR).toBe(homeDir)
  })
})
