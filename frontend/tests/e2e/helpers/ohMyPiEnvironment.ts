import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

export interface OhMyPiEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which refuses each proxied request and records its host. */
  origin: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  /** The private omp profile. */
  profile: string
  /** The provider of `models.yml`, which qualifies each model as `<provider>/<id>`. */
  providerID: string
  modelID: string
  alternateModelID: string
  mcpEchoServer: McpProbeServer
}

/**
 * The agent directory of omp's profile, which holds its configuration and sessions.
 *
 * omp and Pi both read PI_CODING_AGENT_DIR and PI_CODING_AGENT_SESSION_DIR.
 * Pi's fixture values would therefore select Pi's directories for omp.
 * OMP_PROFILE selects ~/.omp/profiles/<name>/agent and takes precedence over PI_CODING_AGENT_DIR.
 * It also takes precedence over PI_PROFILE, so an inherited value cannot select another profile.
 */
export function ohMyPiAgentDirectory(homeDir: string, profile: string): string {
  return join(homeDir, '.omp', 'profiles', profile, 'agent')
}

/** Point omp at the mock in its own profile, and stop every request that no test scripts. */
export function createOhMyPiEnvironment(options: OhMyPiEnvironmentOptions): Record<string, string> {
  const agentDir = ohMyPiAgentDirectory(options.homeDir, options.profile)
  mkdirSync(agentDir, { recursive: true })
  // YAML is omp's format, and JSON is valid YAML, so the one writer serves.
  writePrivateJSON(join(agentDir, 'models.yml'), ohMyPiModels(options))
  writePrivateJSON(join(agentDir, 'config.yml'), ohMyPiConfig(options))
  writePrivateJSON(join(agentDir, 'mcp.json'), {
    mcpServers: { [options.mcpEchoServer.name]: { type: 'stdio', command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args] } },
  })
  return {
    OMP_PROFILE: options.profile,
    // omp routes every request that is not to a loopback or private address
    // through its own proxy variable, and the model endpoint is loopback. A
    // request to anywhere else -- a model catalog of a provider that a later omp
    // adds, which `disabledProviders` below does not list -- then reaches the
    // refusing mock, which records the host. No other provider reads the
    // variable.
    PI_PROXY: options.origin,
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
  }
}

function ohMyPiModels(options: OhMyPiEnvironmentOptions): Record<string, unknown> {
  const primary = {
    id: options.modelID,
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
      [options.providerID]: {
        baseUrl: options.baseURL,
        // omp first treats apiKey as an environment variable identifier.
        // It uses the literal key only when that variable does not exist.
        apiKey: 'LEAPMUX_E2E_MODEL_API_KEY',
        api: 'openai-completions',
        models: [primary, { ...primary, id: options.alternateModelID, name: 'GLM-5.3 Alternate' }],
      },
    },
  }
}

/**
 * Every provider omp 18.2.11 bundles. omp has no offline switch and no wildcard, and
 * each provider it keeps enabled refreshes a catalog or probes a local server on its
 * own. A provider a later omp adds meets the refusing proxy instead.
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
function ohMyPiConfig(options: OhMyPiEnvironmentOptions): Record<string, unknown> {
  const model = `${options.providerID}/${options.modelID}`
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
