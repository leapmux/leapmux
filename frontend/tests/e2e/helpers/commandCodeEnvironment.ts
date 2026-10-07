import type { McpServerLaunch } from './agentEnvironmentInputs'
import { mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { requireLoopbackHttpURL, validatedMcpServers } from './agentEnvironmentInputs'

export interface CommandCodeEnvironmentOptions {
  runDirectory: string
  modelURL: string
  modelKey: string
  /** The default model, which offers the low and high efforts. */
  modelID: string
  /** A model that declares no reasoning, so it offers no effort. */
  alternateModelID: string
  /** A model that offers the low, medium, and high efforts, so a model switch can keep a level that both models offer. */
  reasoningModelID: string
  mcpServers?: readonly McpServerLaunch[]
}

/** Write native settings inside the suite's private agent home. */
export function createCommandCodeEnvironment(options: CommandCodeEnvironmentOptions): Record<string, string> {
  if (!isAbsolute(options.runDirectory))
    throw new Error('The Command Code run directory must be absolute.')
  const endpoint = requireLoopbackHttpURL(options.modelURL, 'The Command Code model endpoint')
  if (!options.modelKey)
    throw new Error('The Command Code mock model key must be present.')
  const primary = parseModelID(options.modelID)
  const alternate = parseModelID(options.alternateModelID)
  const reasoning = parseModelID(options.reasoningModelID)
  const others = [alternate, reasoning]
  if (others.some(other => other.provider !== primary.provider) || new Set([primary, ...others].map(model => model.model)).size !== 3)
    throw new Error('The Command Code mock models must be distinct models of the same provider.')
  const mcpServers = Object.fromEntries(validatedMcpServers(options.mcpServers, 'Command Code', 32)
    .map(server => [server.name, { command: server.command, args: server.args }]))
  const home = join(options.runDirectory, 'agent-home')
  const config = join(home, '.commandcode')
  mkdirSync(config, { recursive: true, mode: 0o700 })
  writeFileSync(join(config, 'providers.json'), `${JSON.stringify({ provider: {
    [primary.provider]: {
      baseURL: `${endpoint.href.replace(/\/$/, '')}/v1`,
      api: 'openai-completions',
      apiKey: '$COMMAND_CODE_MOCK_KEY',
      models: {
        [primary.model]: { contextWindow: 131072, reasoning: true, reasoningEfforts: ['low', 'high'] },
        [alternate.model]: { contextWindow: 131072 },
        [reasoning.model]: { contextWindow: 131072, reasoning: true, reasoningEfforts: ['low', 'medium', 'high'] },
      },
    },
  } }, null, 2)}\n`, { mode: 0o600 })
  writeFileSync(join(config, 'settings.json'), `${JSON.stringify({
    model: options.modelID,
    mods: { disabled: ['learning', 'titling', 'update-notice'] },
    byokFeatureTasks: 'session',
  }, null, 2)}\n`, { mode: 0o600 })
  writeFileSync(join(config, 'mcp.json'), `${JSON.stringify({ mcpServers }, null, 2)}\n`, { mode: 0o600 })
  return {
    HOME: home,
    CMD_LOCAL_ONLY: '1',
    OTEL_SDK_DISABLED: 'true',
    COMMANDCODE_DISABLE_CRON: '1',
    COMMANDCODE_DISABLE_DURABLE_CRON: '1',
    COMMAND_CODE_API_KEY: 'leapmux-e2e-command-code-token',
    COMMAND_CODE_MOCK_KEY: options.modelKey,
    LEAPMUX_COMMANDCODE_DEFAULT_MODEL: options.modelID,
    LEAPMUX_COMMANDCODE_DEFAULT_EFFORT: 'high',
  }
}

function parseModelID(value: string): { provider: string, model: string } {
  const match = /^([\w-]+)\/([\w.-]+)$/.exec(value)
  if (!match?.[1] || !match[2])
    throw new Error('The Command Code mock model ID must contain a provider and a model.')
  return { provider: match[1], model: match[2] }
}
