import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/** One model of Kimi Code's configuration: the alias that Kimi addresses it by, and the model that the endpoint receives. */
export interface KimiModelAlias {
  alias: string
  model: string
}

export interface KimiEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  /** The provider table of the configuration. */
  providerID: string
  /** The default model, which thinks and takes an effort. */
  thinking: KimiModelAlias
  /** A second model, which does neither, so a switch also changes the effort axis. */
  plain: KimiModelAlias
  /**
   * A third model, which thinks and takes the effort ladder of the default model but starts at Medium, not High. A
   * switch that keeps a level then differs from a switch that takes the default of the new model.
   */
  alternateThinking: KimiModelAlias
  mcpEchoServer: McpProbeServer
}

/**
 * Point Kimi Code at the mock through its own `config.toml`, with the echo server, no telemetry, and no update.
 * Kimi reads the key from `LEAPMUX_E2E_MODEL_API_KEY`, so the key stays out of the file.
 */
export function createKimiEnvironment(options: KimiEnvironmentOptions): Record<string, string> {
  const kimiHome = join(options.homeDir, '.kimi-code')
  mkdirSync(kimiHome, { recursive: true })
  writeFileSync(join(kimiHome, 'config.toml'), kimiConfig(options), { mode: 0o600 })
  writePrivateJSON(join(kimiHome, 'mcp.json'), {
    mcpServers: { [options.mcpEchoServer.name]: { command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args] } },
  })
  // Kimi Code keeps its configuration, sessions, and server token here. The
  // two switches stop the telemetry upload and the update. `kimi web` makes no
  // update check, so the update switch guards the swap of a staged native
  // update, which `kimi --version` and `kimi web` both run; the TUI also checks
  // for a release and installs it. Kimi downloads `rg` from its host when `rg` is
  // absent from PATH, and no switch stops that, so the run keeps the developer's
  // PATH.
  return {
    KIMI_CODE_HOME: kimiHome,
    KIMI_DISABLE_TELEMETRY: '1',
    KIMI_CODE_NO_AUTO_UPDATE: '1',
  }
}

/**
 * Kimi Code's `config.toml`.
 *
 * `auto_session_title = false` keeps the title local. A title that a model
 * writes would be a request with no scenario marker. Kimi sends a model request
 * only for a turn, so no other housekeeping request reaches the mock.
 */
function kimiConfig(options: KimiEnvironmentOptions): string {
  const provider = options.providerID
  return `default_model = "${options.thinking.alias}"
telemetry = false
auto_session_title = false

[providers.${provider}]
type = "openai"
base_url = "${options.baseURL}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"

[models."${options.thinking.alias}"]
provider = "${provider}"
model = "${options.thinking.model}"
display_name = "GLM-5.3 Flash"
max_context_size = 128000
capabilities = ["tool_use", "thinking", "image_in"]
support_efforts = ["low", "medium", "high"]
default_effort = "high"

[models."${options.plain.alias}"]
provider = "${provider}"
model = "${options.plain.model}"
display_name = "GLM-5.3"
max_context_size = 128000
capabilities = ["tool_use"]

[models."${options.alternateThinking.alias}"]
provider = "${provider}"
model = "${options.alternateThinking.model}"
display_name = "DeepSeek Flash"
max_context_size = 128000
capabilities = ["tool_use", "thinking"]
support_efforts = ["low", "medium", "high"]
default_effort = "medium"
`
}
