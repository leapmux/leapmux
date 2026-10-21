import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { writeMcpFormServer } from './mcpFormServer'

export interface CodexEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  /**
   * The model that Codex runs when a thread states none, which the Default model entry resolves to. Without it, the
   * default is the one that the installed Codex build chooses, which can change with each release.
   */
  defaultModelID: string
}

/**
 * Point Codex at the mock through a provider of its own `config.toml`, beside a local MCP form server.
 * Codex reads the key from `LEAPMUX_E2E_MODEL_API_KEY`, which the run sets for every agent.
 */
export function createCodexEnvironment(options: CodexEnvironmentOptions): Record<string, string> {
  const codexHome = join(options.homeDir, '.codex')
  mkdirSync(codexHome, { recursive: true })
  const formServer = writeMcpFormServer(codexHome, 'form-server.mjs')
  writeFileSync(join(codexHome, 'config.toml'), codexConfig(options, formServer), { mode: 0o600 })
  return { CODEX_HOME: codexHome }
}

// `check_for_update_on_startup` turns off the update notice of Codex's TUI. The
// `codex app-server` that the worker starts never reads it, and never updates: only
// the TUI and the `codex update` and `codex app-server daemon` commands do.
function codexConfig(options: CodexEnvironmentOptions, mcpFormServer: McpProbeServer): string {
  return `model_provider = "leapmux-e2e"
model = "${options.defaultModelID}"
check_for_update_on_startup = false

# These switches stop memory phase 1. Codex still runs phase 2 without a scenario marker.
# The codex-memory-consolidation housekeeping rule answers that request.
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
base_url = "${options.baseURL}"
env_key = "LEAPMUX_E2E_MODEL_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
# A stream failure gets one retry. An HTTP request failure gets none.
stream_max_retries = 1
`
}
