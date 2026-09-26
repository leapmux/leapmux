import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import process from 'node:process'
import { agentSearchPath, agentSearchPathEnv, findBinary } from './binaryOnPath'

const MODEL_KEY = 'leapmux-e2e-model-key'
const COPILOT_TOKEN = 'github_pat_leapmuxe2e000000000000000000000000000000000000000000'

/**
 * The model identifiers the isolated agent configuration pins.
 *
 * This is the one source for them. The provider configuration below writes
 * them, the pinned settings catalog selects them, and the mock server
 * advertises them, so a model the agent asks for is always a model the
 * endpoint lists.
 */
export const MOCK_MODELS = {
  /** Claude Code, over the Anthropic Messages protocol. */
  anthropic: 'sonnet',
  /** Codex and GitHub Copilot, over the OpenAI protocols. */
  openai: 'gpt-5.6-luna',
  /** Goose, Kilo, MiMo Code, OpenCode, and ZCode. */
  zai: 'glm-5.3-flash',
  /** Pi, and the second model of MiMo Code, which a settings spec switches to. */
  pi: 'glm-5.3',
  /** Oh My Pi. */
  ohMyPi: 'glm-5.3',
  /** Reasonix and Codewhale. */
  deepseek: 'deepseek-flash',
  /**
   * Grok Build. The catalog key in its `config.toml` and the model id it sends
   * are one word, so the id LeapMux stores is the id the endpoint receives.
   */
  grok: 'grok-e2e',
  /**
   * Qwen Code. Qwen states a model to its client as `<id>(<auth type>)`, so a
   * fixture that pins one uses `QWEN_MODEL_ID`.
   */
  qwen: 'qwen-e2e',
  /**
   * Cline. Its `openai-compatible` provider sends the model id of its settings
   * unchanged, and Cline never lists the endpoint's models, so the id is one
   * word of its own.
   */
  cline: 'cline-e2e',
  /** Factory Droid. Its BYOK custom-model entry sends this id unchanged. */
  droid: 'droid-e2e',
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
 * The provider identifiers that qualify a model id.
 *
 * OpenCode, Kilo, MiMo Code and ZCode each address a model as
 * `<provider>/<model>`, so a fixture that pins a model needs the same
 * identifier this file registers.
 */
export const MOCK_PROVIDER_IDS = {
  /**
   * The OpenCode, Kilo and MiMo Code provider block below.
   *
   * The identifier must belong to no public catalog. OpenCode and Kilo merge a
   * configured provider onto the built-in entry of the same id, and Kilo's
   * gateway then keeps its own base URL: the agent answered from the real
   * endpoint and the mock saw no request at all.
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
 * The omp profile the run uses.
 *
 * omp and Pi share two variable names, PI_CODING_AGENT_DIR and
 * PI_CODING_AGENT_SESSION_DIR, so the Pi entries below would point omp at Pi's own
 * agent directory. A named profile is omp's own switch, and it wins: omp then reads
 * `~/.omp/profiles/<name>/agent` and ignores PI_CODING_AGENT_DIR. `OMP_PROFILE`
 * outranks the older `PI_PROFILE` too, so an inherited value cannot select another
 * profile.
 */
export const OH_MY_PI_PROFILE = 'leapmux-e2e'

/**
 * The hosts that every client reaches directly, past the refusing proxy: the
 * loopback addresses where the mock listens.
 */
const LOOPBACK_NO_PROXY = '127.0.0.1,localhost,::1'

/** Pi addresses a model through a named provider in its own `models.json`. */
const PI_PROVIDER_ID = 'zai'

/** Reasonix names its provider block, and `default_model` qualifies with it. */
const REASONIX_PROVIDER_ID = 'deepseek'

/**
 * The built-in Codewhale route that the isolated configuration points at the
 * mock.
 *
 * `deepseek` and no custom route, because Codewhale reads `reasoning_content`
 * as thinking only on a route that it knows to reason. On `openai`, the probe
 * saw the reasoning text merged into the answer.
 */
const CODEWHALE_PROVIDER_ID = 'deepseek'

/** The auth type the Qwen configuration selects, which qualifies its model ids. */
const QWEN_AUTH_TYPE = 'openai'

/** The model id Qwen reports for the model its configuration pins. */
export const QWEN_MODEL_ID = `${MOCK_MODELS.qwen}(${QWEN_AUTH_TYPE})`
export const JUNIE_MOCK_MODEL = 'custom:mock-model'
export const FAST_AGENT_MOCK_MODEL = 'gpt-4o'

/**
 * The model id a fixture pins for a provider that addresses a custom entry by
 * a QUALIFIED handle. CodeBuddy takes `custom-local:<id>`, Qoder
 * `<provider>/<model>`, and Letta Code `provider/model`. A bare
 * `MOCK_MODELS` value selects no custom entry for these three.
 */
export const CODEBUDDY_MODEL_ID = `custom-local:${MOCK_MODELS.deepseek}`
export const QODER_MODEL_ID = `mockprov/${MOCK_MODELS.deepseek}`
export const LETTA_MODEL_ID = `openai-compatible/${MOCK_MODELS.letta}`

/**
 * The switches that stop Grok Build from calling the model outside a turn a test
 * scripts, and from reaching any network endpoint but the mock.
 *
 * Grok's own end-to-end sandbox (`xai-grok-test-support/src/sandbox.rs`) sets the
 * same telemetry, feedback and update switches. The turn summary, the title
 * refresh, the session recap, the prompt suggestions and the memory pass each
 * send a model request after a turn, and none carries a turn a test sent. The
 * FIRST title of a session cannot be switched off; the housekeeping rules of
 * `./mockModelScenario` answer it.
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
 * scripts, and from sending usage statistics. The same switches are in its
 * `settings.json`; the environment is the second guard, because Qwen reads it
 * first.
 */
const QWEN_QUIET_ENV: Readonly<Record<string, string>> = {
  QWEN_DISABLE_AUTO_TITLE: '1',
  QWEN_USAGE_STATISTICS_ENABLED: 'false',
  QWEN_TELEMETRY_ENABLED: 'false',
  QWEN_CODE_SKIP_UPDATE_CHECK_ONCE: 'true',
}

/**
 * The key Kiro sends as its bearer token. Kiro checks nothing about it locally but
 * the `ksk_` prefix of an API key. The mock refuses any other bearer, so a real
 * login that Kiro reads from somewhere that HOME does not isolate, such as the
 * macOS keychain, fails visibly instead of reaching the mock unseen.
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
 * Amp talks to its OWN service, not to a model API, so it points at the mock's Amp
 * surface (`./ampSurface`), which plays that service and runs the agent loop.
 *
 * The real service cannot be reached with the user's account, and that holds by
 * construction rather than by a list of switches: Amp keeps its login in
 * `~/.local/share/amp` under the HOME below, which the run creates empty, and the
 * only credential Amp holds is the fake key here. A request that reached the real
 * service anyway would carry no credential it accepts.
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
export const CLINE_PROVIDER_ID = 'openai-compatible'

/**
 * Cline's isolated configuration and data.
 *
 * The worker reads the user's own Cline settings, as Cline's CLI does, so the run
 * points Cline at directories of its own: `CLINE_DIR` and `CLINE_DATA_DIR` state the
 * directories that the isolated HOME implies anyway, so a developer's own value
 * cannot point the agent at a real configuration.
 *
 * - `providers.json` selects the mock as the last used provider, in the shape that
 *   `cline auth` writes.
 * - `global-settings.json` opts out of telemetry. Cline compiles its telemetry
 *   endpoint in, so no environment variable can turn it off.
 * - The feature-flag cache holds an empty answer, so Cline does not fetch its flags
 *   from its own host for an hour. A later fetch meets the refusing proxy.
 *
 * Each variable that moves one part of the data is EMPTY, which Cline reads as
 * unset (`process.env.X?.trim()`), so each part follows `CLINE_DATA_DIR`. They are
 * every `CLINE_*_DIR` and `CLINE_*_PATH` of Cline's `paths.ts`, the hook directory,
 * and the hook, log, capture and approval files. The worker sets the agenda
 * database and the discovery record of its own daemon after the shell's profile,
 * so an empty value here changes nothing for them. `CLINE_PROVIDER` and
 * `CLINE_MODEL` stay unset rather than empty: Cline reads them with `??`, where an
 * empty value is a value. The worker states the provider and the model at
 * `session.create` anyway.
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
    // The npm update check. The worker sets it for the daemon as well.
    CLINE_NO_AUTO_UPDATE: '1',
  }
}

/** Every pinned identifier, for the mock endpoint's catalog route. */
export const MOCK_MODEL_IDS: readonly string[] = [...new Set(Object.values(MOCK_MODELS))]

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
  const openAIBaseURL = `${origin}/v1`
  const homeDir = join(runDir, 'agent-home')
  const codexHome = join(homeDir, '.codex')
  const piAgentDir = join(homeDir, '.pi', 'agent')
  const reasonixHome = join(homeDir, '.reasonix')
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
  for (const directory of [codexHome, piAgentDir, reasonixHome, zcodeDir, copilotHome, cursorConfigDir, codewhaleHome, grokHome, qwenHome, kiroSettingsDir, kimiHome, ohMyPiAgentDir, mimoHome, clineSettingsDir, clineCacheDir, codebuddyHome, qoderHome, factoryHome, lettaHome, lettaBackendDir, lettaProvidersDir, junieModelsDir, join(diracDir, 'data', 'state'), fastAgentHome, cliShimsDir])
    mkdirSync(directory, { recursive: true })

  writeFileSync(join(codexHome, 'config.toml'), codexConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(piAgentDir, 'models.json'), piModels(openAIBaseURL))
  writeJSON(join(piAgentDir, 'settings.json'), {
    defaultProvider: PI_PROVIDER_ID,
    defaultModel: MOCK_MODELS.pi,
    packages: piPackagePaths(options.realHomeDir),
  })
  // YAML is omp's format, and JSON is valid YAML, so the one writer serves.
  writeJSON(join(ohMyPiAgentDir, 'models.yml'), ohMyPiModels(openAIBaseURL))
  writeJSON(join(ohMyPiAgentDir, 'config.yml'), ohMyPiConfig())
  writeFileSync(join(reasonixHome, 'config.toml'), reasonixConfig(openAIBaseURL), { mode: 0o600 })
  writeFileSync(join(codewhaleHome, 'config.toml'), codewhaleConfig(openAIBaseURL), { mode: 0o600 })
  writeFileSync(join(grokHome, 'config.toml'), grokConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(qwenHome, 'settings.json'), qwenSettings(openAIBaseURL))
  writeJSON(join(codebuddyHome, 'models.json'), codebuddyModels(openAIBaseURL))
  writeJSON(join(codebuddyHome, 'settings.json'), codebuddySettings())
  writeJSON(join(qoderHome, 'settings.json'), qoderSettings(openAIBaseURL))
  qoderEndpointCaches(qoderHome, origin)
  writeJSON(join(kiroSettingsDir, 'cli.json'), kiroSettings(origin))
  const zcodeConfigPath = join(zcodeDir, 'config.json')
  const zcodePersonalConfigPath = join(zcodeDir, 'provider_config.json')
  writeJSON(zcodeConfigPath, zcodeLegacyConfig(openAIBaseURL))
  writeJSON(zcodePersonalConfigPath, zcodePersonalConfig(openAIBaseURL))
  writeFileSync(join(kimiHome, 'config.toml'), kimiConfig(openAIBaseURL), { mode: 0o600 })
  const clineWrittenAt = Date.now()
  writeJSON(join(clineSettingsDir, 'providers.json'), clineProviders(openAIBaseURL, clineWrittenAt))
  writeJSON(join(clineSettingsDir, 'global-settings.json'), { telemetryOptOut: true, autoUpdateEnabled: false })
  writeJSON(join(clineCacheDir, 'feature-flags.json'), clineFeatureFlags(clineWrittenAt))
  writeJSON(join(factoryHome, 'settings.json'), droidSettings(openAIBaseURL))
  writeJSON(join(lettaBackendDir, 'providers', 'auth.json'), lettaAuth(openAIBaseURL))
  // `letta backend local` writes this. Without it the App Server creates agents
  // against the cloud API and runtime_start fails 401.
  writeJSON(join(lettaHome, 'settings.json'), { preferredBackendMode: 'local' })
  writeJSON(join(junieModelsDir, 'mock-model.json'), junieModelProfile(`${origin}/v1/chat/completions`))
  writeFileSync(join(diracDir, 'data', 'globalState.json'), JSON.stringify({ telemetrySetting: 'disabled', autoApproveAllToggled: true, yoloModeToggled: true }), { mode: 0o600 })
  writeFileSync(join(fastAgentHome, 'fast-agent.yaml'), fastAgentConfig(openAIBaseURL), { mode: 0o600 })
  await prepareLettaBackend(lettaHome, lettaBackendDir, openAIBaseURL)

  const openCodeConfig = JSON.stringify(openCodeFamilyConfig(openAIBaseURL))
  return {
    homeDir,
    piAgentDir,
    ohMyPiAgentDir,
    env: {
      HOME: homeDir,
      USERPROFILE: homeDir,
      // The developer's PATH, with the real install directory of each mise tool
      // before mise's shims, which cannot start a tool under the isolated HOME
      // above. See `agentSearchPath`.
      ...agentSearchPathEnv(),
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
      // sends it only for the value `all`. Kilo 7.7 reads the variable once at
      // start, and the variable outranks the configuration.
      KILO_TELEMETRY_LEVEL: 'off',

      GOOSE_PROVIDER: 'openai',
      GOOSE_MODEL: MOCK_MODELS.zai,

      REASONIX_HOME: reasonixHome,

      PI_CODING_AGENT_DIR: piAgentDir,
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
      COPILOT_GITHUB_TOKEN: COPILOT_TOKEN,
      COPILOT_HOME: copilotHome,
      GITHUB_COPILOT_API_TOKEN: MODEL_KEY,

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
      // two switches stop the only requests that it sends to its own hosts: the
      // telemetry upload and the update check. Kimi downloads `rg` from its host
      // when `rg` is absent from PATH, so the run keeps the developer's PATH.
      KIMI_CODE_HOME: kimiHome,
      KIMI_DISABLE_TELEMETRY: '1',
      KIMI_CODE_NO_AUTO_UPDATE: '1',

      // MiMo reads no OPENCODE_* variable, so it takes the same inline provider
      // under its own names. Every switch below stops a request that no test
      // scripts, or a read of the developer's own configuration.
      MIMOCODE_HOME: mimoHome,
      MIMOCODE_CONFIG_CONTENT: JSON.stringify(mimoCodeConfig(openAIBaseURL)),
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
      // The workflow tool is experimental in MiMo 0.1.14 and off by default. A spec
      // runs a workflow through it (`mimoWorkflowToolCall`).
      MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL: 'true',

      ...ampEnv(origin, homeDir),

      ...clineEnv(clineDir, clineDataDir),

      ...droidEnv(homeDir, openAIBaseURL),

      ...lettaEnv(lettaHome, lettaBackendDir),
      ...codebuddyEnv(codebuddyHome),
      ...qoderEnv(qoderHome),
      ...junieEnv(homeDir, junieModelsDir, options.realHomeDir),
      DIRAC_PROVIDER: 'openai',
      DIRAC_BASE_URL: openAIBaseURL,
      DIRAC_API_KEY: MODEL_KEY,
      DIRAC_MODEL: MOCK_MODELS.deepseek,
      DIRAC_DIR: diracDir,
      FAST_AGENT_HOME: fastAgentHome,
      ...credentialStoreShimEnv(cliShimsDir, process.env.PATH),
    },
  }
}

/**
 * Factory Droid's isolated configuration.
 *
 * `FACTORY_HOME_OVERRIDE` is the primary isolation seam. It names the directory
 * that HOLDS `.factory` — not `.factory` itself — so the CLI reads its settings
 * from `<override>/.factory/settings.json` and keeps every session, log and
 * telemetry file under that tree. The settings file's `customModels[].baseUrl`
 * is the BYOK path that points at the mock. Every switch below stops a request
 * that no test scripts.
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
    FACTORY_AIRGAP_ENABLED: '1',
    FACTORY_DISABLE_DYNAMIC_CONFIG: '1',
    FACTORY_DISABLE_KEYRING: '1',
  }
}

/** Factory Droid's BYOK settings, which point the model at the mock. */
function droidSettings(baseURL: string): Record<string, unknown> {
  return {
    customModels: [
      {
        model: MOCK_MODELS.droid,
        id: `custom:Droid-0`,
        index: 0,
        baseUrl: baseURL,
        apiKey: MODEL_KEY,
        displayName: 'Mock Model',
        maxOutputTokens: 8192,
        noImageSupport: true,
        provider: 'generic-chat-completion-api',
      },
    ],
    sessionDefaultSettings: {
      model: `custom:Droid-0`,
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
    },
  }
}

/**
 * CodeBuddy's custom-local model catalog.
 *
 * CodeBuddy reads `models.json` from `$CODEBUDDY_CONFIG_DIR` and selects an
 * entry through the `custom-local:` id prefix. The `url` MUST end in
 * `/chat/completions`, and the mock MUST stream SSE: CodeBuddy always sends
 * `stream: true`, and a plain JSON completion is dropped with
 * `error_during_execution`.
 */
function codebuddyModels(baseURL: string): Record<string, unknown> {
  return {
    models: [{
      id: MOCK_MODELS.deepseek,
      name: 'Mock Model',
      vendor: 'Mock',
      apiKey: MODEL_KEY,
      maxInputTokens: 128_000,
      maxOutputTokens: 4096,
      url: `${baseURL}/chat/completions`,
      temperature: 0,
      supportsToolCall: true,
      supportsImages: false,
    }],
    availableModels: [MOCK_MODELS.deepseek],
  }
}

/** The model CodeBuddy starts on, as the `custom-local:` prefix selects it. */
function codebuddySettings(): Record<string, unknown> {
  return { model: CODEBUDDY_MODEL_ID }
}

/**
 * CodeBuddy's isolated environment.
 *
 * `configDir` is the directory that holds `models.json` and `settings.json`.
 * CODEBUDDY_CONFIG_DIR and a per-agent HOME are the isolation knobs that
 * matter. The four switches below turn off the telemetry/Galileo collectors,
 * the auto-updater and the trace collector, which are the only requests a
 * start sends that no test scripts.
 */
function codebuddyEnv(configDir: string): Record<string, string> {
  return {
    CODEBUDDY_CONFIG_DIR: configDir,
    DISABLE_TELEMETRY: '1',
    DISABLE_GALILEO: '1',
    DISABLE_AUTOUPDATER: '1',
    CODEBUDDY_DISABLE_TRACE_COLLECTOR: '1',
    CODEBUDDY_DISABLE_WORKFLOWS: '1',
  }
}

/**
 * Qoder's custom provider.
 *
 * Qoder reads `settings.json` from its `--config-dir` and selects a model as
 * `<provider>/<model>`. One `providers` entry alone registers the model: a
 * `modelConfigs.customModels` entry for the SAME key is a second registration,
 * and Qoder drops the provider with `model key ... conflicts with an existing
 * catalog model`, after which the model call falls through to the real Qoder
 * API. The entry therefore lives in `providers` alone.
 */
function qoderSettings(baseURL: string): Record<string, unknown> {
  return {
    providers: {
      mockprov: {
        type: 'openai-compatible',
        protocol: 'openai',
        authType: 'bearer',
        // The schema spells the key `baseUrl` (camelCase, not `baseURL`).
        baseUrl: baseURL,
        apiKey: MODEL_KEY,
        displayName: 'Mock Provider',
        models: [{ model: MOCK_MODELS.deepseek, displayName: 'Mock Model' }],
      },
    },
  }
}

/**
 * Qoder's isolated environment and its mocked-auth recipe.
 *
 * The headless auth gate blocks stream-json until the account is authenticated.
 * The E2E recipe mocks authentication the way Cursor and Copilot do:
 *
 *   - `QODER_SDK_AUTH_PAYLOAD_FILE` installs a fake access token through the SDK
 *     auth seam (`initFromAccessToken`), which is the one credential injection
 *     that reaches the stream-json path. `QODER_AGENT_SDK_ENTRYPOINT` is the
 *     switch that selects it, and the CLI consumes the payload file once.
 *   - `qoderEndpointCaches` pre-seeds the endpoint-election caches, so the
 *     token exchange (`/api/v1/jobToken/exchange`), the userinfo lookup and the
 *     model call all land on `handleQoderAuthRoute` and the model endpoint in
 *     `./mockModelServer`. The SDK model entry alone is not enough: a
 *     `modelConfigs.customModels` key beside the `providers` entry makes Qoder
 *     drop the provider as a catalog conflict.
 *
 * `--config-dir` is the isolation knob, and the worker passes it. The switches
 * below pin the GLOBAL site, skip the developer's rc files, keep the credential
 * store out of the macOS keychain and disable Alibaba HTTPDNS.
 */
function qoderEnv(runDir: string): Record<string, string> {
  // The SDK auth payload is a file the CLI reads once at startup. It is NOT a
  // secret: the mock accepts any token, and no real account is reached.
  const authPayloadPath = join(runDir, 'qoder-sdk-auth.json')
  writeJSON(authPayloadPath, { type: 'accessToken', accessToken: MODEL_KEY })
  return {
    QODER_SITE: 'GLOBAL',
    // Pin the environment to `prod` with no region suffix. `QODER_ENV` is
    // `"<env>-<region>"`, and a region other than `auto` elects endpoints for
    // `securityInference` alone -- the openapi call then falls through to the
    // real `openapi.qoder.sh` and the token exchange never reaches the mock.
    QODER_ENV: 'prod',
    QODER_NO_RC: '1',
    QODER_FORCE_FILE_STORAGE: '1',
    QODER_HTTPDNS: '0',
    // The mocked-auth recipe. See the block comment above.
    QODER_AGENT_SDK_ENTRYPOINT: '1',
    QODER_SDK_AUTH_PAYLOAD_FILE: authPayloadPath,
    // In SDK mode a custom provider is gated behind this switch; without it
    // `isCustomProviderEntryEnabled()` is false and the mockprov model is never
    // registered, so the model call falls through to the real Qoder API.
    QODER_SDK_CUSTOM_BASE_URL_BYOK: '1',
    // EMPTY, which Qoder reads as unset, so a developer's own token cannot
    // reach the E2E Qoder. The mock serves authentication, so a real PAT is
    // both unnecessary and refused.
    QODER_PERSONAL_ACCESS_TOKEN: '',
    QODER_SESSION_ID: '',
    QODER_CLI: '',
    QODERCN_CLI: '',
    QODER_REMOTE_CHILD: '',
  }
}

/**
 * Pre-seeds Qoder's endpoint-election caches so every elected purpose (center,
 * inference, securityInference, openapi) points at the mock.
 *
 * Without this the CLI elects the real `*.qoder.sh` endpoints on a cold start:
 * the token exchange and the model call never reach the mock, and the headless
 * auth gate fails with "Not logged in · Please run /login". The v1 cache is the
 * seam the auth path reads (an auth run without it dies with
 * `access_token_invalid` before any exchange), and the v2 cache is what the
 * election refresh writes. Both carry a 24h TTL, so the fixture writes a fresh
 * `updatedAt` on every run.
 */
function qoderEndpointCaches(qoderHome: string, origin: string): void {
  const cacheDir = join(qoderHome, '.cache')
  mkdirSync(cacheDir, { recursive: true })
  const now = Date.now()
  const purposes = ['center', 'inference', 'securityInference', 'openapi']
  const endpointSets = Object.fromEntries(
    purposes.map(purpose => [purpose, { candidates: [origin], selected: origin }]),
  )
  const v2 = { version: 2, entries: { prod: { endpointSets, updatedAt: now } } }
  writeJSON(join(cacheDir, 'qoder-client-endpoint-cache.json'), v2)
  writeJSON(join(cacheDir, 'qoder-client-endpoint-cache-public.json'), v2)
  const v1 = {
    version: 1,
    entries: {
      prod: {
        endpoint: origin,
        inferEndpoints: [origin],
        securityEndpoint: origin,
        securityEndpoints: [origin],
        centerEndpoint: origin,
        centerEndpoints: [origin],
        openapiEndpoint: origin,
        openapiEndpoints: [origin],
        updatedAt: now,
      },
    },
  }
  writeJSON(join(cacheDir, 'endpoint-cache.json'), v1)
}

/**
 * Re-materializes the SDK auth payload file before one Qoder launch.
 *
 * `qodercli` DELETES `QODER_SDK_AUTH_PAYLOAD_FILE` after it reads the one-shot
 * credential, so a second agent from the same environment starts with the file
 * gone and dies with `access_token_invalid` (exit 41). The fixtures call this
 * before each agent they open; the file is a fixture artifact, not a secret.
 */
export function refreshQoderSdkAuthPayload(agentEnv: Record<string, string>): void {
  const path = agentEnv.QODER_SDK_AUTH_PAYLOAD_FILE
  if (path)
    writeJSON(path, { type: 'accessToken', accessToken: MODEL_KEY })
}

/**
 * Junie's custom model profile, as one `*.json` file of a folder that
 * `--model-location` or a `model-locations` config entry names.
 *
 * The FILE NAME is the profile identifier: `mock-model.json` is the model
 * `custom:mock-model` (`JUNIE_MOCK_MODEL`). The `id` field is the model name
 * that the ENDPOINT receives, not the profile identifier. `baseUrl` is the
 * FULL endpoint because Junie never appends `/chat/completions`.
 */
function junieModelProfile(fullEndpoint: string): Record<string, unknown> {
  return {
    id: MOCK_MODELS.junie,
    displayName: 'Mock Model',
    providerName: 'Mock',
    baseUrl: fullEndpoint,
    apiKey: MODEL_KEY,
    apiType: 'OpenAICompletion',
    maxContextLength: 200000,
  }
}

/**
 * Junie's isolated store, its install root and the one configuration file that
 * points it at the mock's model profile.
 *
 * `JUNIE_HOME` holds the sessions and the secrets. It is the isolated home's
 * `.junie`, so a developer's own store is never read or written.
 *
 * `JUNIE_DATA` is the install root that holds `versions/`. The managed `junie`
 * shim resolves the version to run from `$JUNIE_DATA/versions` (or
 * `$JUNIE_DATA/current`), and the isolated HOME has no install. The run points
 * it at the DEVELOPER'S install root: the versions are the programs the test
 * must run, and nothing under them is session state.
 *
 * `JUNIE_CONFIG_LOCATION` names the one config file that states
 * `model-locations`. The worker launches Junie with
 * `--model-default-locations=false`, so neither `$JUNIE_HOME/models` nor
 * `<project>/.junie/models` is scanned; the explicit location is how the
 * environment supplies the mock's profile. Explicit config locations stay
 * enabled under that flag.
 */
function junieEnv(homeDir: string, modelsDir: string, realHomeDir: string | undefined): Record<string, string> {
  const junieHome = join(homeDir, '.junie')
  mkdirSync(junieHome, { recursive: true })
  const configPath = join(modelsDir, 'config.json')
  writeJSON(configPath, { 'model-locations': [modelsDir] })
  const env: Record<string, string> = {
    JUNIE_HOME: junieHome,
    JUNIE_CONFIG_LOCATION: configPath,
  }
  const installRoot = realHomeDir === undefined ? undefined : join(realHomeDir, '.local', 'share', 'junie')
  if (installRoot !== undefined)
    env.JUNIE_DATA = installRoot
  return env
}

/** fast-agent's model routing: `gpt-4o` goes to this `openai` block. */
function fastAgentConfig(baseURL: string): string {
  return `default_model: "${FAST_AGENT_MOCK_MODEL}"
openai:
  api_key: "${MODEL_KEY}"
  base_url: "${baseURL}"
`
}

/**
 * The PATH entry that shadows the system credential-store CLIs with stubs that
 * refuse every call, and launches Junie through a wrapper that keeps those
 * stubs first on its PATH. Written under `shimsDir`.
 *
 * Junie's secure-storage layer decides between the system keyring and its own
 * file store by running `which security` and then a keychain round-trip
 * (`__junie_availability_check_`). The round-trip touches the DEVELOPER'S
 * keychain, which a test must never do. A stub `security` that fails every call
 * makes the round-trip fail, so Junie keeps every secret in its file store
 * under the isolated JUNIE_HOME. The probe is the same shape on Linux
 * (`secret-tool`). Windows needs none: its store is the Win32 credential
 * manager, which no PATH entry shadows.
 *
 * A stub on the launch PATH alone does not reach Junie: the agent starts
 * through the user's shell, and the shell's startup files rebuild PATH (this
 * machine's `~/.zshenv` does, and drops every added entry). The `junie` wrapper
 * this writes re-prepends the stub directory AFTER those startup files and then
 * execs the real CLI, so the stub is first on the PATH that Junie's own probe
 * resolves. The worker launches the wrapper because `shimsDir` is first on its
 * PATH; the skip check runs in the Playwright process and finds the real CLI.
 */
function credentialStoreShimEnv(shimsDir: string, searchPath: string | undefined): Record<string, string> {
  if (process.platform === 'win32')
    return {}
  const stubbed = process.platform === 'darwin' ? ['security'] : ['secret-tool']
  for (const name of stubbed) {
    const stub = join(shimsDir, name)
    writeFileSync(stub, `#!/bin/sh\necho "leapmux e2e: refusing to touch the system credential store" >&2\nexit 1\n`, { mode: 0o755 })
  }
  const realJunie = findBinary('junie')
  if (realJunie !== null) {
    writeFileSync(join(shimsDir, 'junie'), `#!/bin/sh\nexport PATH=${posixQuote(shimsDir)}:"$PATH"\nexec ${posixQuote(realJunie)} "$@"\n`, { mode: 0o755 })
  }
  return { PATH: [shimsDir, searchPath ?? process.env.PATH ?? ''].join(delimiter) }
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

function codexConfig(baseURL: string): string {
  return `model_provider = "leapmux-e2e"
check_for_update_on_startup = false
disable_response_storage = true

# Codex consolidates its own memories in a background turn, against a model of
# its own choice and with no user prompt. That turn would reach the mock
# endpoint outside any test's script.
[memories]
generate_memories = false
use_memories = false
dedicated_tools = false

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

function openCodeFamilyConfig(baseURL: string): Record<string, unknown> {
  return {
    formatter: false,
    lsp: false,
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`,
    provider: {
      [MOCK_PROVIDER_IDS.openCode]: openCodeFamilyProvider(baseURL, [openCodeFamilyModel(MOCK_MODELS.zai, 'GLM-5.3 Flash')]),
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
 * `variantOptions` gives the request options that each variant sets. The OpenCode
 * family merges a configured variant over its built-in one, so an empty object
 * sends no reasoning field at all.
 */
function openCodeFamilyModel(id: string, name: string, variantOptions: (variant: string) => Record<string, unknown> = () => ({})): Record<string, unknown> {
  return {
    id,
    name,
    attachment: false,
    reasoning: true,
    temperature: false,
    tool_call: true,
    release_date: '2026-01-01',
    limit: { context: 128_000, output: 16_000 },
    cost: { input: 0, output: 0 },
    options: {},
    variants: Object.fromEntries(REASONING_VARIANTS.map(variant => [variant, variantOptions(variant)])),
  }
}

/**
 * MiMo Code's configuration: the OpenCode family's provider block, which MiMo reads
 * unchanged, and the switches for every model request that no test scripts.
 *
 * - A second model, so that a spec can switch models and read the switch off
 *   the next request.
 * - Variants that send `reasoning_effort`, so that a spec can read the effort
 *   off the next request too.
 * - `enabled_providers` hides MiMo's own built-in providers, so the model menu
 *   holds the mock alone.
 * - `agent.title.disable` stops the title request that otherwise runs beside the
 *   first turn.
 * - `retry` makes a failed request fail once. A retry would consume the next
 *   scripted step.
 * - `snapshot` and `share` keep MiMo from writing git snapshots and from
 *   offering a public link.
 */
function mimoCodeConfig(baseURL: string): Record<string, unknown> {
  const noRetry = { mode: 'bounded', maxRetries: 0 }
  const reasoningEffort = (variant: string) => ({ reasoningEffort: variant })
  return {
    ...openCodeFamilyConfig(baseURL),
    provider: {
      [MOCK_PROVIDER_IDS.openCode]: openCodeFamilyProvider(baseURL, [
        openCodeFamilyModel(MOCK_MODELS.zai, 'GLM-5.3 Flash', reasoningEffort),
        openCodeFamilyModel(MOCK_MODELS.pi, 'GLM-5.3', reasoningEffort),
      ]),
    },
    enabled_providers: [MOCK_PROVIDER_IDS.openCode],
    agent: { title: { disable: true } },
    autoupdate: false,
    share: 'disabled',
    snapshot: false,
    retry: { request: noRetry, stream: noRetry, network: noRetry, server: noRetry, rateLimit: noRetry, unknown: noRetry },
  }
}

function piModels(baseURL: string): Record<string, unknown> {
  return {
    providers: {
      [PI_PROVIDER_ID]: {
        baseUrl: baseURL,
        api: 'openai-completions',
        apiKey: MODEL_KEY,
        models: [{
          id: MOCK_MODELS.pi,
          name: 'GLM-5.3',
          reasoning: true,
          input: ['text'],
          contextWindow: 128_000,
          maxTokens: 16_000,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        }],
      },
    },
  }
}

function ohMyPiModels(baseURL: string): Record<string, unknown> {
  return {
    providers: {
      [MOCK_PROVIDER_IDS.ohMyPi]: {
        baseUrl: baseURL,
        // omp reads `apiKey` as the NAME of a variable first, and uses the value
        // literally only when no variable has that name.
        apiKey: 'LEAPMUX_E2E_MODEL_API_KEY',
        api: 'openai-completions',
        models: [{
          id: MOCK_MODELS.ohMyPi,
          name: 'GLM-5.3',
          // A reasoning model, so the thinking-level axis exists.
          reasoning: true,
          input: ['text', 'image'],
          contextWindow: 128_000,
          maxTokens: 16_000,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        }],
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
    compaction: { asyncEnabled: false },
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
    'pi-mcp-adapter',
  ].map(packageName => join(modules, packageName))
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
model = "${MOCK_MODELS.deepseek}"

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
 * Grok Build's configuration: one model of its own, pinned to the mock.
 *
 * `remote_fetch = false` keeps Grok from fetching its remote settings and model
 * catalog, and the two built-in models are hidden so the catalog LeapMux shows
 * holds the mock alone. The session title is pinned to the same model, because
 * Grok otherwise asks its built-in auxiliary model, which the mock does not list.
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
function qwenSettings(baseURL: string): Record<string, unknown> {
  return {
    $version: 4,
    security: { auth: { selectedType: QWEN_AUTH_TYPE } },
    model: { name: MOCK_MODELS.qwen },
    modelProviders: {
      [QWEN_AUTH_TYPE]: [{
        id: MOCK_MODELS.qwen,
        name: 'Qwen E2E',
        baseUrl: baseURL,
        envKey: 'LEAPMUX_E2E_MODEL_API_KEY',
        capabilities: { reasoning: { thinking: true, efforts: ['low', 'medium', 'high'], defaultEffort: 'high', disableField: 'reasoning_effort' } },
        generationConfig: { contextWindowSize: 128_000 },
      }],
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
            personalModelIds: [modelID],
            modelOrder: [modelID],
            visibility: 'visible',
          },
        }],
      },
      modelConfigRules: {
        providerModelRules: [{ providerId: providerID, modelId: modelID, config: { enabled: true } }],
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
 * Cline's `providers.json`, in the shape that `cline auth` writes.
 *
 * Cline checks the file against its schema and reads a file that fails the check
 * as no settings at all: an entry without `updatedAt` sent the agent to the
 * provider's built-in endpoint, which the refusing proxy then turned away.
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
async function prepareLettaBackend(lettaHome: string, lettaBackendDir: string, baseURL: string): Promise<void> {
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
  const env = { ...base, ...lettaEnv(lettaHome, lettaBackendDir), HOME: lettaHome, PATH: agentSearchPath(process.env.PATH), NO_PROXY: '127.0.0.1,localhost', no_proxy: '127.0.0.1,localhost' }
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
