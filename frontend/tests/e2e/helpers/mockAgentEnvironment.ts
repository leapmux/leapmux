import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

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
  /** Goose, Kilo, OpenCode, and ZCode. */
  zai: 'glm-5.3-flash',
  /** Pi. */
  pi: 'glm-5.3',
  /** Reasonix. */
  deepseek: 'deepseek-flash',
} as const

/**
 * The provider identifiers that qualify a model id.
 *
 * OpenCode, Kilo and ZCode each address a model as `<provider>/<model>`, so a
 * fixture that pins a model needs the same identifier this file registers.
 */
export const MOCK_PROVIDER_IDS = {
  /**
   * The OpenCode and Kilo provider block below.
   *
   * The identifier must belong to no public catalog. OpenCode and Kilo merge a
   * configured provider onto the built-in entry of the same id, and Kilo's
   * gateway then keeps its own base URL: the agent answered from the real
   * endpoint and the mock saw no request at all.
   */
  openCode: 'leapmux-e2e',
  /** The ZCode personal provider block below. */
  zcode: 'personal:leapmux-e2e',
} as const

/** Pi addresses a model through a named provider in its own `models.json`. */
const PI_PROVIDER_ID = 'zai'

/** Reasonix names its provider block, and `default_model` qualifies with it. */
const REASONIX_PROVIDER_ID = 'deepseek'

/** Every pinned identifier, for the mock endpoint's catalog route. */
export const MOCK_MODEL_IDS: readonly string[] = [...new Set(Object.values(MOCK_MODELS))]

export interface MockAgentEnvironment {
  env: Record<string, string>
  homeDir: string
  piAgentDir: string
}

interface MockAgentEnvironmentOptions {
  realHomeDir?: string
}

/** Write isolated agent configuration and return its process environment. */
export function createMockAgentEnvironment(
  runDir: string,
  serverURL: string,
  options: MockAgentEnvironmentOptions = {},
): MockAgentEnvironment {
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
  for (const directory of [codexHome, piAgentDir, reasonixHome, zcodeDir, copilotHome, cursorConfigDir])
    mkdirSync(directory, { recursive: true })

  writeFileSync(join(codexHome, 'config.toml'), codexConfig(openAIBaseURL), { mode: 0o600 })
  writeJSON(join(piAgentDir, 'models.json'), piModels(openAIBaseURL))
  writeJSON(join(piAgentDir, 'settings.json'), {
    defaultProvider: PI_PROVIDER_ID,
    defaultModel: MOCK_MODELS.pi,
    packages: piPackagePaths(options.realHomeDir),
  })
  writeFileSync(join(reasonixHome, 'config.toml'), reasonixConfig(openAIBaseURL), { mode: 0o600 })
  const zcodeConfigPath = join(zcodeDir, 'config.json')
  const zcodePersonalConfigPath = join(zcodeDir, 'provider_config.json')
  writeJSON(zcodeConfigPath, zcodeLegacyConfig(openAIBaseURL))
  writeJSON(zcodePersonalConfigPath, zcodePersonalConfig(openAIBaseURL))

  const openCodeConfig = JSON.stringify(openCodeFamilyConfig(openAIBaseURL))
  return {
    homeDir,
    piAgentDir,
    env: {
      HOME: homeDir,
      USERPROFILE: homeDir,
      LEAPMUX_E2E_MODEL_API_KEY: MODEL_KEY,
      NO_PROXY: '127.0.0.1,localhost',
      no_proxy: '127.0.0.1,localhost',

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

      GOOSE_PROVIDER: 'openai',
      GOOSE_MODEL: MOCK_MODELS.zai,

      REASONIX_HOME: reasonixHome,

      PI_CODING_AGENT_DIR: piAgentDir,
      PI_CODING_AGENT_SESSION_DIR: join(runDir, 'pi-sessions'),

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

      ZCODE_MODEL_TELEMETRY_ENABLED: 'false',
      ZCODE_PERSONAL_PROVIDER_CONFIG_FILE: zcodePersonalConfigPath,
      ZCODE_STORAGE_DIR: join(runDir, 'zcode-storage'),
    },
  }
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
      [MOCK_PROVIDER_IDS.openCode]: {
        name: 'LeapMux E2E',
        id: MOCK_PROVIDER_IDS.openCode,
        env: [],
        npm: '@ai-sdk/openai-compatible',
        models: {
          [MOCK_MODELS.zai]: {
            id: MOCK_MODELS.zai,
            name: 'GLM-5.3 Flash',
            attachment: false,
            reasoning: true,
            temperature: false,
            tool_call: true,
            release_date: '2026-01-01',
            limit: { context: 128_000, output: 16_000 },
            cost: { input: 0, output: 0 },
            options: {},
            variants: { low: {}, medium: {}, high: {}, xhigh: {}, max: {} },
          },
        },
        options: { apiKey: MODEL_KEY, baseURL },
      },
    },
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

function writeJSON(path: string, value: unknown): void {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}
