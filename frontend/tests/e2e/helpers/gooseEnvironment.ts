import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { writeMcpFormServer } from './mcpFormServer'

export interface GooseEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The Chat Completions model that Goose sends images and tool calls through. */
  modelID: string
}

/**
 * Point Goose at the mock through its OpenAI provider, with the Todo extension on and a local MCP form server.
 * Goose reads the generic `OPENAI_API_KEY` and `OPENAI_BASE_URL`, which the run sets for every agent.
 */
export function createGooseEnvironment(options: GooseEnvironmentOptions): Record<string, string> {
  const gooseRoot = join(options.homeDir, '.goose')
  mkdirSync(join(gooseRoot, 'config'), { recursive: true })
  const formServer = writeMcpFormServer(gooseRoot, 'form-server.mjs')
  writeFileSync(join(gooseRoot, 'config', 'config.yaml'), gooseConfig(formServer), { mode: 0o600 })
  return {
    GOOSE_PROVIDER: 'openai',
    GOOSE_MODEL: options.modelID,
    GOOSE_PATH_ROOT: gooseRoot,
  }
}

/** Goose disables Todo by default. The isolated fixture also supplies a form server. */
function gooseConfig(formServer: McpProbeServer): string {
  return `extensions:
  todo:
    enabled: true
    type: platform
    name: todo
    description: Enable a todo list for goose so it can keep track of what it is doing
    display_name: Todo
    available_tools: []
  ${formServer.name}:
    enabled: true
    type: stdio
    name: ${formServer.name}
    description: Request the disposable probe form
    cmd: ${JSON.stringify(formServer.command)}
    args:
${formServer.args.map(argument => `      - ${JSON.stringify(argument)}`).join('\n')}
    envs: {}
    env_keys: []
    timeout: 120
`
}
