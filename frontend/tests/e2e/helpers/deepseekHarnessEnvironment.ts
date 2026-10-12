import type { McpServerLaunch } from './agentEnvironmentInputs'
import { mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { requireLoopbackHttpURL, validatedMcpServers } from './agentEnvironmentInputs'

export const DEEPSEEK_HARNESS_PROVIDER_ID = 'deepseek-official'
export const DEEPSEEK_HARNESS_MODEL_ID = `${DEEPSEEK_HARNESS_PROVIDER_ID}/deepseek-flash`
export const DEEPSEEK_HARNESS_ALT_MODEL_ID = `${DEEPSEEK_HARNESS_PROVIDER_ID}/deepseek-v4-pro`
/**
 * The route of the model without reasoning. The DeepSeek provider gives each of its models the effort ladder of the
 * connection, so a model without an effort needs a route of its own: a gateway of the `dsh-llm-pi-ai` plugin, where
 * `reasoningEfforts: false` declares a model that does not reason.
 */
export const DEEPSEEK_HARNESS_PLAIN_PROVIDER_ID = 'leapmux-e2e-plain'
export const DEEPSEEK_HARNESS_PLAIN_MODEL_WIRE_ID = 'deepseek-plain'
export const DEEPSEEK_HARNESS_PLAIN_MODEL_ID = `${DEEPSEEK_HARNESS_PLAIN_PROVIDER_ID}/${DEEPSEEK_HARNESS_PLAIN_MODEL_WIRE_ID}`
/** The context window that the private profile states for each of its models, in tokens. */
export const DEEPSEEK_HARNESS_CONTEXT_WINDOW = 1_000_000

export interface DeepseekHarnessEnvironmentOptions {
  runDirectory: string
  modelURL: string
  modelKey: string
  /**
   * Native default preset for this private test profile.
   * An absent value writes no registry row, so the native standard default applies.
   */
  agentPreset?: 'standard' | 'ptc'
  /** Existing shared MCP server executable and arguments. */
  mcpServers?: readonly McpServerLaunch[]
}

/** Create a private native profile that can reach only the supplied model endpoint. */
export function createDeepseekHarnessEnvironment(options: DeepseekHarnessEnvironmentOptions): Record<string, string> {
  if (!isAbsolute(options.runDirectory))
    throw new Error('The DeepSeek Harness run directory must be absolute.')
  const endpoint = requireLoopbackHttpURL(options.modelURL, 'The DeepSeek Harness model endpoint')
  if (!options.modelKey)
    throw new Error('The DeepSeek Harness mock model key must be present.')
  const home = join(options.runDirectory, 'deepseek-harness-home')
  mkdirSync(home, { recursive: true })
  const rows: unknown[] = [
    { id: 'session-title-llm', disabled: true },
    { id: 'llm-deepseek', config: {
      baseURL: endpoint.href.replace(/\/$/, ''),
      apiKeyEnv: 'DEEPSEEK_API_KEY',
      maxTokens: 4096,
      models: [
        { id: 'deepseek-flash', name: 'Mock Flash', contextWindow: DEEPSEEK_HARNESS_CONTEXT_WINDOW, inputModalities: ['text', 'image'] },
        { id: 'deepseek-v4-pro', name: 'Mock Pro', contextWindow: DEEPSEEK_HARNESS_CONTEXT_WINDOW, inputModalities: ['text', 'image'] },
      ],
    } },
    { id: 'agent-default-model', config: { provider: DEEPSEEK_HARNESS_PROVIDER_ID, model: 'deepseek-flash', reasoningEffort: 'high' } },
    // The `web` profile mounts the pi-ai plugin under this ID with no route. A second mount of the plugin does not
    // compose, so the row configures the mount that the profile holds.
    { id: 'llm-pi-ai', config: { providers: {
      [DEEPSEEK_HARNESS_PLAIN_PROVIDER_ID]: {
        displayName: 'LeapMux E2E Plain',
        apiKeyEnv: 'DEEPSEEK_API_KEY',
        api: 'openai-completions',
        baseURL: `${endpoint.href.replace(/\/$/, '')}/v1`,
        models: [{ id: DEEPSEEK_HARNESS_PLAIN_MODEL_WIRE_ID, name: 'Mock Plain', contextWindow: DEEPSEEK_HARNESS_CONTEXT_WINDOW, reasoningEfforts: false }],
      },
    } } },
  ]
  // The native registry reads its default preset from this row. An absent selection keeps the shipped default.
  if (options.agentPreset !== undefined)
    rows.push({ id: 'agent-preset-registry', config: { default: options.agentPreset } })
  const mcpServers = validatedMcpServers(options.mcpServers, 'DeepSeek Harness', 32)
  if (mcpServers.length > 0) {
    const servers = mcpServers.map((server) => {
      return { id: `leapmux-mcp-${server.name}`, name: '@deepseek-ai/dsh-mcp-client', config: {
        transport: 'stdio',
        serverName: server.name,
        command: server.command,
        args: [...server.args],
        cwd: options.runDirectory,
        env: {},
        failOnStartupError: true,
      } }
    })
    rows.push({ insert: servers })
  }
  // JSON is valid YAML. The native loader reads this exact profile overlay.
  writeFileSync(join(home, 'cordis.patch.yml'), `${JSON.stringify(rows, null, 2)}\n`, { mode: 0o600 })
  return {
    DSH_HOME: home,
    DEEPSEEK_API_KEY: options.modelKey,
    DEEPSEEK_BASE_URL: endpoint.href.replace(/\/$/, ''),
    DSH_TELEMETRY_DISABLED: '1',
    DSH_PRIMARY_RUNTIME: '',
    LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_MODEL: DEEPSEEK_HARNESS_MODEL_ID,
    LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_EFFORT: 'high',
  }
}
