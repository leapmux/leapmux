import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

export interface ClineEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /**
   * Cline's provider in the isolated settings: the generic OpenAI Chat Completions
   * client, which takes any base URL and any model id.
   */
  providerID: string
  modelID: string
  mcpEchoServer: McpProbeServer
}

/**
 * Configure private Cline settings and data.
 *
 * The Worker reads the same settings as Cline's CLI.
 * CLINE_DIR and CLINE_DATA_DIR select private directories under the isolated HOME.
 * They replace inherited user values that could select real configuration.
 *
 * - providers.json selects the mock as the last used provider in the native cline auth format.
 * - global-settings.json disables telemetry and the update (`autoUpdateEnabled`). No environment variable can replace Cline's compiled telemetry endpoint.
 * - An empty feature cache prevents a native remote flag request for one hour.
 *   The refusing proxy prevents a later remote fetch.
 *
 * Cline treats each empty path override as unset through process.env.X?.trim().
 * Each path then follows CLINE_DATA_DIR.
 * These overrides include every CLINE_*_DIR and CLINE_*_PATH in paths.ts.
 * They also include the hook directory and hook, log, capture, and approval files.
 * The Worker sets its agenda database and daemon discovery record after the shell profile.
 * Empty fixture values do not replace those Worker values.
 *
 * CLINE_PROVIDER and CLINE_MODEL stay unset.
 * Cline reads them through ??, so an empty string would select an empty value.
 * The Worker supplies the provider and model in session.create.
 */
export function createClineEnvironment(options: ClineEnvironmentOptions): Record<string, string> {
  const clineDir = join(options.homeDir, '.cline')
  const dataDir = join(clineDir, 'data')
  const settingsDir = join(dataDir, 'settings')
  const cacheDir = join(dataDir, 'cache')
  mkdirSync(settingsDir, { recursive: true })
  mkdirSync(cacheDir, { recursive: true })
  const writtenAt = Date.now()
  writePrivateJSON(join(settingsDir, 'providers.json'), clineProviders(options, writtenAt))
  writePrivateJSON(join(settingsDir, 'global-settings.json'), { telemetryOptOut: true, autoUpdateEnabled: false })
  writePrivateJSON(join(settingsDir, 'cline_mcp_settings.json'), {
    mcpServers: { [options.mcpEchoServer.name]: { transport: { type: 'stdio', command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args] } } },
  })
  writePrivateJSON(join(cacheDir, 'feature-flags.json'), clineFeatureFlags(writtenAt))
  return {
    CLINE_DIR: clineDir,
    CLINE_DATA_DIR: dataDir,
    CLINE_PROVIDER_SETTINGS_PATH: '',
    CLINE_GLOBAL_SETTINGS_PATH: '',
    CLINE_DB_DATA_DIR: '',
    CLINE_SESSION_DATA_DIR: '',
    CLINE_TEAM_DATA_DIR: '',
    CLINE_MCP_SETTINGS_PATH: '',
    CLINE_CONNECTOR_DATA_DIR: '',
    CLINE_CONNECTOR_SETTINGS_PATH: '',
    CLINE_CONNECTORS_DB_PATH: '',
    // A developer's value would make each daemon poll a real automation database.
    CLINE_CRON_DB_PATH: '',
    CLINE_TASKS_DB_PATH: '',
    CLINE_HOOKS_DIR: '',
    CLINE_HOOKS_LOG_PATH: '',
    CLINE_LOG_PATH: '',
    CLINE_CAPTURE_DIR: '',
    CLINE_TOOL_APPROVAL_DIR: '',
    // The build environment decides the owner of the shared stores. An explicit
    // value wins over NODE_ENV, so a developer's `NODE_ENV=development` cannot move
    // a daemon to Cline's development stores. `production` is what a released
    // `cline` resolves by itself.
    CLINE_BUILD_ENV: 'production',
    // Cline's own account key. The mock provider takes its key from the settings,
    // and an empty key reaches no Cline service.
    CLINE_API_KEY: '',
    // Every `cline` that is not the daemon asks the npm registry at its start, and
    // starts a detached `npm update -g cline` as it exits when a newer release
    // exists. The worker sets it for the daemon as well. Only the exact value `1`
    // counts.
    CLINE_NO_AUTO_UPDATE: '1',
  }
}

/**
 * Build Cline's providers.json in the native cline auth format.
 *
 * Cline treats a schema failure as absent settings.
 * An entry without updatedAt selects the built-in provider endpoint, which the private proxy refuses.
 */
function clineProviders(options: ClineEnvironmentOptions, updatedAt: number): Record<string, unknown> {
  return {
    version: 1,
    lastUsedProvider: options.providerID,
    modes: {},
    providers: {
      [options.providerID]: {
        settings: { provider: options.providerID, apiKey: options.modelKey, model: options.modelID, baseUrl: options.baseURL },
        updatedAt: new Date(updatedAt).toISOString(),
        tokenSource: 'manual',
      },
    },
  }
}

/**
 * Cline's feature-flag cache, holding no flag. Cline trusts the cache for an hour
 * after `updatedAt`, which covers a whole run.
 */
function clineFeatureFlags(updatedAt: number): Record<string, unknown> {
  return { version: 2, updatedAt, userId: null, flagsPayload: { featureFlags: {}, featureFlagPayloads: {} } }
}
