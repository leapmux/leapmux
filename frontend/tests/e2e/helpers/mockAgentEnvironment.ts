import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, mkdirSync, readdirSync, readFileSync, readlinkSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { basename, delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { agentSearchPath, agentSearchPathEnv, findBinary } from './binaryOnPath'
import { createCommandCodeEnvironment } from './commandCodeEnvironment'
import { createDeepseekHarnessEnvironment } from './deepseekHarnessEnvironment'
import { createGeminiEnvironment } from './geminiEnvironment'
import { writeMcpConfirmationServer } from './mcpConfirmationServer'
import { writeMcpEchoServer } from './mcpEchoServer'
import { writeMcpFormServer } from './mcpFormServer'
import { qoderEndpointCacheRecords } from './qoderSurface'

export const COMMAND_CODE_MODEL_ID = 'leapmux-e2e/command-code-e2e'
export const COMMAND_CODE_ALT_MODEL_ID = 'leapmux-e2e/command-code-e2e-alt'
export const GEMINI_MODEL_ID = 'gemini-2.5-pro'

export const MODEL_KEY = 'leapmux-e2e-model-key'
export const MOCK_IDENTITY_TOKEN = 'leapmux-e2e-token'
export const MOCK_SESSION_TOKEN = 'leapmux-e2e-session-token'
export const MOCK_COPILOT_GITHUB_TOKEN = 'github_pat_leapmuxe2e000000000000000000000000000000000000000000'

/**
 * Keep one catalog of model identifiers for isolated provider configuration and mock responses.
 *
 * The configuration writers and pinned settings use these identifiers.
 * The mock advertises the same identifiers, so each selected model exists in its catalog.
 */
export const MOCK_MODELS = {
  /** Claude Code, over the Anthropic Messages protocol. */
  anthropic: 'sonnet',
  /** Codex and GitHub Copilot, over the OpenAI protocols. */
  openai: 'gpt-5.6-luna',
  /** Kilo, MiMo Code, OpenCode, and ZCode. */
  zai: 'glm-5.3-flash',
  /** Goose sends images and tool calls through this Chat Completions model. */
  goose: 'gpt-4o',
  /** Goose and Copilot use this reasoning model in focused tests. */
  gooseReasoning: 'gpt-5.4',
  /** Pi, and the second model of MiMo Code, which a settings spec switches to. */
  pi: 'glm-5.3',
  /** Oh My Pi. */
  ohMyPi: 'glm-5.3',
  /** Reasonix and Codewhale. */
  deepseek: 'deepseek-flash',
  /** Grok Build uses the same model identifier in config.toml, LeapMux state, and native model requests. */
  grok: 'grok-e2e',
  /**
   * Qwen Code. Qwen states a model to its client as `<id>(<auth type>)`, so a
   * fixture that pins one uses `QWEN_MODEL_ID`.
   */
  qwen: 'qwen-e2e',
  /**
   * Cline. Its DeepSeek provider sends the configured model id unchanged.
   * The worker adds this custom id to the native provider's catalog.
   */
  cline: 'cline-e2e',
  /** Factory Droid. Its BYOK custom-model entry sends this id unchanged. */
  droid: 'droid-e2e',
  /** Factory Droid's alternate BYOK model for a native settings check. */
  droidAlt: 'droid-e2e-alt',
  /** Letta Code. The model handle is `provider/model`. */
  letta: 'letta-e2e',
  /** CodeBuddy Code. */
  codebuddy: 'codebuddy-e2e',
  /** Qoder CLI. */
  qoder: 'qoder-e2e',
  /** Junie. */
  junie: 'junie-e2e',
  /** Dirac. */
  dirac: 'dirac-e2e',
  /** Fast Agent. */
  fastagent: 'fastagent-e2e',
} as const

/**
 * Keep the provider identifiers that qualify native model IDs.
 *
 * OpenCode, Kilo, MiMo Code, and ZCode address each model as <provider>/<model>.
 * The selected identifier must match the provider registration below.
 */
export const MOCK_PROVIDER_IDS = {
  /**
   * Identify the isolated provider for OpenCode, Kilo, and MiMo Code.
   *
   * The identifier must not exist in a public catalog.
   * OpenCode and Kilo merge configuration into a built-in entry with the same ID.
   * Kilo then retains its gateway URL and sends the model request to the real service.
   */
  openCode: 'leapmux-e2e',
  /** The ZCode personal provider block below. */
  zcode: 'personal:leapmux-e2e',
  /** The Kimi Code provider table below, which also qualifies its model aliases. */
  kimi: 'leapmux-e2e',
  /** The Oh My Pi `models.yml` provider below, which qualifies its model as `<provider>/<id>`. */
  ohMyPi: 'leapmux-e2e',
} as const

/**
 * The model aliases Kimi Code's configuration declares.
 *
 * Kimi addresses a model by its ALIAS, the key of a `[models."..."]` table, and
 * sends the table's `model` to the endpoint. Each alias maps onto an identifier
 * that `MOCK_MODELS` already holds. A new identifier would enter the catalog
 * route that the mock serves to every provider, and Copilot reads that route.
 *
 * Two aliases, so a test can switch the model. The first thinks and takes an
 * effort; the second does neither, so a switch also changes the effort axis.
 */
export const KIMI_MOCK_MODELS = {
  thinking: `${MOCK_PROVIDER_IDS.kimi}/${MOCK_MODELS.zai}`,
  plain: `${MOCK_PROVIDER_IDS.kimi}/${MOCK_MODELS.pi}`,
} as const

/**
 * Select the private omp profile.
 *
 * omp and Pi both read PI_CODING_AGENT_DIR and PI_CODING_AGENT_SESSION_DIR.
 * Pi's fixture values would therefore select Pi's directories for omp.
 * OMP_PROFILE selects ~/.omp/profiles/<name>/agent and takes precedence over PI_CODING_AGENT_DIR.
 * It also takes precedence over PI_PROFILE, so an inherited value cannot select another profile.
 */
export const OH_MY_PI_PROFILE = 'leapmux-e2e'

/**
 * The hosts that every client reaches directly, past the refusing proxy: the
 * loopback addresses where the mock listens.
 */
const LOOPBACK_NO_PROXY = '127.0.0.1,localhost,::1'

/** Pi addresses a model through a named provider in its own `models.json`. */
const PI_PROVIDER_ID = 'zai'

/** Goose disables Todo by default. The isolated fixture also supplies a form server. */
function gooseConfig(formServer: string): string {
  return `extensions:
  todo:
    enabled: true
    type: platform
    name: todo
    description: Enable a todo list for goose so it can keep track of what it is doing
    display_name: Todo
    available_tools: []
  form_probe:
    enabled: true
    type: stdio
    name: form_probe
    description: Request the disposable probe form
    cmd: ${JSON.stringify(process.execPath)}
    args:
      - ${JSON.stringify(formServer)}
    envs: {}
    env_keys: []
    timeout: 120
`
}

/** Reasonix qualifies default_model with its provider identifier. */
const REASONIX_PROVIDER_ID = 'deepseek'
export const REASONIX_ALT_PROVIDER_ID = 'leapmux-e2e-alt'
export const REASONIX_ALT_MODEL_ID = `${REASONIX_ALT_PROVIDER_ID}/${MOCK_MODELS.pi}`

/**
 * The built-in Codewhale route that the isolated configuration points at the
 * mock.
 *
 * `deepseek` is the built-in provider route. Codewhale reads
 * `reasoning_content` as thinking only on a route that it knows to reason.
 * An `openai` route merged the reasoning into the answer during a probe.
 */
const CODEWHALE_PROVIDER_ID = 'deepseek'

/** The built-in Codewhale model whose route accepts image input. */
export const CODEWHALE_VISION_MODEL_ID = 'deepseek-v4-flash-vision-exp'

/** The auth type the Qwen configuration selects, which qualifies its model ids. */
const QWEN_AUTH_TYPE = 'openai'

/** The model id Qwen reports for the model its configuration pins. */
export const QWEN_MODEL_ID = `${MOCK_MODELS.qwen}(${QWEN_AUTH_TYPE})`
export const QWEN_ALT_MODEL_WIRE_ID = 'qwen-e2e-alt'
export const QWEN_ALT_MODEL_ID = `${QWEN_ALT_MODEL_WIRE_ID}(${QWEN_AUTH_TYPE})`
export const GROK_ALT_MODEL_ID = 'grok-e2e-alt'
export const OH_MY_PI_ALT_MODEL_WIRE_ID = 'glm-5.3-alt'
export const OH_MY_PI_ALT_MODEL_ID = `${MOCK_PROVIDER_IDS.ohMyPi}/${OH_MY_PI_ALT_MODEL_WIRE_ID}`
export const JUNIE_MOCK_MODEL = 'custom:mock-model'
export const JUNIE_RESPONSES_MODEL = 'custom:mock-responses'
export const JUNIE_NATIVE_EFFORT_MODEL = 'gpt-5.3-codex'
export const JUNIE_PROXY_PROVIDER = 'leapmux-e2e-openai'
export const FAST_AGENT_MOCK_MODEL = 'gpt-4o'

/**
 * The model id a fixture pins for a provider that addresses a custom entry by
 * a QUALIFIED handle. CodeBuddy takes `custom-local:<id>`, Qoder
 * `<provider>/<model>`, and Letta Code `provider/model`. A bare
 * `MOCK_MODELS` value selects no custom entry for these three.
 */
export const CODEBUDDY_MODEL_ID = `custom-local:${MOCK_MODELS.deepseek}`
export const CODEBUDDY_ALT_MODEL_WIRE_ID = 'leapmux-e2e-alt'
export const CODEBUDDY_ALT_MODEL_ID = `custom-local:${CODEBUDDY_ALT_MODEL_WIRE_ID}`
export const QODER_MODEL_ID = `mockprov/${MOCK_MODELS.deepseek}`
export const QODER_ALTERNATE_MODEL_ID = `mockprov/${MOCK_MODELS.qoder}`
export const LETTA_MODEL_ID = `openai-compatible/${MOCK_MODELS.letta}`
export const LETTA_VISION_MODEL_ID = 'openai/gpt-4o'
export const LETTA_REASONING_MODEL_ID = 'openai/gpt-5.4'

/**
 * Disable Grok requests outside scripted turns and prevent access to remote endpoints.
 *
 * Grok's sandbox in xai-grok-test-support/src/sandbox.rs uses the same telemetry, feedback, and update switches.
 * Each of these native tasks sends a model request after a turn:
 * - Turn summary.
 * - Title refresh.
 * - Session recap.
 * - Prompt suggestions.
 * - Memory processing.
 *
 * Those requests contain no authored test turn.
 * The first session title cannot be disabled. The housekeeping rules in ./mockModelScenario answer it.
 */
const GROK_QUIET_ENV: Readonly<Record<string, string>> = {
  GROK_DISABLE_AUTOUPDATER: '1',
  GROK_TELEMETRY_ENABLED: 'false',
  GROK_TELEMETRY_MIXPANEL_ENABLED: 'false',
  GROK_TELEMETRY_TRACE_UPLOAD: 'false',
  GROK_TRACE_UPLOAD: 'false',
  GROK_FEEDBACK_ENABLED: 'false',
  GROK_INSTRUMENTATION: 'disabled',
  OTEL_SDK_DISABLED: 'true',
  GROK_TURN_SUMMARY: '0',
  GROK_TITLE_REFRESH: '0',
  GROK_SESSION_RECAP: '0',
  GROK_PROMPT_SUGGESTIONS: '0',
  GROK_MEMORY: '0',
  GROK_TWO_PASS_COMPACTION: '0',
  // The credential-less warm-up `GET /` that Grok sends to a model origin.
  GROK_SAMPLER_SHARED_CLIENT: '0',
  GROK_MANAGED_CONFIG: '0',
  GROK_CAMPAIGNS: '0',
  // Grok otherwise runs `$SHELL -l` to capture a login environment, which reads
  // the developer's own shell profile.
  GROK_LOGIN_ENV: '0',
  // A fixed id, so an empty `GROK_HOME` does not compute one per process.
  GROK_AGENT_ID: 'leapmux-e2e',
}

/**
 * The switches that stop Qwen Code from calling the model outside a turn a test
 * scripts, from sending usage statistics, and from checking for and installing an
 * update. The same switches are in its `settings.json`; the environment is the
 * second guard, because Qwen reads it first.
 *
 * Qwen starts the update check from its interactive UI, never from `--acp`, so the
 * update switch covers a `qwen` that an agent's shell tool starts. Only the exact
 * value `true` counts. `general.enableAutoUpdate` in `settings.json` is the same
 * switch, and a worker cannot set it.
 */
const QWEN_QUIET_ENV: Readonly<Record<string, string>> = {
  QWEN_DISABLE_AUTO_TITLE: '1',
  QWEN_USAGE_STATISTICS_ENABLED: 'false',
  QWEN_TELEMETRY_ENABLED: 'false',
  QWEN_CODE_SKIP_UPDATE_CHECK_ONCE: 'true',
}

/**
 * Supply Kiro's isolated bearer key.
 *
 * Kiro validates only the local API key prefix, ksk_.
 * The mock refuses any other bearer, including credentials from stores that HOME cannot isolate, such as the macOS keychain.
 * The refusal stays visible in the request log.
 */
export const KIRO_E2E_API_KEY = 'ksk_leapmux_e2e'

/**
 * The switches that stop Kiro from calling a model or a host outside a turn a test
 * scripts. Kiro's engine reads each `KIRO_DISABLE_*` switch as the word `true`.
 *
 * - The session title is a model call of its own at the first prompt.
 * - The recap is a model call when a reader returns to a session.
 * - The experiment configuration is a request to Kiro's own host.
 * - Telemetry, the update check and the remote changelog are requests to hosts
 *   that no setting points at the mock.
 */
const KIRO_QUIET_ENV: Readonly<Record<string, string>> = {
  KIRO_DISABLE_SESSION_TITLE_LLM: 'true',
  KIRO_DISABLE_RECAP: 'true',
  KIRO_DISABLE_EXPERIMENT_CONFIG: 'true',
  KIRO_DISABLE_TELEMETRY: '1',
  KIRO_NO_AUTO_UPDATE: '1',
  KIRO_NO_REMOTE_CHANGELOG: '1',
}

/**
 * Direct Amp to its native service in ./ampSurface.
 *
 * The Surface controls Amp's actor loop and supplies each scripted inference.
 * Amp stores its login under ~/.local/share/amp in the private HOME, which the run creates empty.
 * Its only available credential is the fixed mock key.
 * A request that reaches the real service cannot authenticate with that key.
 */
function ampEnv(origin: string, homeDir: string): Record<string, string> {
  return {
    AMP_URL: origin,
    AMP_API_KEY: MODEL_KEY,
    // The actor gateway; the CLI would derive the same address from AMP_URL.
    RIVET_PUBLIC_ENDPOINT: `${origin}/actors`,
    // Empty, which Amp reads as unset, so a developer's own pool cannot reach it.
    RIVET_POOL: '',
    // Empty for the same reason. The worker hands Amp a settings file of its own,
    // built from the user settings under the isolated XDG_CONFIG_HOME.
    AMP_SETTINGS_FILE: '',
    AMP_SKIP_UPDATE_CHECK: '1',
    AMP_REMOTE_CONTROL_TERMINAL: '0',
    // Amp reads its settings, its history and its cache under these, and a value in
    // the developer's own environment would otherwise point it at theirs. Each one
    // is the directory the isolated HOME implies anyway, so no provider that
    // derives the same path from HOME sees a change.
    XDG_CONFIG_HOME: join(homeDir, '.config'),
    XDG_DATA_HOME: join(homeDir, '.local', 'share'),
    XDG_CACHE_HOME: join(homeDir, '.cache'),
    XDG_STATE_HOME: join(homeDir, '.local', 'state'),
  }
}

/**
 * Cline's provider in the isolated settings: the generic OpenAI Chat Completions
 * client, which takes any base URL and any model id. It reads `reasoning_content`
 * as thinking, which the mock sends.
 */
export const CLINE_PROVIDER_ID = 'deepseek'

/**
 * Configure private Cline settings and data.
 *
 * The Worker reads the same settings as Cline's CLI.
 * CLINE_DIR and CLINE_DATA_DIR select private directories under the isolated HOME.
 * They replace inherited user values that could select real configuration.
 *
 * - providers.json selects the mock as the last used provider in the native cline auth format.
 * - global-settings.json disables telemetry and the update (`autoUpdateEnabled`). No environment variable can replace Cline's compiled telemetry endpoint.
 * - An empty feature cache prevents a native remote flag request for one hour.
 *   The refusing proxy prevents a later remote fetch.
 *
 * Cline treats each empty path override as unset through process.env.X?.trim().
 * Each path then follows CLINE_DATA_DIR.
 * These overrides include every CLINE_*_DIR and CLINE_*_PATH in paths.ts.
 * They also include the hook directory and hook, log, capture, and approval files.
 * The Worker sets its agenda database and daemon discovery record after the shell profile.
 * Empty fixture values do not replace those Worker values.
 *
 * CLINE_PROVIDER and CLINE_MODEL stay unset.
 * Cline reads them through ??, so an empty string would select an empty value.
 * The Worker supplies the provider and model in session.create.
 */
function clineEnv(clineDir: string, clineDataDir: string): Record<string, string> {
  return {
    CLINE_DIR: clineDir,
    CLINE_DATA_DIR: clineDataDir,
    CLINE_PROVIDER_SETTINGS_PATH: '',
    CLINE_GLOBAL_SETTINGS_PATH: '',
    CLINE_DB_DATA_DIR: '',
    CLINE_SESSION_DATA_DIR: '',
    CLINE_TEAM_DATA_DIR: '',
    CLINE_MCP_SETTINGS_PATH: '',
    CLINE_CONNECTOR_DATA_DIR: '',
    CLINE_CONNECTOR_SETTINGS_PATH: '',
    CLINE_CONNECTORS_DB_PATH: '',
    // A developer's value would make each daemon poll a real automation database.
    CLINE_CRON_DB_PATH: '',
    CLINE_TASKS_DB_PATH: '',
    CLINE_HOOKS_DIR: '',
    CLINE_HOOKS_LOG_PATH: '',
    CLINE_LOG_PATH: '',
    CLINE_CAPTURE_DIR: '',
    CLINE_TOOL_APPROVAL_DIR: '',
    // The build environment decides the owner of the shared stores. An explicit
    // value wins over NODE_ENV, so a developer's `NODE_ENV=development` cannot move
    // a daemon to Cline's development stores. `production` is what a released
    // `cline` resolves by itself.
    CLINE_BUILD_ENV: 'production',
    // Cline's own account key. The mock provider takes its key from the settings,
    // and an empty key reaches no Cline service.
    CLINE_API_KEY: '',
    // Every `cline` that is not the daemon asks the npm registry at its start, and
    // starts a detached `npm update -g cline` as it exits when a newer release
    // exists. The worker sets it for the daemon as well. Only the exact value `1`
    // counts.
    CLINE_NO_AUTO_UPDATE: '1',
  }
}

/** Every pinned identifier, for the mock endpoint's catalog route. */
export const MOCK_MODEL_IDS: readonly string[] = [...new Set(Object.values(MOCK_MODELS))]

/** The custom-model handles Droid reads from its isolated settings. */
export const DROID_MOCK_MODEL_IDS = {
  primary: 'custom:Droid-0',
  alternate: 'custom:Droid-1',
} as const

export interface MockAgentEnvironment {
  env: Record<string, string>
  homeDir: string
  piAgentDir: string
  /** The agent directory of omp's profile, which holds its configuration and sessions. */
  ohMyPiAgentDir: string
}

interface MockAgentEnvironmentOptions {
  realHomeDir?: string
}

/** Write isolated agent configuration and return its process environment. */
export async function createMockAgentEnvironment(
  runDir: string,
  serverURL: string,
  options: MockAgentEnvironmentOptions = {},
): Promise<MockAgentEnvironment> {
  const origin = mockServerOrigin(serverURL)
  const temporaryDirectory = resolve(runDir, 'tmp')
  mkdirSync(temporaryDirectory, { recursive: true, mode: 0o700 })
  const temporaryEnv = { TMPDIR: temporaryDirectory, TEMP: temporaryDirectory, TMP: temporaryDirectory }
  const openAIBaseURL = `${origin}/v1`
  const homeDir = join(runDir, 'agent-home')
  const codexHome = join(homeDir, '.codex')
  const piAgentDir = join(homeDir, '.pi', 'agent')
  const reasonixHome = join(homeDir, '.reasonix')
  const gooseRoot = join(homeDir, '.goose')
  const zcodeDir = join(homeDir, '.zcode', 'v2')
  // Cursor's config directory is ISOLATED like every other provider's. It used
  // to be the real home's, because Cursor was the one provider that still
  // needed a live account's stored credentials. It answers to the mock now, so
  // a test neither reads nor writes the developer's own Cursor configuration.
  const cursorConfigDir = join(homeDir, '.cursor')
  const copilotHome = join(homeDir, '.copilot')
  const codewhaleHome = join(homeDir, '.codewhale')
  const grokHome = join(homeDir, '.grok')
  const qwenHome = join(homeDir, '.qwen')
  // Kiro keeps its settings here, and its sessions under `~/.kiro/sessions` of HOME.
  const kiroHome = join(homeDir, '.kiro')
  const kiroSettingsDir = join(kiroHome, 'settings')
  const kimiHome = join(homeDir, '.kimi-code')
  const ohMyPiAgentDir = join(homeDir, '.omp', 'profiles', OH_MY_PI_PROFILE, 'agent')
  const codebuddyHome = join(homeDir, '.codebuddy')
  const qoderHome = join(homeDir, '.qoder')
  const junieModelsDir = join(runDir, 'junie-models')
  const junieAgentsDir = join(runDir, 'junie-agents')
  const junieDataDir = join(runDir, 'junie-data')
  const diracDir = join(homeDir, '.dirac')
  const fastAgentHome = join(homeDir, '.fast-agent')
  const cliShimsDir = join(runDir, 'cli-shims')
  const clineDir = join(homeDir, '.cline')
  const factoryHome = join(homeDir, '.factory')
  const lettaHome = join(homeDir, '.letta')
  const lettaBackendDir = join(runDir, 'letta-backend')
  const lettaProvidersDir = join(lettaBackendDir, 'providers')
  const clineDataDir = join(clineDir, 'data')
  const clineSettingsDir = join(clineDataDir, 'settings')
  const clineCacheDir = join(clineDataDir, 'cache')
  // MiMo keeps its data, configuration, state and cache under one root. It must be
  // an absolute path, because MiMo refuses to start with a relative one.
  const mimoHome = join(runDir, 'mimocode-home')
  // A spec that asserts a private path needs the directory before an agent writes to it,
  // so the environment creates each directory that one of its variables points at.
  const providerStorageDirs = [join(runDir, 'zcode-storage'), join(runDir, 'kiro-data'), join(runDir, 'grok-lock-slots')]
  for (const directory of [...providerStorageDirs, codexHome, piAgentDir, reasonixHome, join(gooseRoot, 'config'), zcodeDir, copilotHome, cursorConfigDir, codewhaleHome, grokHome, qwenHome, kiroSettingsDir, kimiHome, ohMyPiAgentDir, mimoHome, clineSettingsDir, clineCacheDir, codebuddyHome, qoderHome, factoryHome, lettaHome, lettaBackendDir, lettaProvidersDir, junieModelsDir, junieAgentsDir, join(diracDir, 'data', 'state'), fastAgentHome, cliShimsDir])
    mkdirSync(directory, { recursive: true })

  // macOS zsh's system login profile rebuilds PATH. Reapply the private shims
  // after that profile, so Junie's `security` probe cannot reach the keychain.
  const privatePath = `export PATH=${posixQuote(cliShimsDir)}:"$PATH"\n`
  for (const file of ['.zshrc', '.zlogin'])
    writeFileSync(join(homeDir, file), privatePath, { mode: 0o600 })

  const codexMcpFormServer = writeMcpFormServer(codexHome, 'form-server.mjs')
  const mimoMcpConfirmationServer = writeMcpConfirmationServer(mimoHome)
  const mcpEchoServer = writeMcpEchoServer(runDir)
  const mcpServers = [{ name: 'echo_probe', command: process.execPath, args: [mcpEchoServer] }]
  writeFileSync(join(codexHome, 'config.toml'), codexConfig(openAIBaseURL, codexMcpFormServer), { mode: 0o600 })
  writeJSON(join(piAgentDir, 'models.json'), piModels(openAIBaseURL))
  writeJSON(join(piAgentDir, 'settings.json'), {
    defaultProvider: PI_PROVIDER_ID,
    defaultModel: MOCK_MODELS.pi,
    compaction: { keepRecentTokens: 32 },
    packages: piPackagePaths(options.realHomeDir),
  })
  writeJSON(join(piAgentDir, 'mcp.json'), {
    mcpServers: { echo_probe: { command: process.execPath, args: [mcpEchoServer], exposure: 'direct' } },
  })
  // YAML is omp's format, and JSON is valid YAML, so the one writer serves.
  writeJSON(join(ohMyPiAgentDir, 'models.yml'), ohMyPiModels(openAIBaseURL))
  writeJSON(join(ohMyPiAgentDir, 'config.yml'), ohMyPiConfig())
  writeJSON(join(ohMyPiAgentDir, 'mcp.json'), {
    mcpServers: { echo_probe: { type: 'stdio', command: process.execPath, args: [mcpEchoServer] } },
  })
  writeFileSync(join(reasonixHome, 'config.toml'), reasonixConfig(openAIBaseURL), { mode: 0o600 })
  writeFileSync(join(reasonixHome, '.env'), reasonixCredentials(), { mode: 0o600 })
  const gooseMcpFormServer = writeMcpFormServer(gooseRoot, 'form-server.mjs')
  writeFileSync(join(gooseRoot, 'config', 'config.yaml'), gooseConfig(gooseMcpFormServer), { mode: 0o600 })
  writeFileSync(join(codewhaleHome, 'config.toml'), codewhaleConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(codewhaleHome, 'mcp.json'), { servers: { echo_probe: { command: process.execPath, args: [mcpEchoServer] } } })
  mkdirSync(join(codewhaleHome, 'catalog'), { recursive: true })
  writeJSON(join(codewhaleHome, 'catalog', 'provider-catalogs.json'), codewhaleCatalog(openAIBaseURL))
  writeFileSync(join(grokHome, 'config.toml'), grokConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(qwenHome, 'settings.json'), qwenSettings(openAIBaseURL, mcpEchoServer))
  writeJSON(join(codebuddyHome, 'models.json'), codebuddyModels(openAIBaseURL))
  writeJSON(join(codebuddyHome, 'settings.json'), codebuddySettings())
  const qoderMcpFormServer = writeMcpFormServer(qoderHome, 'form-server.mjs')
  writeJSON(join(qoderHome, 'settings.json'), qoderSettings(openAIBaseURL, qoderMcpFormServer))
  qoderEndpointCaches(qoderHome, origin)
  writeJSON(join(kiroSettingsDir, 'cli.json'), kiroSettings(origin))
  const zcodeConfigPath = join(zcodeDir, 'config.json')
  const zcodePersonalConfigPath = join(zcodeDir, 'provider_config.json')
  writeJSON(zcodeConfigPath, zcodeLegacyConfig(openAIBaseURL))
  writeJSON(zcodePersonalConfigPath, zcodePersonalConfig(openAIBaseURL))
  writeFileSync(join(kimiHome, 'config.toml'), kimiConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(kimiHome, 'mcp.json'), {
    mcpServers: { echo_probe: { command: process.execPath, args: [mcpEchoServer] } },
  })
  const clineWrittenAt = Date.now()
  writeJSON(join(clineSettingsDir, 'providers.json'), clineProviders(openAIBaseURL, clineWrittenAt))
  writeJSON(join(clineSettingsDir, 'global-settings.json'), { telemetryOptOut: true, autoUpdateEnabled: false })
  writeJSON(join(clineSettingsDir, 'cline_mcp_settings.json'), {
    mcpServers: { echo_probe: { transport: { type: 'stdio', command: process.execPath, args: [mcpEchoServer] } } },
  })
  writeJSON(join(clineCacheDir, 'feature-flags.json'), clineFeatureFlags(clineWrittenAt))
  writeJSON(join(factoryHome, 'settings.json'), droidSettings(openAIBaseURL))
  writeJSON(join(lettaBackendDir, 'providers', 'auth.json'), lettaAuth(openAIBaseURL))
  // `letta backend local` writes this. Without it the App Server creates agents
  // against the cloud API and runtime_start fails 401.
  writeJSON(join(lettaHome, 'settings.json'), { preferredBackendMode: 'local' })
  writeJSON(join(junieModelsDir, 'mock-model.json'), junieModelProfile(`${origin}/v1/chat/completions`))
  writeJSON(join(junieModelsDir, 'mock-responses.json'), junieModelProfile(`${origin}/v1/responses`, 'OpenAIResponses'))
  writeFileSync(join(junieAgentsDir, 'leapmux-e2e-child.md'), junieTestSubagent(), { mode: 0o600 })
  writeFileSync(join(diracDir, 'data', 'globalState.json'), JSON.stringify({ telemetrySetting: 'disabled', autoApproveAllToggled: true, yoloModeToggled: true }), { mode: 0o600 })
  writeFileSync(join(fastAgentHome, 'fast-agent.yaml'), fastAgentConfig(openAIBaseURL), { mode: 0o600 })
  await prepareLettaBackend(lettaHome, lettaBackendDir, openAIBaseURL, temporaryEnv)

  // OpenCode otherwise retains every short seed turn when it compacts. Keep
  // two recent turns so the browser can prove that the first one left context.
  const openCodeConfig = JSON.stringify({
    ...openCodeFamilyConfig(openAIBaseURL, mcpEchoServer),
    compaction: { tail_turns: 2 },
  })
  const searchPathEnv = agentSearchPathEnv()
  return {
    homeDir,
    piAgentDir,
    ohMyPiAgentDir,
    env: {
      HOME: homeDir,
      USERPROFILE: homeDir,
      ...temporaryEnv,
      // A user's ZDOTDIR can make the login shell load real startup files.
      // Those files can replace PATH and expose the system keychain to Junie.
      ZDOTDIR: homeDir,
      // The developer's PATH, with the real install directory of each mise tool
      // before mise's shims, which cannot start a tool under the isolated HOME
      // above. See `agentSearchPath`.
      ...searchPathEnv,
      LEAPMUX_E2E_MODEL_API_KEY: MODEL_KEY,
      NO_PROXY: LOOPBACK_NO_PROXY,
      no_proxy: LOOPBACK_NO_PROXY,
      // The mock is also the HTTPS proxy of the `leapmux dev` process, and so of
      // the hub, the worker, the terminal shells and every agent. It refuses each
      // tunnel, so a request to a real host fails at once instead of leaving the
      // machine. Kiro needs this: its engine opens remote sessions on Kiro's own
      // host, and no setting moves that request.
      //
      // There is NO plain-HTTP proxy, and none may be added. The mock's own origin
      // is plain HTTP, and Cursor's HTTP/2 pool reads HTTP_PROXY for an `http:` URL
      // and never reads NO_PROXY, so a plain-HTTP proxy carries even Cursor's calls
      // to the mock into the refusal, and every Cursor turn fails. Every real host
      // is HTTPS, so the HTTPS proxy alone keeps each one closed.
      HTTPS_PROXY: origin,
      https_proxy: origin,

      ANTHROPIC_API_KEY: MODEL_KEY,
      ANTHROPIC_BASE_URL: origin,
      CLAUDE_CONFIG_DIR: join(homeDir, '.claude'),
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
      DISABLE_TELEMETRY: '1',

      CODEX_HOME: codexHome,
      OPENAI_API_KEY: MODEL_KEY,
      OPENAI_BASE_URL: openAIBaseURL,

      OPENCODE_CONFIG_CONTENT: openCodeConfig,
      OPENCODE_DISABLE_PROJECT_CONFIG: 'true',
      KILO_CONFIG_CONTENT: openCodeConfig,
      KILO_DISABLE_PROJECT_CONFIG: 'true',
      // Kilo sends PostHog telemetry by default. When this variable is set, Kilo
      // sends it only for the value `all`. Kilo 7.8 reads the variable once at
      // start, and the variable outranks the configuration.
      KILO_TELEMETRY_LEVEL: 'off',
      // OpenCode and Kilo upgrade their own install from the check that their TUI
      // starts, never from `acp`. The `autoupdate` key of the inline configuration
      // above cannot carry the switch: the check reads only the global config files,
      // which the isolated HOME and XDG directories leave empty. Each of the two
      // reads `true` and `1`.
      OPENCODE_DISABLE_AUTOUPDATE: 'true',
      KILO_DISABLE_AUTOUPDATE: 'true',

      GOOSE_PROVIDER: 'openai',
      GOOSE_MODEL: MOCK_MODELS.goose,
      GOOSE_PATH_ROOT: gooseRoot,

      REASONIX_HOME: reasonixHome,

      PI_CODING_AGENT_DIR: piAgentDir,
      PI_OFFLINE: '1',
      PI_SKIP_VERSION_CHECK: '1',
      // EMPTY, which Pi, omp and the worker's session readers all read as unset.
      // omp reads this variable whatever its profile, so a directory here would put
      // omp's sessions beside Pi's, and each session picker would list the other's.
      // Pi keeps its sessions under its agent directory instead, the path the
      // worker's Pi reader resolves from PI_CODING_AGENT_DIR. Empty rather than
      // absent, because a developer's own value would otherwise reach both agents.
      PI_CODING_AGENT_SESSION_DIR: '',

      OMP_PROFILE: OH_MY_PI_PROFILE,
      // omp routes every request that is not to a loopback or private address
      // through its own proxy variable, and the model endpoint is loopback. A
      // request to anywhere else -- a model catalog of a provider that a later omp
      // adds, which `disabledProviders` below does not list -- then reaches the
      // refusing mock, which records the host. No other provider reads the
      // variable.
      PI_PROXY: origin,
      // omp's own switches for the requests no test scripts: the session title, the
      // auto-QA prompt, OpenTelemetry export, desktop notifications, the language
      // server multiplexer, and the first-run setup.
      PI_NO_TITLE: '1',
      PI_AUTO_QA: '0',
      OTEL_SDK_DISABLED: 'true',
      PI_NOTIFICATIONS: 'off',
      PI_DISABLE_LSPMUX: '1',
      OMP_SKIP_SETUP: '1',
      // EMPTY, which omp reads as unset, so a developer's own value cannot reach
      // the E2E omp. PI_CONFIG_FILES adds overlay configurations that win over the
      // profile's config.yml. PI_CONFIG_DIR moves omp's store away from ~/.omp,
      // which loses disabledProviders. The broker pair fetches credentials from a
      // remote broker.
      PI_CONFIG_FILES: '',
      PI_CONFIG_DIR: '',
      OMP_AUTH_BROKER_URL: '',
      OMP_AUTH_BROKER_TOKEN: '',

      CURSOR_CONFIG_DIR: cursorConfigDir,
      // Cursor talks to its OWN backend, not a model API, so this points at the
      // mock's Cursor surface rather than at a model route. See
      // `./cursorSurface` for the three facts that shape it -- above all that
      // the startup calls answer all-defaults, because a REAL answer makes the
      // agent use its built-in endpoint and the turn never arrives here.
      CURSOR_API_ENDPOINT: origin,
      // The ACP entrypoint refuses `session/new` without a token, which
      // `cursor-agent --print` never asked for. The value is not checked
      // against anything -- the mock reads no credential -- but its ABSENCE is,
      // and it is refused locally, before any request reaches the endpoint.
      CURSOR_AUTH_TOKEN: MODEL_KEY,
      // Keep the CLI out of the developer's macOS keychain. Its default
      // credential store is the keychain, and a fresh `CURSOR_CONFIG_DIR` has
      // nothing to read, so the CLI asks for one and macOS raises a MODAL
      // dialog ("A keychain cannot be found to store cursor-user"). Nothing
      // answers it on a test machine, so the agent starts, blocks, and never
      // opens its turn stream -- which reads as a provider that hangs.
      // `memory` writes nothing anywhere: CURSOR_AUTH_TOKEN supplies the
      // credential on every spawn, so a test has nothing worth persisting, and
      // no stale credential can outlive the run that made it.
      AGENT_CLI_CREDENTIAL_STORE: 'memory',

      COPILOT_API_URL: origin,
      COPILOT_DEBUG_GITHUB_API_URL: origin,
      COPILOT_GITHUB_TOKEN: MOCK_COPILOT_GITHUB_TOKEN,
      COPILOT_HOME: copilotHome,
      GITHUB_COPILOT_API_TOKEN: MODEL_KEY,
      // Copilot CLI starts its updater one second after every start, `--server
      // --stdio` included. The updater downloads the newest package, and then the
      // release executable, and renames that over the executable that runs, which is
      // the developer's own install: an isolated HOME does not move it. The refusing
      // proxy above only fails the download, and it ends when a run starts without
      // it. Only the exact value `false` counts; `0` and `off` do not.
      COPILOT_AUTO_UPDATE: 'false',

      GROK_HOME: grokHome,
      // Grok's leader lock keeps acquire slots under the system temporary
      // directory; this keeps them inside the run.
      GROK_FILE_LOCK_SLOT_DIR: join(runDir, 'grok-lock-slots'),
      ...GROK_QUIET_ENV,

      QWEN_HOME: qwenHome,
      ...QWEN_QUIET_ENV,

      // Kiro reads its settings, and so its endpoints, from KIRO_HOME, which a
      // developer's own value would otherwise point at their real configuration.
      KIRO_HOME: kiroHome,
      KIRO_DATA_DIR: join(runDir, 'kiro-data'),
      KIRO_API_KEY: KIRO_E2E_API_KEY,
      KIRO_REMOTE_SESSIONS_ENDPOINT: origin,
      CLOUD_CONFIG_ENDPOINT: origin,
      ...KIRO_QUIET_ENV,
      // The AWS SDKs of Kiro read a profile and a credential from these, and the
      // developer's own values would otherwise pass through to the agent. The files
      // are the defaults under the isolated HOME, which hold nothing, and the
      // profile is the default one of those files. The SDKs read an empty key as
      // no key.
      AWS_PROFILE: 'default',
      AWS_CONFIG_FILE: join(homeDir, '.aws', 'config'),
      AWS_SHARED_CREDENTIALS_FILE: join(homeDir, '.aws', 'credentials'),
      AWS_ACCESS_KEY_ID: '',
      AWS_SECRET_ACCESS_KEY: '',
      AWS_SESSION_TOKEN: '',

      ZCODE_MODEL_TELEMETRY_ENABLED: 'false',
      ZCODE_PERSONAL_PROVIDER_CONFIG_FILE: zcodePersonalConfigPath,
      ZCODE_STORAGE_DIR: join(runDir, 'zcode-storage'),

      // Each of the three Codewhale switches below has a twin in its
      // configuration file. The variables outrank the file, so a default that a
      // later version adds to the file cannot turn one of them back on.
      CODEWHALE_HOME: codewhaleHome,
      CODEWHALE_TELEMETRY: '0',
      CODEWHALE_NO_UPDATE_CHECK: '1',
      CODEWHALE_ALLOW_SHELL: '1',

      // Kimi Code keeps its configuration, sessions, and server token here. The
      // two switches stop the telemetry upload and the update. `kimi web` makes no
      // update check, so the update switch guards the swap of a staged native
      // update, which `kimi --version` and `kimi web` both run; the TUI also checks
      // for a release and installs it. Kimi downloads `rg` from its host when `rg` is
      // absent from PATH, and no switch stops that, so the run keeps the developer's
      // PATH.
      KIMI_CODE_HOME: kimiHome,
      KIMI_DISABLE_TELEMETRY: '1',
      KIMI_CODE_NO_AUTO_UPDATE: '1',

      // MiMo reads no OPENCODE_* variable, so it takes the same inline provider
      // under its own names. Every switch below stops a request that no test
      // scripts, or a read of the developer's own configuration.
      MIMOCODE_HOME: mimoHome,
      MIMOCODE_CONFIG_CONTENT: JSON.stringify(mimoCodeConfig(openAIBaseURL, mimoMcpConfirmationServer, mcpEchoServer)),
      MIMOCODE_DISABLE_PROJECT_CONFIG: 'true',
      // The worker pins this one too. It is here so that the configuration states
      // every tool that the specs script.
      MIMOCODE_ENABLE_QUESTION_TOOL: '1',
      // Analytics is ON unless this is `false`, and it posts to Xiaomi's tracker.
      MIMOCODE_ENABLE_ANALYSIS: 'false',
      // Without this, each start fetches the public model catalog from models.dev.
      MIMOCODE_DISABLE_MODELS_FETCH: 'true',
      MIMOCODE_DISABLE_AUTOUPDATE: 'true',
      // The cron scheduler starts turns of its own, and no test scripts them.
      MIMOCODE_EXPERIMENTAL_CRON: 'false',
      // The checkpoint writer is a hidden subagent that calls the model at 40, 60
      // and 80 percent of the context window.
      MIMOCODE_DISABLE_CHECKPOINT: 'true',
      // No CLAUDE.md, Claude Code command or Claude Code MCP server of the
      // developer's reaches the agent.
      MIMOCODE_DISABLE_CLAUDE_CODE: 'true',
      // MiMo reads OPENAI_API_KEY, ANTHROPIC_API_KEY and the rest into providers
      // of its own. The inline provider is the only one a test may reach.
      MIMOCODE_DISABLE_PROVIDER_ENV: 'true',
      MIMOCODE_DISABLE_BUILTIN_SKILLS: 'true',
      MIMOCODE_DISABLE_COMPOSE_SKILLS: 'true',
      MIMOCODE_DISABLE_AGENTS_SKILLS: 'true',
      MIMOCODE_DISABLE_LSP_DOWNLOAD: 'true',
      // The workflow tool is experimental in MiMo 0.1.15 and off by default. A spec
      // runs a workflow through it (`mimoWorkflowToolCall`).
      MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL: 'true',

      ...ampEnv(origin, homeDir),

      ...clineEnv(clineDir, clineDataDir),

      ...droidEnv(homeDir, openAIBaseURL),

      ...lettaEnv(lettaHome, lettaBackendDir),
      ...codebuddyEnv(codebuddyHome),
      ...qoderEnv(qoderHome, cliShimsDir),
      QODER_CONFIG_SERVICE_URL: origin,
      QODER_SERVER_ENDPOINT: '',
      ...junieEnv(homeDir, junieModelsDir, junieAgentsDir, junieDataDir, origin, options.realHomeDir),
      DIRAC_PROVIDER: 'openai',
      DIRAC_BASE_URL: openAIBaseURL,
      DIRAC_API_KEY: MODEL_KEY,
      DIRAC_MODEL: MOCK_MODELS.deepseek,
      DIRAC_DIR: diracDir,
      // Dirac starts a detached update of its own install from its startup path,
      // `--acp` included. Only the exact value `1` counts. The worker pins it too.
      DIRAC_NO_AUTO_UPDATE: '1',
      FAST_AGENT_HOME: fastAgentHome,
      ...createCommandCodeEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, modelID: COMMAND_CODE_MODEL_ID, alternateModelID: COMMAND_CODE_ALT_MODEL_ID, mcpServers }),
      ...createDeepseekHarnessEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, mcpServers }),
      ...createGeminiEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, modelID: GEMINI_MODEL_ID, mcpServers }),
      ...credentialStoreShimEnv(cliShimsDir, searchPathEnv.PATH ?? process.env.PATH),
    },
  }
}

/**
 * Isolate Factory Droid's files and service requests.
 *
 * `FACTORY_HOME_OVERRIDE` identifies the directory that contains `.factory`.
 * Droid reads settings from `<override>/.factory/settings.json`. It keeps its
 * sessions, logs, and telemetry there. The custom model points at the mock.
 * The CLI checks its isolated API key at the mock's whoami route. The proxy
 * rejects requests to other hosts.
 */
function droidEnv(homeDir: string, baseURL: string): Record<string, string> {
  return {
    FACTORY_HOME_OVERRIDE: homeDir,
    FACTORY_API_BASE_URL: baseURL,
    FACTORY_API_KEY: MODEL_KEY,
    FACTORY_DROID_AUTO_UPDATE_ENABLED: '0',
    // An unroutable sink keeps telemetry off the network.
    FACTORY_TELEMETRY_INGEST_BASE_URL: 'http://127.0.0.1:9',
    FACTORY_OTEL_ENABLED: '0',
    // Keep the built-in model catalog so the effort test can select a real
    // native ladder. The model endpoint stays on the mock, and the proxy
    // refuses every request to a real host.
    FACTORY_AIRGAP_ENABLED: '0',
    FACTORY_DISABLE_DYNAMIC_CONFIG: '1',
    FACTORY_DISABLE_KEYRING: '1',
  }
}

/** Factory Droid's BYOK settings, which point the model at the mock. */
function droidSettings(baseURL: string): Record<string, unknown> {
  const primary = {
    model: MOCK_MODELS.droid,
    id: DROID_MOCK_MODEL_IDS.primary,
    index: 0,
    baseUrl: baseURL,
    apiKey: MODEL_KEY,
    displayName: 'Mock Model',
    maxOutputTokens: 8192,
    noImageSupport: false,
    reasoningEffort: 'high',
    provider: 'generic-chat-completion-api',
  }
  return {
    customModels: [
      primary,
      { ...primary, model: MOCK_MODELS.droidAlt, id: DROID_MOCK_MODEL_IDS.alternate, index: 1, displayName: 'Alternate Mock Model' },
    ],
    sessionDefaultSettings: {
      model: DROID_MOCK_MODEL_IDS.primary,
      reasoningEffort: 'none',
      autonomyMode: 'normal',
    },
  }
}

/**
 * Letta Code's isolated configuration.
 *
 * `LETTA_LOCAL_BACKEND_DIR` moves the flat-file store, `LETTA_HOME` the agent
 * settings and transcripts. The PATH must contain a real node plus the `letta`
 * bin: subagents re-exec `letta`, and a mise shim fails under an isolated HOME.
 */
function lettaEnv(lettaHome: string, lettaBackendDir: string): Record<string, string> {
  return {
    LETTA_HOME: lettaHome,
    LETTA_LOCAL_BACKEND_DIR: lettaBackendDir,
    // The App Server refuses to start a runtime without an API key in the
    // environment, even when the local backend has a provider record.
    LETTA_API_KEY: MODEL_KEY,
    LETTA_CODE_TELEM: '0',
    DO_NOT_TRACK: '1',
    LETTA_CODE_OFFLINE: '1',
    LETTA_DISABLE_MODS: '1',
    // Letta Code runs `npm install -g @letta-ai/letta-code` from its startup path,
    // which replaces the operator's global install. `letta server` and the other
    // subcommands exit before that path, but each subagent is a `letta` child that
    // takes it, and the child inherits this environment. Only the exact value `1`
    // counts. The worker pins it too.
    DISABLE_AUTOUPDATER: '1',
  }
}

/** Letta Code's provider credential record, which points the model at the mock. */
function lettaAuth(baseURL: string): Record<string, unknown> {
  return {
    version: 1,
    providers: {
      'openai-compatible': {
        auth: { type: 'api', key: MODEL_KEY },
        base_url: baseURL,
      },
      'openai': {
        id: 'local-provider-openai',
        name: 'openai',
        provider_type: 'openai',
        provider_category: 'byok',
        auth: { type: 'api', key: MODEL_KEY },
        base_url: baseURL,
      },
    },
  }
}

/**
 * Configure CodeBuddy's native custom-local model catalog.
 *
 * CodeBuddy reads models.json from CODEBUDDY_CONFIG_DIR and selects entries through the custom-local: prefix.
 * Each URL must end in /chat/completions.
 * CodeBuddy always sends stream: true and requires Server-Sent Events (SSE).
 * A plain JSON completion causes error_during_execution.
 */
function codebuddyModels(baseURL: string): Record<string, unknown> {
  const primary = {
    id: MOCK_MODELS.deepseek,
    name: 'Mock Model',
    vendor: 'Mock',
    apiKey: MODEL_KEY,
    maxInputTokens: 128_000,
    maxOutputTokens: 4096,
    url: `${baseURL}/chat/completions`,
    temperature: 0,
    supportsToolCall: true,
    // CodeBuddy drops image and document blocks before the model request
    // when this catalog entry declares text-only input.
    supportsImages: true,
  }
  return {
    models: [primary, { ...primary, id: CODEBUDDY_ALT_MODEL_WIRE_ID, name: 'Alternate Mock Model' }],
    availableModels: [MOCK_MODELS.deepseek, CODEBUDDY_ALT_MODEL_WIRE_ID],
  }
}

/** The model CodeBuddy starts on, as the `custom-local:` prefix selects it. */
function codebuddySettings(): Record<string, unknown> {
  return { model: CODEBUDDY_MODEL_ID }
}

/**
 * Configure CodeBuddy's private environment.
 *
 * configDir contains models.json and settings.json.
 * CODEBUDDY_CONFIG_DIR and a private HOME isolate that configuration.
 * The switches disable these unscripted startup requests:
 * - Telemetry and Galileo collection.
 * - Trace collection.
 *
 * DISABLE_AUTOUPDATER also turns off CodeBuddy's updater. The `-p` mode that the
 * worker starts never runs it, but a daemon (CODEBUDDY_SESSION_KIND=daemon) does.
 * Claude Code and Letta Code read the same name.
 */
function codebuddyEnv(configDir: string): Record<string, string> {
  return {
    CODEBUDDY_CONFIG_DIR: configDir,
    DISABLE_TELEMETRY: '1',
    DISABLE_GALILEO: '1',
    DISABLE_AUTOUPDATER: '1',
    CODEBUDDY_DISABLE_TRACE_COLLECTOR: '1',
  }
}

/**
 * Configure Qoder's private custom provider.
 *
 * Qoder reads settings.json from --config-dir and selects each model as <provider>/<model>.
 * One providers entry registers the model.
 * A modelConfigs.customModels entry for the same key registers it twice.
 * Qoder then rejects the provider with "model key ... conflicts with an existing catalog model".
 * The next model call would reach the real Qoder API, so this fixture uses providers alone.
 */
function qoderSettings(baseURL: string, formServer: string): Record<string, unknown> {
  return {
    mcpServers: {
      form_probe: { command: process.execPath, args: [formServer] },
    },
    providers: {
      mockprov: {
        type: 'openai-compatible',
        protocol: 'openai',
        authType: 'bearer',
        // The schema spells the key `baseUrl` (camelCase, not `baseURL`).
        baseUrl: baseURL,
        apiKey: MODEL_KEY,
        displayName: 'Mock Provider',
        models: [
          { model: MOCK_MODELS.deepseek, displayName: 'Mock Model', capabilities: { vision: true } },
          { model: MOCK_MODELS.qoder, displayName: 'Alternate Mock Model', capabilities: { vision: true } },
        ],
      },
    },
  }
}

/**
 * Configure isolated Qoder authentication and endpoint discovery.
 *
 * The native headless mode requires an authenticated account before stream-json starts.
 * QODER_SDK_AUTH_PAYLOAD_FILE supplies a fake access token through initFromAccessToken.
 * QODER_AGENT_SDK_ENTRYPOINT selects this software development kit (SDK) path.
 * The CLI reads the file once, so the private launcher writes it before each start.
 *
 * qoderEndpointCaches supplies each elected endpoint through both native cache formats.
 * Authentication calls reach handleQoderHttp in ./qoderSurface. Model calls reach ./mockModelServer.
 * A modelConfigs.customModels entry beside providers causes a native catalog conflict.
 * The fixture therefore supplies one provider registration.
 *
 * The Worker passes --config-dir for private configuration.
 * These settings select GLOBAL, omit user rc files, use file credentials, and disable Alibaba HTTPDNS.
 */
function qoderEnv(runDir: string, shimsDir: string): Record<string, string> {
  // The CLI reads this SDK payload once at startup.
  // It contains the known mock key, and the private endpoints never reach a real account.
  const authPayloadPath = join(runDir, 'qoder-sdk-auth.json')
  const authPayload = { type: 'accessToken', accessToken: MODEL_KEY }
  writeJSON(authPayloadPath, authPayload)
  const installedQoder = findBinary('qodercli')
  if (installedQoder !== null && process.platform !== 'win32') {
    writeFileSync(join(shimsDir, 'qodercli'), `#!/bin/sh
set -eu
if [ -z "$QODER_SDK_AUTH_PAYLOAD_FILE" ]; then
  exit 1
fi
printf '%s\\n' ${posixQuote(JSON.stringify(authPayload))} > "$QODER_SDK_AUTH_PAYLOAD_FILE"
exec ${posixQuote(installedQoder)} "$@"
`, { mode: 0o755 })
  }
  return {
    QODER_SITE: 'GLOBAL',
    // Select prod without a region suffix. The native format is "<env>-<region>".
    // A region other than auto elects only securityInference.
    // The openapi token exchange would then reach the real openapi.qoder.sh service.
    QODER_ENV: 'prod',
    QODER_NO_RC: '1',
    QODER_FORCE_FILE_STORAGE: '1',
    QODER_HTTPDNS: '0',
    // The mocked-auth recipe. See the block comment above.
    QODER_AGENT_SDK_ENTRYPOINT: '1',
    QODER_SDK_AUTH_PAYLOAD_FILE: authPayloadPath,
    // The SDK requires this switch to register the custom provider.
    // Without it, isCustomProviderEntryEnabled() returns false and the model call reaches the real Qoder API.
    QODER_SDK_CUSTOM_BASE_URL_BYOK: '1',
    // Qoder treats an empty token as unset. This prevents the user's token from reaching the test.
    // The mock serves authentication with its own fixed credential.
    QODER_PERSONAL_ACCESS_TOKEN: '',
    QODER_SESSION_ID: '',
    QODER_CLI: '',
    QODERCN_CLI: '',
    QODER_REMOTE_CHILD: '',
    // Qoder's own updater runs only in its interactive UI. A different download
    // runs in `-p` mode: the Qoder Security plugin loads unless each of its four
    // checks is explicitly false, and then downloads `qodersec` and a pinned
    // `qodercli` under $HOME. SDK mode, which this fixture selects, defaults the
    // four checks to off; this value does not depend on that default. Each key must
    // be a literal false, and the value must be valid JSON, or Qoder ignores it.
    QODER_SECURITY_SCAN_SETTINGS_JSON: JSON.stringify({ l1StaticCheck: false, l2LightweightScan: false, l3DeepScan: false, gitPushScanHook: false }),
    QODERSEC_SKIP_ASYNC_UPDATE: '1',
  }
}

/**
 * Set every elected Qoder endpoint to the mock through its native V1 and V2 caches.
 *
 * A cold native cache selects real *.qoder.sh endpoints for authentication and model calls.
 * The auth path requires V1. Its absence causes access_token_invalid before token exchange.
 * Discovery refresh writes V2. Both formats expire after 24 hours.
 * The fixture writes a fresh updatedAt for each run.
 */
function qoderEndpointCaches(qoderHome: string, origin: string): void {
  const cacheDir = join(qoderHome, '.cache')
  mkdirSync(cacheDir, { recursive: true })
  const now = Date.now()
  const { v1, v2 } = qoderEndpointCacheRecords(origin, now)
  writeJSON(join(cacheDir, 'qoder-client-endpoint-cache.json'), v2)
  writeJSON(join(cacheDir, 'qoder-client-endpoint-cache-public.json'), v2)
  writeJSON(join(cacheDir, 'endpoint-cache.json'), v1)
}

/**
 * Build a Junie custom model profile in an explicit model location.
 *
 * --model-location or model-locations selects the folder that contains each JSON profile.
 * The file basename selects the profile: mock-model.json gives custom:mock-model.
 * id supplies the model identifier in the request. baseUrl supplies the complete endpoint for either API type.
 */
function junieModelProfile(fullEndpoint: string, apiType: 'OpenAICompletion' | 'OpenAIResponses' = 'OpenAICompletion'): Record<string, unknown> {
  return {
    id: MOCK_MODELS.junie,
    displayName: apiType === 'OpenAIResponses' ? 'Mock Responses Model' : 'Mock Model',
    providerName: 'Mock',
    baseUrl: fullEndpoint,
    apiKey: MODEL_KEY,
    apiType,
    maxContextLength: 200000,
  }
}

/** A child with a file-read turn before its final answer. */
function junieTestSubagent(): string {
  return `---
name: leapmux-e2e-child
description: Read a local marker file and report its contents in an isolated test.
model: ${JUNIE_MOCK_MODEL}
---

You are the LeapMux test subagent.
Read the file in the task with open_entire_file.
Then call submit with the marker you read.
`
}

/**
 * Configure Junie's private store and explicit model and child locations.
 *
 * JUNIE_HOME contains private sessions and secrets under the isolated HOME.
 * Junie cannot read or write the user's store through that directory.
 *
 * JUNIE_DATA selects the installed programs in versions/ or current/.
 * The private HOME has no program installation, so the fixture selects the user's installed binaries.
 * Those program directories contain no session state.
 * See {@link junieDataDirectory} for why the directory is a private one that links them.
 *
 * JUNIE_SKIP_UPDATE_CHECK turns off the update check and download of the CLI itself.
 * It does not stop the launcher, which applies a staged update before the CLI starts.
 *
 * JUNIE_CONFIG_LOCATION selects the explicit configuration file for these locations:
 * - Mock model profiles.
 * - Custom child profiles.
 * - Private proxy endpoint.
 *
 * The Worker passes --model-default-locations=false and --agent-default-location=false.
 * Junie therefore does not scan default model or child directories, including project and JUNIE_HOME model folders.
 * The explicit fixture locations remain enabled under both flags.
 */
function junieEnv(homeDir: string, modelsDir: string, agentsDir: string, dataDir: string, mockOrigin: string, realHomeDir: string | undefined): Record<string, string> {
  const junieHome = join(homeDir, '.junie')
  mkdirSync(junieHome, { recursive: true })
  const configPath = join(modelsDir, 'config.json')
  writeJSON(configPath, {
    'model-locations': [modelsDir],
    'agent-locations': [agentsDir],
    'provider': JUNIE_PROXY_PROVIDER,
    'proxies': [{
      'name': JUNIE_PROXY_PROVIDER,
      'kind': 'OpenAI',
      'api-url': mockOrigin,
      'headers': [`Authorization: Bearer ${MODEL_KEY}`],
    }],
  })
  const env: Record<string, string> = {
    JUNIE_HOME: junieHome,
    JUNIE_CONFIG_LOCATION: configPath,
    JUNIE_SKIP_UPDATE_CHECK: '1',
  }
  const installRoot = realHomeDir === undefined ? undefined : join(realHomeDir, '.local', 'share', 'junie')
  if (installRoot !== undefined)
    env.JUNIE_DATA = junieDataDirectory(installRoot, dataDir)
  return env
}

/**
 * The data directory that the Junie launcher runs against, or the install root when
 * the root has no layout that a private directory can link.
 *
 * The launcher applies `updates/pending-update.json` before it starts the CLI: it
 * swaps `versions/<version>` and flips the `current` link. An interactive Junie stages
 * that file on its own, so a run that shares the operator's data directory applies the
 * operator's update in the middle of a test run, and `--skip-update-check` does not
 * stop it.
 *
 * The private directory links each installed version, points its own `current` link at
 * the version that the install runs, and holds an empty `updates/`. The launcher then
 * finds nothing to apply, and a swap that it makes would move only a link in the private
 * directory. The versions stay real, so no program is copied.
 */
function junieDataDirectory(installRoot: string, dataDir: string): string {
  // The launcher is a bash script, and a link to a directory needs a privilege on Windows.
  if (process.platform === 'win32')
    return installRoot
  const versionsDir = join(installRoot, 'versions')
  let installed: string[]
  let current: string
  try {
    installed = readdirSync(versionsDir, { withFileTypes: true }).filter(entry => entry.isDirectory() && !entry.name.startsWith('.')).map(entry => entry.name)
    current = basename(readlinkSync(join(installRoot, 'current')))
  }
  catch {
    return installRoot
  }
  if (!installed.includes(current))
    return installRoot
  // A second call for the same run directory builds the directory again. Removing a
  // directory of links deletes the links, never the versions that they name.
  rmSync(dataDir, { recursive: true, force: true })
  const privateVersions = join(dataDir, 'versions')
  mkdirSync(privateVersions, { recursive: true })
  mkdirSync(join(dataDir, 'updates'), { recursive: true })
  for (const version of installed)
    symlinkSync(join(versionsDir, version), join(privateVersions, version), 'dir')
  symlinkSync(join(privateVersions, current), join(dataDir, 'current'), 'dir')
  return dataDir
}

/** fast-agent's model routing: `gpt-4o` goes to this `openai` block. */
function fastAgentConfig(baseURL: string): string {
  return `default_model: "${FAST_AGENT_MOCK_MODEL}"
openai:
  api_key: "${MODEL_KEY}"
  base_url: "${baseURL}"
zai:
  api_key: "${MODEL_KEY}"
  base_url: "${baseURL}"
  default_model: "${MOCK_MODELS.zai}"
`
}

/**
 * Prevent native credential-store calls through private PATH wrappers.
 *
 * Junie runs which security and the __junie_availability_check_ keychain check to select its secret store.
 * That check would access the user's keychain.
 * A private security wrapper refuses every call, so Junie uses its file store under JUNIE_HOME.
 * Linux uses the same mechanism with secret-tool.
 * Windows uses the Win32 credential manager, which PATH cannot replace.
 *
 * macOS zsh rebuilds PATH in its system login profile.
 * Private zsh startup files restore the wrapper directory after that profile.
 * The Junie launcher also restores it before it starts the installed CLI.
 * The Worker finds this launcher through PATH.
 * Playwright's availability check still finds the installed CLI in its own process.
 */
export const CREDENTIAL_STORE_SHIM_LOG = 'credential-store-attempts.log'

function credentialStoreShimEnv(shimsDir: string, searchPath: string | undefined): Record<string, string> {
  if (process.platform === 'win32')
    return {}
  const stubbed = process.platform === 'darwin' ? ['security'] : ['secret-tool']
  const logPath = join(shimsDir, CREDENTIAL_STORE_SHIM_LOG)
  for (const name of stubbed) {
    const stub = join(shimsDir, name)
    writeFileSync(stub, `#!/bin/sh\nprintf '%s\\n' ${posixQuote(name)} >> ${posixQuote(logPath)}\necho "leapmux e2e: refusing to touch the system credential store" >&2\nexit 1\n`, { mode: 0o755 })
  }
  const realJunie = findBinary('junie')
  if (realJunie !== null) {
    // The CLI refreshes the launcher that JUNIE_SHIM_PATH names, which is the file that
    // started it, when the launcher is older than the one in the installed version. Run
    // a private copy of the launcher script, so that refresh cannot rewrite the
    // developer's own ~/.local/bin/junie. The launcher reads no path of its own: it
    // takes JUNIE_DATA from the environment.
    const launcher = junieLauncherCopy(realJunie, join(shimsDir, 'junie-launcher'))
    writeFileSync(join(shimsDir, 'junie'), `#!/bin/sh\nexport PATH=${posixQuote(shimsDir)}:"$PATH"\nexec ${posixQuote(launcher)} "$@"\n`, { mode: 0o755 })
  }
  return { PATH: [shimsDir, searchPath ?? process.env.PATH ?? ''].join(delimiter) }
}

/** The size above which an installed `junie` is a program, not the launcher script. */
const JUNIE_LAUNCHER_MAX_BYTES = 1 << 20

/**
 * The path that the E2E Junie launcher runs: a private copy when the installed file is the
 * managed launcher script, and the installed file itself otherwise.
 */
function junieLauncherCopy(installed: string, copy: string): string {
  let script: string
  try {
    // The launcher script is some 30 KB. A larger file is a program, not the script.
    if (statSync(installed).size > JUNIE_LAUNCHER_MAX_BYTES)
      return installed
    script = readFileSync(installed, 'utf8')
  }
  catch {
    return installed
  }
  if (!script.startsWith('#!') || !script.includes('JUNIE_MANAGED_SHIM'))
    return installed
  copyFileSync(installed, copy)
  chmodSync(copy, 0o755)
  return copy
}

/** Quote one value for a POSIX shell. */
function posixQuote(value: string): string {
  return `'${value.replaceAll('\'', '\'\\\'\'')}'`
}

function mockServerOrigin(value: string): string {
  const url = new URL(value)
  const loopback = url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '[::1]'
  if (url.protocol !== 'http:' || !loopback || url.pathname !== '/' || url.username || url.password || url.search || url.hash)
    throw new Error('The mock model server URL must be a loopback HTTP origin')
  return url.origin
}

// `check_for_update_on_startup` turns off the update notice of Codex's TUI. The
// `codex app-server` that the worker starts never reads it, and never updates: only
// the TUI and the `codex update` and `codex app-server daemon` commands do.
function codexConfig(baseURL: string, mcpFormServer: string): string {
  return `model_provider = "leapmux-e2e"
check_for_update_on_startup = false

# Codex consolidates its own memories in a background turn, against a model of
# its own choice and with no user prompt. That turn would reach the mock
# endpoint outside any test's script.
[memories]
generate_memories = false
use_memories = false
dedicated_tools = false

# Codex leaves update_plan off unless its own config enables it. The E2E todo
# case needs the native tool so its notification can reach the browser.
[tools.update_plan]
enabled = true

[mcp_servers.form_probe]
command = ${JSON.stringify(process.execPath)}
args = [${JSON.stringify(mcpFormServer)}]

[model_providers.leapmux-e2e]
name = "LeapMux E2E"
base_url = "${baseURL}"
env_key = "LEAPMUX_E2E_MODEL_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`
}

function openCodeFamilyConfig(baseURL: string, mcpEchoServer: string): Record<string, unknown> {
  return {
    formatter: false,
    lsp: false,
    mcp: { echo_probe: { type: 'local', command: [process.execPath, mcpEchoServer] } },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`,
    provider: {
      [MOCK_PROVIDER_IDS.openCode]: openCodeFamilyProvider(baseURL, [
        openCodeFamilyModel(MOCK_MODELS.zai, 'GLM-5.3 Flash'),
        openCodeFamilyModel(MOCK_MODELS.pi, 'GLM-5.3'),
      ]),
    },
  }
}

/** The mock provider block, in the shape the OpenCode family reads. */
function openCodeFamilyProvider(baseURL: string, models: Record<string, unknown>[]): Record<string, unknown> {
  return {
    name: 'LeapMux E2E',
    id: MOCK_PROVIDER_IDS.openCode,
    env: [],
    npm: '@ai-sdk/openai-compatible',
    models: Object.fromEntries(models.map(model => [model.id, model])),
    options: { apiKey: MODEL_KEY, baseURL },
  }
}

/** The reasoning variants of each mock model. */
const REASONING_VARIANTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const

/**
 * One model of the mock provider block, with each reasoning variant.
 *
 * Each variant sends its effort to the mock model. The installed OpenCode family
 * merges a configured variant over its built-in one.
 */
function openCodeFamilyModel(id: string, name: string): Record<string, unknown> {
  return {
    id,
    name,
    attachment: false,
    reasoning: true,
    temperature: false,
    tool_call: true,
    // MiMo's read tool returns an image or a PDF only when the model declares
    // the input modality. An absent entry reads as false and the tool answers
    // with a text refusal instead of the file.
    modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
    release_date: '2026-01-01',
    limit: { context: 128_000, output: 16_000 },
    cost: { input: 0, output: 0 },
    options: {},
    variants: Object.fromEntries(REASONING_VARIANTS.map(variant => [variant, { reasoningEffort: variant }])),
  }
}

/**
 * MiMo Code's configuration: the OpenCode family's provider block, which MiMo reads
 * unchanged, and the switches for every model request that no test scripts.
 *
 * - A second model, so that a spec can switch models and read the switch off
 *   the next request.
 * - The shared mock model variants send `reasoning_effort`, so a spec can read
 *   the effort off the next request too.
 * - `enabled_providers` hides MiMo's own built-in providers, so the model menu
 *   holds the mock alone.
 * - `agent.title.disable` stops the title request that otherwise runs beside the
 *   first turn.
 * - `retry` makes a failed request fail once. A retry would consume the next
 *   scripted step.
 * - `snapshot` and `share` keep MiMo from writing git snapshots and from
 *   offering a public link.
 *
 * The configuration holds no `autoupdate` key. MiMo reads that key only from its
 * global config files, never from this inline value, so MIMOCODE_DISABLE_AUTOUPDATE
 * in the environment carries the switch.
 */
function mimoCodeConfig(baseURL: string, mcpConfirmationServer: string, mcpEchoServer: string): Record<string, unknown> {
  const noRetry = { mode: 'bounded', maxRetries: 0 }
  return {
    ...openCodeFamilyConfig(baseURL, mcpEchoServer),
    provider: {
      [MOCK_PROVIDER_IDS.openCode]: openCodeFamilyProvider(baseURL, [
        openCodeFamilyModel(MOCK_MODELS.zai, 'GLM-5.3 Flash'),
        openCodeFamilyModel(MOCK_MODELS.pi, 'GLM-5.3'),
      ]),
    },
    enabled_providers: [MOCK_PROVIDER_IDS.openCode],
    mcp: {
      form_probe: { type: 'local', command: [process.execPath, mcpConfirmationServer] },
      echo_probe: { type: 'local', command: [process.execPath, mcpEchoServer] },
    },
    agent: { title: { disable: true } },
    share: 'disabled',
    snapshot: false,
    retry: { request: noRetry, stream: noRetry, network: noRetry, server: noRetry, rateLimit: noRetry, unknown: noRetry },
  }
}

function piModels(baseURL: string): Record<string, unknown> {
  const model = (id: string, name: string) => ({
    id,
    name,
    reasoning: true,
    input: ['text', 'image'],
    contextWindow: 128_000,
    maxTokens: 16_000,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  })
  return {
    providers: {
      [PI_PROVIDER_ID]: {
        baseUrl: baseURL,
        api: 'openai-completions',
        apiKey: MODEL_KEY,
        models: [model(MOCK_MODELS.pi, 'GLM-5.3'), { ...model(MOCK_MODELS.zai, 'GLM-5.3 Flash'), compat: { supportsReasoningEffort: true } }],
      },
    },
  }
}

function ohMyPiModels(baseURL: string): Record<string, unknown> {
  const primary = {
    id: MOCK_MODELS.ohMyPi,
    name: 'GLM-5.3',
    // A reasoning model, so the thinking-level axis exists.
    reasoning: true,
    input: ['text', 'image'],
    contextWindow: 128_000,
    maxTokens: 16_000,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  }
  return {
    providers: {
      [MOCK_PROVIDER_IDS.ohMyPi]: {
        baseUrl: baseURL,
        // omp first treats apiKey as an environment variable identifier.
        // It uses the literal key only when that variable does not exist.
        apiKey: 'LEAPMUX_E2E_MODEL_API_KEY',
        api: 'openai-completions',
        models: [primary, { ...primary, id: OH_MY_PI_ALT_MODEL_WIRE_ID, name: 'GLM-5.3 Alternate' }],
      },
    },
  }
}

/**
 * Every provider omp 18.2.11 bundles. omp has no offline switch and no wildcard, and
 * each provider it keeps enabled refreshes a catalog or probes a local server on its
 * own. A provider a later omp adds meets the refusing proxy above instead.
 */
const OH_MY_PI_BUNDLED_PROVIDERS = [
  'abliteration',
  'aiand',
  'aimlapi',
  'alibaba-coding-plan',
  'alibaba-token-plan',
  'amazon-bedrock',
  'anthropic',
  'azure',
  'baseten',
  'bedrock-mantle',
  'cerebras',
  'charm-hyper',
  'cline-pass',
  'cloudflare-ai-gateway',
  'commandcode',
  'coreweave',
  'cursor',
  'deepinfra',
  'deepseek',
  'devin',
  'firepass',
  'fireworks',
  'github-copilot',
  'gitlab-duo-agent',
  'gitlab-duo',
  'gmi-cloud',
  'google-antigravity',
  'google-gemini-cli',
  'google-vertex',
  'google',
  'groq',
  'huggingface',
  'kilo',
  'kimi-code',
  'litellm',
  'llama.cpp',
  'lm-studio',
  'local',
  'meta',
  'minimax-code-cn',
  'minimax-code',
  'minimax',
  'minimax-cn',
  'mistral',
  'moonshot',
  'muse-code',
  'nanogpt',
  'novita',
  'nvidia',
  'ollama-cloud',
  'ollama',
  'openai-codex',
  'openai',
  'opencode-go',
  'opencode-zen',
  'openrouter',
  'qianfan',
  'qwen-portal',
  'sakana',
  'siliconflow-cn',
  'siliconflow',
  'singularityapi-dev',
  'singularityapi-tech',
  'stepfun',
  'synthetic',
  'together',
  'typesafe',
  'umans',
  'venice',
  'vercel-ai-gateway',
  'vllm',
  'wafer-serverless',
  'web',
  'xai-oauth',
  'xai',
  'xiaomi-token-plan-ams',
  'xiaomi-token-plan-cn',
  'xiaomi-token-plan-sgp',
  'xiaomi',
  'yolo-auto',
  'zai',
  'zenmux',
  'zhipu-coding-plan',
]

/**
 * omp's settings for a run that reaches the mock endpoint alone, and runs only the
 * turns a test scripts.
 */
function ohMyPiConfig(): Record<string, unknown> {
  const model = `${MOCK_PROVIDER_IDS.ohMyPi}/${MOCK_MODELS.ohMyPi}`
  return {
    // A subagent runs the task role. Pinning its thinking level stops the per-prompt
    // classifier, which asks the judge role one question before each child turn.
    modelRoles: { default: model, task: `${model}:off` },
    disabledProviders: OH_MY_PI_BUNDLED_PROVIDERS,
    startup: { checkUpdate: false, setupWizard: false },
    marketplace: { autoUpdate: 'off' },
    dev: { autoqa: false },
    lsp: { enabled: false },
    features: { unexpectedStopDetection: 'none' },
    ttsr: { judge: 'off' },
    advisor: { enabled: false },
    memory: { backend: 'off' },
    memories: { enabled: false },
    title: { refreshOnReplan: false },
    recap: { enabled: false },
    images: { describeForTextModels: false },
    compaction: { asyncEnabled: false, keepRecentTokens: 128 },
    magicKeywords: { enabled: false },
    todo: { reminders: false },
    retry: { enabled: false },
    // A subagent that runs in the foreground finishes inside its `task` call, so its
    // parent asks for one more turn in a fixed order. A background subagent would
    // wake the parent with a turn of omp's own.
    async: { enabled: false },
    bash: { autoBackground: { enabled: false } },
    // The `replace` edit takes the text before and after. The default `hashline`
    // edit addresses lines by a hash of the file that only omp itself can compute,
    // which a scripted turn cannot know.
    edit: { mode: 'replace' },
  }
}

function piPackagePaths(realHomeDir: string | undefined): string[] {
  if (!realHomeDir)
    return []
  const modules = join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules')
  return [
    '@gotgenes/pi-nocd',
    '@juicesharp/rpiv-args',
    '@tintinweb/pi-subagents',
    '@juicesharp/rpiv-ask-user-question',
    '@narumitw/pi-plan-mode',
    'pi-goal-x',
    '@juicesharp/rpiv-todo',
  ].map(packageName => join(modules, packageName))
}

/**
 * Reasonix's credential file, which binds the variable of `api_key_env` to the mock key.
 *
 * Reasonix 1.38 reads that variable only from `$REASONIX_HOME/.env` and never from the
 * process environment (internal/config/config.go ProviderEntry.APIKey). A loopback
 * base_url needs no key, so without this file each request reaches the mock with no
 * credential.
 */
function reasonixCredentials(): string {
  return `LEAPMUX_E2E_MODEL_API_KEY=${MODEL_KEY}\n`
}

function reasonixConfig(baseURL: string): string {
  return `default_model = "${REASONIX_PROVIDER_ID}/${MOCK_MODELS.deepseek}"

[[providers]]
name = "${REASONIX_PROVIDER_ID}"
kind = "openai"
base_url = "${baseURL}"
model = "${MOCK_MODELS.deepseek}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"
context_window = 128000
max_output_tokens = 16000
reasoning_protocol = "openai"
supported_efforts = ["low", "medium", "high"]
default_effort = "high"
vision_models = ["${MOCK_MODELS.deepseek}"]

[[providers]]
name = "${REASONIX_ALT_PROVIDER_ID}"
kind = "openai"
base_url = "${baseURL}"
model = "${MOCK_MODELS.pi}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"
context_window = 128000
max_output_tokens = 16000
reasoning_protocol = "openai"
supported_efforts = ["low", "medium", "high"]
default_effort = "high"
`
}

/**
 * Codewhale's configuration.
 *
 * Each table turns off one request that no test scripts:
 *
 * - `telemetry` and `[update]`: the telemetry upload and the release check.
 * - `[retry]`: the client retries a failed model request three times, so an
 *   unscripted turn would reach the mock three more times.
 * - `[reasoning_only]`: an answer that holds only reasoning makes the client
 *   ask again, twice by default.
 * - `[snapshots]`: a side git repository under HOME that snapshots each turn.
 *
 * `[tools] user_input_timeout_seconds = 0` removes the wait limit on an
 * approval or a question. The default is 300 seconds, after which the runtime
 * denies the approval by itself, and a slow run would read as a user who
 * refused.
 */
function codewhaleConfig(baseURL: string): string {
  return `provider = "${CODEWHALE_PROVIDER_ID}"
default_text_model = "${MOCK_MODELS.deepseek}"
telemetry = false
allow_shell = true

[providers.${CODEWHALE_PROVIDER_ID}]
base_url = "${baseURL}"
api_key = "${MODEL_KEY}"
auth_mode = "api-key"
model = "${CODEWHALE_VISION_MODEL_ID}"

[tools]
user_input_timeout_seconds = 0

[update]
check_for_updates = false

[snapshots]
enabled = false

[retry]
enabled = false

[reasoning_only]
max_reprompts = 0
`
}

/** Codewhale accepts image input only from a fresh catalog for this endpoint. */
function codewhaleCatalog(baseURL: string): Record<string, unknown> {
  const fingerprint = createHash('sha256').update(baseURL).digest('hex')
  const fetchedAt = Math.floor(Date.now() / 1000)
  const provider = `${CODEWHALE_PROVIDER_ID}:${CODEWHALE_PROVIDER_ID}`
  const offering = (model: string, endpoint: string, image: boolean, isDefault: boolean) => ({
    provider: CODEWHALE_PROVIDER_ID,
    wire_model_id: model,
    endpoint_key: endpoint,
    default_for_provider: isDefault,
    modalities: { input: image ? ['text', 'image'] : ['text'], output: ['text'] },
    source: { kind: 'live', base_url_fingerprint: fingerprint, fetched_at: fetchedAt },
  })
  return {
    schema_version: 2,
    cache: {
      entries: {
        [`${provider}\x1F${fingerprint}`]: {
          provider,
          base_url_fingerprint: fingerprint,
          fetched_at: fetchedAt,
          ttl_secs: 86_400,
          offerings: [
            offering(MOCK_MODELS.deepseek, 'responses', false, true),
            offering(CODEWHALE_VISION_MODEL_ID, 'chat', true, false),
          ],
          status: { state: 'fresh' },
        },
      },
    },
  }
}

/**
 * Kimi Code's `config.toml`.
 *
 * `auto_session_title = false` keeps the title local. A title that a model
 * writes would be a request with no scenario marker. Kimi sends a model request
 * only for a turn, so no other housekeeping request reaches the mock.
 */
function kimiConfig(baseURL: string): string {
  const provider = MOCK_PROVIDER_IDS.kimi
  return `default_model = "${KIMI_MOCK_MODELS.thinking}"
telemetry = false
auto_session_title = false

[providers.${provider}]
type = "openai"
base_url = "${baseURL}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"

[models."${KIMI_MOCK_MODELS.thinking}"]
provider = "${provider}"
model = "${MOCK_MODELS.zai}"
display_name = "GLM-5.3 Flash"
max_context_size = 128000
capabilities = ["tool_use", "thinking", "image_in"]
support_efforts = ["low", "medium", "high"]
default_effort = "high"

[models."${KIMI_MOCK_MODELS.plain}"]
provider = "${provider}"
model = "${MOCK_MODELS.pi}"
display_name = "GLM-5.3"
max_context_size = 128000
capabilities = ["tool_use"]
`
}

/**
 * Configure Grok Build with one private model.
 *
 * remote_fetch = false disables remote settings and catalog requests.
 * The fixture hides both built-in models, so LeapMux's catalog contains only the mock.
 * The session title uses that model too.
 * Otherwise Grok selects an auxiliary model that the mock does not advertise.
 */
function grokConfig(baseURL: string): string {
  const model = MOCK_MODELS.grok
  return `[cli]
auto_update = false

[features]
telemetry = false
remote_fetch = false

[models]
default = "${model}"
session_summary = "${model}"

[model."grok-4.6"]
hidden = true

[model."grok-4.5"]
hidden = true

[model."${model}"]
model = "${model}"
base_url = "${baseURL}"
name = "Grok E2E"
api_key = "${MODEL_KEY}"
api_backend = "chat_completions"
context_window = 128000
supports_reasoning_effort = true
reasoning_effort = "medium"

[[model."${model}".reasoning_efforts]]
id = "low"
value = "low"
label = "Low"
default = false

[[model."${model}".reasoning_efforts]]
id = "medium"
value = "medium"
label = "Medium"
default = true

[[model."${model}".reasoning_efforts]]
id = "high"
value = "high"
label = "High"
default = false

[model."${GROK_ALT_MODEL_ID}"]
model = "${GROK_ALT_MODEL_ID}"
base_url = "${baseURL}"
name = "Grok E2E Alternate"
api_key = "${MODEL_KEY}"
api_backend = "chat_completions"
context_window = 128000
`
}

/**
 * Kiro's settings: every service it calls, pinned to the mock.
 *
 * Kiro's v3 engine reads its runtime service from `api.krs.service` and its control
 * plane from `api.cps.service`, and it honors an `http` endpoint on loopback alone.
 * The three older keys are the v2 engine's, which LeapMux does not start. They stay
 * pinned so no Kiro engine can reach its real service. Telemetry and the update
 * check are off.
 */
function kiroSettings(origin: string): Record<string, unknown> {
  const service = { endpoint: origin, region: 'us-east-1' }
  return {
    'api.krs.service': service,
    'api.cps.service': service,
    'api.codewhisperer.service': service,
    'api.q.service': origin,
    'api.kiroauth.service': origin,
    'telemetry.enabled': false,
    'app.disableAutoupdates': true,
  }
}

/**
 * Qwen Code's settings: one OpenAI-compatible model, pinned to the mock.
 *
 * Qwen's own default approval mode is `auto`, whose classifier asks the model
 * before a tool runs; `default` asks the reader instead. The managed memory pass
 * and the follow-up suggestions each send a model request after a turn. The
 * to-do tool and workflows are opt-in, and a spec uses both.
 */
function qwenSettings(baseURL: string, mcpEchoServer: string): Record<string, unknown> {
  const primary = {
    id: MOCK_MODELS.qwen,
    name: 'Qwen E2E',
    baseUrl: baseURL,
    envKey: 'LEAPMUX_E2E_MODEL_API_KEY',
    capabilities: { vision: true, reasoning: { thinking: true, efforts: ['low', 'medium', 'high'], defaultEffort: 'high', disableField: 'reasoning_effort' } },
    generationConfig: { contextWindowSize: 128_000, modalities: { image: true, pdf: true } },
  }
  return {
    $version: 4,
    security: { auth: { selectedType: QWEN_AUTH_TYPE } },
    model: { name: MOCK_MODELS.qwen },
    mcpServers: { echo_probe: { command: process.execPath, args: [mcpEchoServer] } },
    modelProviders: {
      [QWEN_AUTH_TYPE]: [primary, { ...primary, id: QWEN_ALT_MODEL_WIRE_ID, name: 'Qwen E2E Alternate' }],
    },
    tools: { approvalMode: 'default', todoWrite: { enabled: true }, workflowsEnabled: true },
    memory: { enableManagedAutoMemory: false, enableManagedAutoDream: false },
    ui: { enableFollowupSuggestions: false },
    privacy: { usageStatisticsEnabled: false },
    general: { enableAutoUpdate: false },
  }
}

function zcodeLegacyConfig(baseURL: string): Record<string, unknown> {
  return {
    provider: {
      [MOCK_PROVIDER_IDS.zcode]: {
        name: 'LeapMux E2E',
        kind: 'openai-compatible',
        source: 'custom',
        enabled: true,
        options: { apiKey: MODEL_KEY, baseURL },
        models: {
          [MOCK_MODELS.zai]: {
            name: MOCK_MODELS.zai,
            reasoning: { enabled: true, variants: ['low', 'medium', 'high'], defaultVariant: 'high' },
            limit: { context: 128_000, output: 16_000 },
            modalities: { input: ['text'], output: ['text'] },
            zcode: { priority: 0 },
          },
          [MOCK_MODELS.pi]: {
            name: MOCK_MODELS.pi,
            reasoning: { enabled: true, variants: ['low', 'medium', 'high'], defaultVariant: 'high' },
            limit: { context: 128_000, output: 16_000 },
            modalities: { input: ['text'], output: ['text'] },
            zcode: { priority: 1 },
          },
        },
      },
    },
  }
}

function zcodePersonalConfig(baseURL: string): Record<string, unknown> {
  const providerID = MOCK_PROVIDER_IDS.zcode
  const modelID = MOCK_MODELS.zai
  return {
    schemaVersion: 1,
    config: {
      providerOrder: [providerID],
      providerConfigRules: {
        providerRules: [{
          providerId: providerID,
          providerName: 'LeapMux E2E',
          enabled: true,
          config: {
            group: 'standard-personal',
            access: { type: 'api-key', apiKey: MODEL_KEY },
            api: { type: 'openai-chat-completions', baseUrl: baseURL },
            personalModelIds: [modelID, MOCK_MODELS.pi],
            modelOrder: [modelID, MOCK_MODELS.pi],
            visibility: 'visible',
          },
        }],
      },
      modelConfigRules: {
        providerModelRules: [modelID, MOCK_MODELS.pi].map(id => ({ providerId: providerID, modelId: id, config: { enabled: true } })),
        manualProviderModelRules: [],
      },
      defaultModelSelection: {
        providerId: providerID,
        modelId: modelID,
        options: { reasoningLevel: 'high' },
      },
    },
  }
}

/**
 * Build Cline's providers.json in the native cline auth format.
 *
 * Cline treats a schema failure as absent settings.
 * An entry without updatedAt selects the built-in provider endpoint, which the private proxy refuses.
 */
function clineProviders(baseURL: string, updatedAt: number): Record<string, unknown> {
  return {
    version: 1,
    lastUsedProvider: CLINE_PROVIDER_ID,
    modes: {},
    providers: {
      [CLINE_PROVIDER_ID]: {
        settings: { provider: CLINE_PROVIDER_ID, apiKey: MODEL_KEY, model: MOCK_MODELS.cline, baseUrl: baseURL },
        updatedAt: new Date(updatedAt).toISOString(),
        tokenSource: 'manual',
      },
    },
  }
}

/**
 * Cline's feature-flag cache, holding no flag. Cline trusts the cache for an hour
 * after `updatedAt`, which covers a whole run.
 */
function clineFeatureFlags(updatedAt: number): Record<string, unknown> {
  return { version: 2, updatedAt, userId: null, flagsPayload: { featureFlags: {}, featureFlagPayloads: {} } }
}

function writeJSON(path: string, value: unknown): void {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}

/**
 * Run the two CLI steps the App Server needs before an agent starts.
 *
 * `letta backend local` pins the local backend, and `letta connect
 * openai-compatible` discovers the mock's models so the agent's model handle
 * resolves. Both write the isolated store; neither reaches a real account.
 * Each step fails on a repeat run, which is fine: the store is already
 * prepared.
 */
async function prepareLettaBackend(lettaHome: string, lettaBackendDir: string, baseURL: string, temporaryEnv: Record<'TMPDIR' | 'TEMP' | 'TMP', string>): Promise<void> {
  // The real install dirs go first on PATH: the `letta` entry is a JS file
  // whose `#!/usr/bin/env node` must resolve to a real node, not a mise shim.
  // HOME must be the isolated home too: `letta model list` reads the model
  // catalog under HOME, not only LETTA_HOME, and the real HOME holds the
  // developer's own records.
  //
  // Every proxy variable is stripped: the loopback mock must be reached
  // direct, or the model-discovery request never arrives and the catalog is
  // empty.
  const base = { ...process.env }
  for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY', 'all_proxy'])
    delete base[key]
  const env = { ...base, ...temporaryEnv, ...lettaEnv(lettaHome, lettaBackendDir), HOME: lettaHome, PATH: agentSearchPath(process.env.PATH), NO_PROXY: '127.0.0.1,localhost', no_proxy: '127.0.0.1,localhost' }
  const letta = findBinary('letta', env)
  if (letta === null)
    return
  for (const args of [
    ['backend', 'local'],
    ['connect', 'openai-compatible', '--base-url', baseURL, '--api-key', MODEL_KEY],
  ]) {
    try {
      execFileSync(letta, args, { env, encoding: 'utf8', timeout: 60_000, stdio: ['ignore', 'pipe', 'pipe'] })
    }
    catch {
      // A repeat run fails each step against a store that is already prepared.
    }
  }
}
