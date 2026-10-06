import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { writeMcpFormServer } from './mcpFormServer'

export interface CodexEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
}

/**
 * Point Codex at the mock through a provider of its own `config.toml`, beside a local MCP form server.
 * Codex reads the key from `LEAPMUX_E2E_MODEL_API_KEY`, which the run sets for every agent.
 */
export function createCodexEnvironment(options: CodexEnvironmentOptions): Record<string, string> {
  const codexHome = join(options.homeDir, '.codex')
  mkdirSync(codexHome, { recursive: true })
  const formServer = writeMcpFormServer(codexHome, 'form-server.mjs')
  writeFileSync(join(codexHome, 'config.toml'), codexConfig(options.baseURL, formServer), { mode: 0o600 })
  return { CODEX_HOME: codexHome }
}

// `check_for_update_on_startup` turns off the update notice of Codex's TUI. The
// `codex app-server` that the worker starts never reads it, and never updates: only
// the TUI and the `codex update` and `codex app-server daemon` commands do.
function codexConfig(baseURL: string, mcpFormServer: McpProbeServer): string {
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

[mcp_servers.${mcpFormServer.name}]
command = ${JSON.stringify(mcpFormServer.command)}
args = [${mcpFormServer.args.map(argument => JSON.stringify(argument)).join(', ')}]

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
