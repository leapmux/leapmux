import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/** Pi addresses a model through a named provider in its own `models.json`. */
const PI_PROVIDER_ID = 'zai'

export interface PiEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model. */
  modelID: string
  /** The second model, which takes a reasoning effort. */
  flashModelID: string
  mcpEchoServer: McpProbeServer
  /** The developer's own HOME, where the Pi packages that the specs load are installed. Absent, Pi loads none. */
  realHomeDir: string | undefined
}

/** The agent directory of Pi under the isolated HOME, which holds its configuration and sessions. */
export function piAgentDirectory(homeDir: string): string {
  return join(homeDir, '.pi', 'agent')
}

/** Point Pi at the mock through a provider of its own `models.json`, with the echo server and no management request. */
export function createPiEnvironment(options: PiEnvironmentOptions): Record<string, string> {
  const piAgentDir = piAgentDirectory(options.homeDir)
  mkdirSync(piAgentDir, { recursive: true })
  writePrivateJSON(join(piAgentDir, 'models.json'), piModels(options))
  writePrivateJSON(join(piAgentDir, 'settings.json'), {
    defaultProvider: PI_PROVIDER_ID,
    defaultModel: options.modelID,
    compaction: { keepRecentTokens: 32 },
    packages: piPackagePaths(options.realHomeDir),
  })
  writePrivateJSON(join(piAgentDir, 'mcp.json'), {
    mcpServers: { [options.mcpEchoServer.name]: { command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args], exposure: 'direct' } },
  })
  return {
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
  }
}

function piModels(options: PiEnvironmentOptions): Record<string, unknown> {
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
        baseUrl: options.baseURL,
        api: 'openai-completions',
        apiKey: options.modelKey,
        models: [model(options.modelID, 'GLM-5.3'), { ...model(options.flashModelID, 'GLM-5.3 Flash'), compat: { supportsReasoningEffort: true } }],
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
  ].map(packageName => join(modules, packageName))
}
