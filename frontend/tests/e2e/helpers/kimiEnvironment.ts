import type { McpProbeServer } from './mcpProbeServer'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { KIMI_CODE_SERVICE_PATH } from './kimiCodeSurface'
import { writePrivateJSON } from './privateConfigFile'
import { quotePosixShellArgument } from './shellArguments'

export const KIMI_BLOCKED_PROMPT_MARKER = 'LEAPMUX-HOOK-BLOCK'
export const KIMI_BLOCKED_PROMPT_REASON = 'LeapMux test hook blocked this prompt'

/** One model of Kimi Code's configuration: the alias that Kimi addresses it by, and the model that the endpoint receives. */
export interface KimiModelAlias {
  alias: string
  model: string
}

/** The provider key of the models that a Kimi Code account supplies. */
export const KIMI_ACCOUNT_PROVIDER = 'managed:kimi-code'

/**
 * The sign-in host of the mocked account, on the mock. The mock serves nothing there: the stored token never expires,
 * so Kimi Code never refreshes it, and a refresh would fail at the mock instead of reaching Kimi's own host.
 */
const KIMI_OAUTH_PATH = '/kimi/oauth'

/** The Kimi Code account of the run, whose plan usage the mock reports. See ./kimiCodeSurface. */
export interface KimiAccountOptions {
  /** The origin of the mock, which stands in for Kimi's sign-in host and for the Kimi Code service. */
  origin: string
  /** The access token of the mocked account. */
  accessToken: string
  /**
   * The model that the account supplies. A real account's provider reads its models from the Kimi Code service and
   * rewrites the configuration. This provider is an OpenAI-compatible stand-in on the mock instead, under the
   * account's provider key, which is all that LeapMux reads.
   */
  model: KimiModelAlias
}

/**
 * The name of the credential file of a Kimi Code account, for one sign-in host and one service root, as Kimi Code
 * 2.3 derives it (`resolveKimiCodeOAuthKey`): the first 16 hex digits of the SHA-256 of a JSON object of the two, in
 * that order and without a trailing slash. A file under another name is a signed-out account, whose usage read fails.
 */
export function kimiAccountCredentialKey(oauthHost: string, serviceURL: string): string {
  const scope = JSON.stringify({ oauthHost: oauthHost.trim().replace(/\/+$/, ''), baseUrl: serviceURL.replace(/\/+$/, '') })
  return `kimi-code-env-${createHash('sha256').update(scope).digest('hex').slice(0, 16)}`
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
  /** The signed-in Kimi Code account. */
  account: KimiAccountOptions
}

/**
 * Point Kimi Code at the mock through its own `config.toml`, with the echo server, no telemetry, and no update.
 * Kimi reads the key from `LEAPMUX_E2E_MODEL_API_KEY`, so the key stays out of the file.
 *
 * Kimi Code is signed in to the mocked account: its token is in Kimi's credential file, and the Kimi Code service is
 * the mock. Only an agent on the account's model reads the plan usage, which the mock reports only while a scenario
 * owns it.
 */
export function createKimiEnvironment(options: KimiEnvironmentOptions): Record<string, string> {
  const kimiHome = join(options.homeDir, '.kimi-code')
  mkdirSync(kimiHome, { recursive: true })
  const serviceURL = `${options.account.origin}${KIMI_CODE_SERVICE_PATH}`
  const oauthHost = `${options.account.origin}${KIMI_OAUTH_PATH}`
  const credentials = join(kimiHome, 'credentials')
  mkdirSync(credentials, { recursive: true, mode: 0o700 })
  // Kimi Code never refreshes a token whose expiry is 0.
  writePrivateJSON(join(credentials, `${kimiAccountCredentialKey(oauthHost, serviceURL)}.json`), {
    access_token: options.account.accessToken,
    refresh_token: options.account.accessToken,
    expires_at: 0,
    expires_in: 0,
    token_type: 'Bearer',
    scope: '',
  })
  const hook = join(options.homeDir, 'kimi-block-hook.sh')
  writeFileSync(hook, `#!/bin/sh\nprintf '%s\\n' ${quotePosixShellArgument(KIMI_BLOCKED_PROMPT_REASON)} >&2\nexit 2\n`, { mode: 0o700 })
  writeFileSync(join(kimiHome, 'config.toml'), kimiConfig(options, quotePosixShellArgument(hook)), { mode: 0o600 })
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
    // Kimi loads a project's MCP servers, skills and instructions only from a workspace that it trusts, and the
    // Worker asks the reader to trust each workspace that Kimi does not trust. Kimi's own switch trusts every
    // workspace, so no spec meets that question unless its own Worker clears the switch.
    KIMI_CODE_TRUST_WORKSPACE: '1',
    // The service of the mocked account, and its sign-in host. Kimi Code keys the stored token by both.
    KIMI_CODE_BASE_URL: serviceURL,
    KIMI_CODE_OAUTH_HOST: oauthHost,
  }
}

/**
 * Kimi Code's `config.toml`.
 *
 * `auto_session_title = false` keeps the title local. A title that a model
 * writes would be a request with no scenario marker. Kimi sends a model request
 * only for a turn, so no other housekeeping request reaches the mock.
 */
function kimiConfig(options: KimiEnvironmentOptions, hookCommand: string): string {
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

[providers."${KIMI_ACCOUNT_PROVIDER}"]
type = "openai"
base_url = "${options.baseURL}"
api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"

[models."${options.account.model.alias}"]
provider = "${KIMI_ACCOUNT_PROVIDER}"
model = "${options.account.model.model}"
display_name = "Kimi for Coding"
max_context_size = 128000
capabilities = ["tool_use"]

[[hooks]]
event = "UserPromptSubmit"
matcher = "${KIMI_BLOCKED_PROMPT_MARKER}"
command = ${JSON.stringify(hookCommand)}
`
}
