import type { McpProbeServer } from './mcpProbeServer'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/**
 * The built-in Codewhale route that the isolated configuration points at the
 * mock.
 *
 * `deepseek` is the built-in provider route. Codewhale reads
 * `reasoning_content` as thinking only on a route that it knows to reason.
 * An `openai` route merged the reasoning into the answer during a probe.
 */
const CODEWHALE_PROVIDER_ID = 'deepseek'

export interface CodewhaleEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default text model. */
  modelID: string
  /** The built-in model whose route accepts image input. */
  visionModelID: string
  mcpEchoServer: McpProbeServer
}

/**
 * Point Codewhale at the mock through its own configuration and a fresh model catalog for the mock endpoint, and
 * close every request that no test scripts.
 */
export function createCodewhaleEnvironment(options: CodewhaleEnvironmentOptions): Record<string, string> {
  const codewhaleHome = join(options.homeDir, '.codewhale')
  mkdirSync(join(codewhaleHome, 'catalog'), { recursive: true })
  writeFileSync(join(codewhaleHome, 'config.toml'), codewhaleConfig(options), { mode: 0o600 })
  writePrivateJSON(join(codewhaleHome, 'mcp.json'), {
    servers: { [options.mcpEchoServer.name]: { command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args] } },
  })
  writePrivateJSON(join(codewhaleHome, 'catalog', 'provider-catalogs.json'), codewhaleCatalog(options))
  // Each of the three switches below has a twin in the configuration file. The
  // variables outrank the file, so a default that a later version adds to the
  // file cannot turn one of them back on.
  return {
    CODEWHALE_HOME: codewhaleHome,
    CODEWHALE_TELEMETRY: '0',
    CODEWHALE_NO_UPDATE_CHECK: '1',
    CODEWHALE_ALLOW_SHELL: '1',
  }
}

/**
 * Codewhale's configuration.
 *
 * Each table turns off one request that no test scripts:
 *
 * - `telemetry` and `[update]`: the telemetry upload and the release check.
 * - `[retry]`: the client retries a failed model request three times, so an
 *   unscripted turn would reach the mock three more times.
 * - `[reasoning_only]`: an answer that holds only reasoning makes the client
 *   ask again, twice by default.
 * - `[snapshots]`: a side git repository under HOME that snapshots each turn.
 *
 * `[tools] user_input_timeout_seconds = 0` removes the wait limit on an
 * approval or a question. The default is 300 seconds, after which the runtime
 * denies the approval by itself, and a slow run would read as a user who
 * refused.
 */
function codewhaleConfig(options: CodewhaleEnvironmentOptions): string {
  return `provider = "${CODEWHALE_PROVIDER_ID}"
default_text_model = "${options.modelID}"
telemetry = false
allow_shell = true

[providers.${CODEWHALE_PROVIDER_ID}]
base_url = "${options.baseURL}"
api_key = "${options.modelKey}"
auth_mode = "api-key"
model = "${options.visionModelID}"

[tools]
user_input_timeout_seconds = 0

[update]
check_for_updates = false

[snapshots]
enabled = false

[retry]
enabled = false

[reasoning_only]
max_reprompts = 0
`
}

/**
 * Codewhale accepts image input only from an offering of the endpoint the thread
 * runs. A thread binds the endpoint of the model it opened with, and a later model
 * switch keeps that binding, so every mock offering states the one endpoint.
 */
function codewhaleCatalog(options: CodewhaleEnvironmentOptions): Record<string, unknown> {
  const fingerprint = createHash('sha256').update(options.baseURL).digest('hex')
  const fetchedAt = Math.floor(Date.now() / 1000)
  const provider = `${CODEWHALE_PROVIDER_ID}:${CODEWHALE_PROVIDER_ID}`
  const offering = (model: string, endpoint: string, image: boolean, isDefault: boolean) => ({
    provider: CODEWHALE_PROVIDER_ID,
    wire_model_id: model,
    endpoint_key: endpoint,
    default_for_provider: isDefault,
    modalities: { input: image ? ['text', 'image'] : ['text'], output: ['text'] },
    source: { kind: 'live', base_url_fingerprint: fingerprint, fetched_at: fetchedAt },
  })
  return {
    schema_version: 2,
    cache: {
      entries: {
        [`${provider}\x1F${fingerprint}`]: {
          provider,
          base_url_fingerprint: fingerprint,
          fetched_at: fetchedAt,
          ttl_secs: 86_400,
          offerings: [
            offering(options.modelID, 'chat', false, true),
            offering(options.visionModelID, 'chat', true, false),
          ],
          status: { state: 'fresh' },
        },
      },
    },
  }
}
