import { mkdirSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { requireLoopbackHttpURL } from './agentEnvironmentInputs'
import { createAmpEnvironment } from './ampEnvironment'
import { agentSearchPathEnv } from './binaryOnPath'
import { createClaudeEnvironment } from './claudeEnvironment'
import { createClineEnvironment } from './clineEnvironment'
import { createCodebuddyEnvironment } from './codebuddyEnvironment'
import { createCodewhaleEnvironment } from './codewhaleEnvironment'
import { createCodexEnvironment } from './codexEnvironment'
import { createCommandCodeEnvironment } from './commandCodeEnvironment'
import { createCopilotEnvironment } from './copilotEnvironment'
import { createCursorEnvironment } from './cursorEnvironment'
import { createDeepseekHarnessEnvironment } from './deepseekHarnessEnvironment'
import { createDiracEnvironment } from './diracEnvironment'
import { createDroidEnvironment } from './droidEnvironment'
import { createFastAgentEnvironment } from './fastAgentEnvironment'
import { createGeminiEnvironment } from './geminiEnvironment'
import { createGooseEnvironment } from './gooseEnvironment'
import { createGrokEnvironment } from './grokEnvironment'
import { createJunieEnvironment } from './junieEnvironment'
import { createKimiEnvironment } from './kimiEnvironment'
import { createKiroEnvironment } from './kiroEnvironment'
import { createLettaEnvironment } from './lettaEnvironment'
import { writeMcpEchoServer } from './mcpEchoServer'
import { createMimoEnvironment } from './mimoEnvironment'
import { createMuseEnvironment } from './museEnvironment'
import { createOhMyPiEnvironment, ohMyPiAgentDirectory } from './ohMyPiEnvironment'
import { createOpenCodeEnvironment } from './openCodeEnvironment'
import { createPiEnvironment, piAgentDirectory } from './piEnvironment'
import { createQoderEnvironment } from './qoderEnvironment'
import { createQwenEnvironment, QWEN_AUTH_TYPE } from './qwenEnvironment'
import { createReasonixEnvironment } from './reasonixEnvironment'
import { quotePosixShellArgument } from './shellArguments'
import { createZcodeEnvironment } from './zcodeEnvironment'

/*
 * The isolated agent environment of an E2E run: the catalog of mock identities that
 * the fixtures and the specs pin, and `createMockAgentEnvironment`, which writes each
 * provider's private configuration through the provider's own module and returns the
 * process environment of every agent.
 */

/** The default Command Code model, which offers the low and high efforts. */
export const COMMAND_CODE_MODEL_ID = 'leapmux-e2e/command-code-e2e'
/** The second Command Code model, which declares no reasoning and so offers no effort. */
export const COMMAND_CODE_ALT_MODEL_ID = 'leapmux-e2e/command-code-e2e-alt'
/** The third Command Code model, which offers the low, medium, and high efforts. */
export const COMMAND_CODE_REASONING_MODEL_ID = 'leapmux-e2e/command-code-e2e-reasoning'
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
  /** Muse Code uses the native Responses provider. */
  muse: 'muse-spark-1.2',
} as const

/**
 * Keep the provider identifiers that qualify native model IDs.
 *
 * OpenCode, Kilo, MiMo Code, and ZCode address each model as <provider>/<model>.
 * The selected identifier must match the provider registration of each provider module.
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
  /** The ZCode personal provider block of ./zcodeEnvironment. */
  zcode: 'personal:leapmux-e2e',
  /** The Kimi Code provider table of ./kimiEnvironment, which also qualifies its model aliases. */
  kimi: 'leapmux-e2e',
  /** The Oh My Pi `models.yml` provider of ./ohMyPiEnvironment, which qualifies its model as `<provider>/<id>`. */
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
 * Three aliases, so a test can switch the model:
 * - `thinking` thinks and takes an effort. It is the default model.
 * - `plain` does neither, so a switch to it also changes the effort axis.
 * - `alternateThinking` thinks and takes the same effort ladder as `thinking`, but starts at
 *   another default level, so a switch that keeps a level differs from a switch
 *   that takes the default.
 */
export const KIMI_MOCK_MODELS = {
  thinking: `${MOCK_PROVIDER_IDS.kimi}/${MOCK_MODELS.zai}`,
  plain: `${MOCK_PROVIDER_IDS.kimi}/${MOCK_MODELS.pi}`,
  alternateThinking: `${MOCK_PROVIDER_IDS.kimi}/${MOCK_MODELS.deepseek}`,
} as const

/** The private omp profile. See `ohMyPiAgentDirectory` for why omp needs one. */
export const OH_MY_PI_PROFILE = 'leapmux-e2e'

/**
 * The hosts that every client reaches directly, past the refusing proxy: the
 * loopback addresses where the mock listens.
 */
const LOOPBACK_NO_PROXY = '127.0.0.1,localhost,::1'

/**
 * The model of the OpenCode family that does not reason, so it declares no reasoning variant and offers no effort.
 * OpenCode, Kilo, and MiMo Code each list it beside the two reasoning models.
 */
export const OPENCODE_FAMILY_PLAIN_MODEL_WIRE_ID = 'glm-5.3-plain'
export const OPENCODE_FAMILY_PLAIN_MODEL_ID = `${MOCK_PROVIDER_IDS.openCode}/${OPENCODE_FAMILY_PLAIN_MODEL_WIRE_ID}`

/** The second Reasonix provider, which a spec switches to. */
export const REASONIX_ALT_PROVIDER_ID = 'leapmux-e2e-alt'
export const REASONIX_ALT_MODEL_ID = `${REASONIX_ALT_PROVIDER_ID}/${MOCK_MODELS.pi}`
/** The third Reasonix provider, which states no reasoning protocol and no effort. */
export const REASONIX_PLAIN_PROVIDER_ID = 'leapmux-e2e-plain'
export const REASONIX_PLAIN_MODEL_ID = `${REASONIX_PLAIN_PROVIDER_ID}/${MOCK_MODELS.zai}`

/**
 * A second model of Dirac's OpenAI provider, so a spec can switch models. Dirac 0.5.17 lists it beside the configured
 * model; the mock environment configures no such model.
 */
export const DIRAC_ALT_MODEL_ID = 'gpt-6-astra'

/**
 * The model that Codex runs for a thread that states no model, which is what the Default model entry resolves to. It
 * differs from the pinned model of the Codex fixture (`MOCK_MODELS.openai`), so a resolution to it cannot pass for a
 * kept model.
 */
export const CODEX_NATIVE_DEFAULT_MODEL = 'gpt-5.6-terra'

/** The built-in Codewhale model whose route accepts image input. */
export const CODEWHALE_VISION_MODEL_ID = 'deepseek-v4-flash-vision-exp'
/** The model id Qwen reports for the model its configuration pins. */
export const QWEN_MODEL_ID = `${MOCK_MODELS.qwen}(${QWEN_AUTH_TYPE})`
export const QWEN_ALT_MODEL_WIRE_ID = 'qwen-e2e-alt'
export const QWEN_ALT_MODEL_ID = `${QWEN_ALT_MODEL_WIRE_ID}(${QWEN_AUTH_TYPE})`
/** The Qwen model that states no reasoning capability, so it offers no effort. */
export const QWEN_PLAIN_MODEL_WIRE_ID = 'qwen-e2e-plain'
export const QWEN_PLAIN_MODEL_ID = `${QWEN_PLAIN_MODEL_WIRE_ID}(${QWEN_AUTH_TYPE})`
/** The second Grok model, which offers no reasoning effort. */
export const GROK_ALT_MODEL_ID = 'grok-e2e-alt'
/** The third Grok model, which offers the effort ladder of the default model and starts at another level. */
export const GROK_REASONING_MODEL_ID = 'grok-e2e-reasoning'
/** The Pi model that does not reason, so Pi offers it only Auto and Off. */
export const PI_PLAIN_MODEL_ID = 'glm-5.3-plain'
/**
 * The ZCode model whose reasoning is off. ZCode then offers it only the thought levels Enabled and Disabled, so it lacks
 * every level of the two GLM models.
 */
export const ZCODE_PLAIN_MODEL_WIRE_ID = 'zcode-e2e-plain'
export const ZCODE_PLAIN_MODEL_ID = `${MOCK_PROVIDER_IDS.zcode}/${ZCODE_PLAIN_MODEL_WIRE_ID}`
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
 * Supply Kiro's isolated bearer key.
 *
 * Kiro validates only the local API key prefix, ksk_.
 * The mock refuses any other bearer, including credentials from stores that HOME cannot isolate, such as the macOS keychain.
 * The refusal stays visible in the request log.
 */
export const KIRO_E2E_API_KEY = 'ksk_leapmux_e2e'

/**
 * Cline's provider in the isolated settings: the generic OpenAI Chat Completions
 * client, which takes any base URL and any model id. It reads `reasoning_content`
 * as thinking, which the mock sends.
 */
export const CLINE_PROVIDER_ID = 'deepseek'

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
  const origin = requireLoopbackHttpURL(serverURL, 'The mock model server URL', { originOnly: true }).origin
  const temporaryDirectory = resolve(runDir, 'tmp')
  mkdirSync(temporaryDirectory, { recursive: true, mode: 0o700 })
  const temporaryEnv = { TMPDIR: temporaryDirectory, TEMP: temporaryDirectory, TMP: temporaryDirectory }
  const openAIBaseURL = `${origin}/v1`
  const homeDir = join(runDir, 'agent-home')
  const cliShimsDir = join(runDir, 'cli-shims')
  const xdgDirectories = xdgBaseDirectories(homeDir)
  // A spec that asserts a private path needs the directory before an agent writes to it,
  // so the environment creates each directory that one of its variables points at. Each
  // provider module creates the directories of its own variables.
  for (const directory of [homeDir, ...Object.values(xdgDirectories), cliShimsDir])
    mkdirSync(directory, { recursive: true })

  // macOS zsh's system login profile rebuilds PATH. Reapply the private shims
  // after that profile, so Junie's `security` probe cannot reach the keychain.
  const privatePath = `export PATH=${quotePosixShellArgument(cliShimsDir)}:"$PATH"\n`
  for (const file of ['.zshrc', '.zlogin'])
    writeFileSync(join(homeDir, file), privatePath, { mode: 0o600 })

  // Each configuration registers a server under the name that the server states, so a tool call reaches it
  // under that name.
  const mcpEchoServer = writeMcpEchoServer(runDir)
  const mcpServers = [{ name: mcpEchoServer.name, command: mcpEchoServer.command, args: [...mcpEchoServer.args] }]
  const openCodeFamily = {
    baseURL: openAIBaseURL,
    modelKey: MODEL_KEY,
    providerID: MOCK_PROVIDER_IDS.openCode,
    models: [
      { id: MOCK_MODELS.zai, name: 'GLM-5.3 Flash' },
      { id: MOCK_MODELS.pi, name: 'GLM-5.3' },
      { id: OPENCODE_FAMILY_PLAIN_MODEL_WIRE_ID, name: 'GLM-5.3 Plain', reasoning: false },
    ],
    mcpEchoServer,
  }
  const searchPathEnv = agentSearchPathEnv()
  return {
    homeDir,
    piAgentDir: piAgentDirectory(homeDir),
    ohMyPiAgentDir: ohMyPiAgentDirectory(homeDir, OH_MY_PI_PROFILE),
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
      // Amp reads its settings, its history and its cache under these, and a value in
      // the developer's own environment would otherwise point it at theirs. Each one
      // is the directory the isolated HOME implies anyway, so no provider that
      // derives the same path from HOME sees a change.
      ...xdgDirectories,
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
      // The generic OpenAI credentials, which Goose's OpenAI provider reads.
      OPENAI_API_KEY: MODEL_KEY,
      OPENAI_BASE_URL: openAIBaseURL,

      ...createClaudeEnvironment({ homeDir, modelURL: origin, modelKey: MODEL_KEY }),
      ...createCodexEnvironment({ homeDir, baseURL: openAIBaseURL, defaultModelID: CODEX_NATIVE_DEFAULT_MODEL }),
      ...createOpenCodeEnvironment(openCodeFamily),
      ...createGooseEnvironment({ homeDir, modelID: MOCK_MODELS.goose }),
      ...createReasonixEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.deepseek, alternateProviderID: REASONIX_ALT_PROVIDER_ID, alternateModelID: MOCK_MODELS.pi, plainProviderID: REASONIX_PLAIN_PROVIDER_ID, plainModelID: MOCK_MODELS.zai }),
      ...createPiEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.pi, flashModelID: MOCK_MODELS.zai, plainModelID: PI_PLAIN_MODEL_ID, mcpEchoServer, realHomeDir: options.realHomeDir, shimsDirectory: cliShimsDir }),
      ...createOhMyPiEnvironment({ homeDir, origin, baseURL: openAIBaseURL, profile: OH_MY_PI_PROFILE, providerID: MOCK_PROVIDER_IDS.ohMyPi, modelID: MOCK_MODELS.ohMyPi, alternateModelID: OH_MY_PI_ALT_MODEL_WIRE_ID, mcpEchoServer }),
      ...createCursorEnvironment({ homeDir, origin, modelKey: MODEL_KEY }),
      ...createCopilotEnvironment({ homeDir, origin, modelKey: MODEL_KEY, githubToken: MOCK_COPILOT_GITHUB_TOKEN }),
      ...createGrokEnvironment({ runDirectory: runDir, homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.grok, alternateModelID: GROK_ALT_MODEL_ID, reasoningModelID: GROK_REASONING_MODEL_ID }),
      ...createQwenEnvironment({ homeDir, baseURL: openAIBaseURL, modelID: MOCK_MODELS.qwen, alternateModelID: QWEN_ALT_MODEL_WIRE_ID, plainModelID: QWEN_PLAIN_MODEL_WIRE_ID, mcpEchoServer }),
      ...createKiroEnvironment({ runDirectory: runDir, homeDir, origin, apiKey: KIRO_E2E_API_KEY }),
      ...createZcodeEnvironment({ runDirectory: runDir, homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, providerID: MOCK_PROVIDER_IDS.zcode, modelID: MOCK_MODELS.zai, alternateModelID: MOCK_MODELS.pi, plainModelID: ZCODE_PLAIN_MODEL_WIRE_ID }),
      ...createCodewhaleEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.deepseek, visionModelID: CODEWHALE_VISION_MODEL_ID, mcpEchoServer }),
      ...createKimiEnvironment({ homeDir, baseURL: openAIBaseURL, providerID: MOCK_PROVIDER_IDS.kimi, thinking: { alias: KIMI_MOCK_MODELS.thinking, model: MOCK_MODELS.zai }, plain: { alias: KIMI_MOCK_MODELS.plain, model: MOCK_MODELS.pi }, alternateThinking: { alias: KIMI_MOCK_MODELS.alternateThinking, model: MOCK_MODELS.deepseek }, mcpEchoServer }),
      ...createMimoEnvironment({ ...openCodeFamily, runDirectory: runDir }),
      ...createAmpEnvironment({ origin, modelKey: MODEL_KEY }),
      ...createClineEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, providerID: CLINE_PROVIDER_ID, modelID: MOCK_MODELS.cline, mcpEchoServer }),
      ...createDroidEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, primary: { handle: DROID_MOCK_MODEL_IDS.primary, model: MOCK_MODELS.droid }, alternate: { handle: DROID_MOCK_MODEL_IDS.alternate, model: MOCK_MODELS.droidAlt } }),
      ...createLettaEnvironment({ runDirectory: runDir, homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, temporaryEnv }),
      ...createCodebuddyEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.deepseek, alternateModelID: CODEBUDDY_ALT_MODEL_WIRE_ID, startModel: CODEBUDDY_MODEL_ID }),
      ...createQoderEnvironment({ homeDir, shimsDirectory: cliShimsDir, origin, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.deepseek, alternateModelID: MOCK_MODELS.qoder }),
      ...createJunieEnvironment({ runDirectory: runDir, homeDir, shimsDirectory: cliShimsDir, origin, modelKey: MODEL_KEY, modelID: MOCK_MODELS.junie, childModel: JUNIE_MOCK_MODEL, proxyProvider: JUNIE_PROXY_PROVIDER, realHomeDir: options.realHomeDir }),
      ...createDiracEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.deepseek }),
      ...createFastAgentEnvironment({ homeDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: FAST_AGENT_MOCK_MODEL, zaiModelID: MOCK_MODELS.zai }),
      ...createCommandCodeEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, modelID: COMMAND_CODE_MODEL_ID, alternateModelID: COMMAND_CODE_ALT_MODEL_ID, reasoningModelID: COMMAND_CODE_REASONING_MODEL_ID, mcpServers }),
      ...createDeepseekHarnessEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, mcpServers }),
      ...createMuseEnvironment({ runDirectory: runDir, homeDir, shimsDirectory: cliShimsDir, baseURL: openAIBaseURL, modelKey: MODEL_KEY, modelID: MOCK_MODELS.muse, mcpServers, searchPath: searchPathEnv.PATH ?? process.env.PATH ?? '' }),
      ...createGeminiEnvironment({ runDirectory: runDir, modelURL: origin, modelKey: MODEL_KEY, modelID: GEMINI_MODEL_ID, mcpServers }),
      ...credentialStoreShimEnv(cliShimsDir, searchPathEnv.PATH ?? process.env.PATH),
    },
  }
}

/** The XDG base directories that the isolated HOME implies. */
function xdgBaseDirectories(homeDir: string): Record<'XDG_CONFIG_HOME' | 'XDG_DATA_HOME' | 'XDG_CACHE_HOME' | 'XDG_STATE_HOME', string> {
  return {
    XDG_CONFIG_HOME: join(homeDir, '.config'),
    XDG_DATA_HOME: join(homeDir, '.local', 'share'),
    XDG_CACHE_HOME: join(homeDir, '.cache'),
    XDG_STATE_HOME: join(homeDir, '.local', 'state'),
  }
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
 * The Junie wrapper of ./junieEnvironment also restores it before it starts the installed CLI.
 */
export const CREDENTIAL_STORE_SHIM_LOG = 'credential-store-attempts.log'

function credentialStoreShimEnv(shimsDir: string, searchPath: string | undefined): Record<string, string> {
  if (process.platform === 'win32')
    return {}
  const stubbed = process.platform === 'darwin' ? ['security'] : ['secret-tool']
  const logPath = join(shimsDir, CREDENTIAL_STORE_SHIM_LOG)
  for (const name of stubbed) {
    const stub = join(shimsDir, name)
    writeFileSync(stub, `#!/bin/sh\nprintf '%s\\n' ${quotePosixShellArgument(name)} >> ${quotePosixShellArgument(logPath)}\necho "leapmux e2e: refusing to touch the system credential store" >&2\nexit 1\n`, { mode: 0o755 })
  }
  return { PATH: [shimsDir, searchPath ?? process.env.PATH ?? ''].join(delimiter) }
}
