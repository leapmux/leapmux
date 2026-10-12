import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/** One BYOK custom model of Droid: the handle that Droid selects it by, and the model that the endpoint receives. */
export interface DroidCustomModel {
  handle: string
  model: string
}

export interface DroidEnvironmentOptions {
  /** The isolated HOME of the run, which holds `.factory`. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model. */
  primary: DroidCustomModel
  /** A second model, for a native settings check. */
  alternate: DroidCustomModel
}

/**
 * Isolate Factory Droid's files and service requests.
 *
 * `FACTORY_HOME_OVERRIDE` identifies the directory that contains `.factory`.
 * Droid reads settings from `<override>/.factory/settings.json`. It keeps its
 * sessions, logs, and telemetry there. The custom model points at the mock.
 * The CLI checks its isolated API key at the mock's whoami route. The proxy
 * rejects requests to other hosts.
 */
export function createDroidEnvironment(options: DroidEnvironmentOptions): Record<string, string> {
  const factoryHome = join(options.homeDir, '.factory')
  mkdirSync(factoryHome, { recursive: true })
  writePrivateJSON(join(factoryHome, 'settings.json'), droidSettings(options))
  return {
    FACTORY_HOME_OVERRIDE: options.homeDir,
    FACTORY_API_BASE_URL: options.baseURL,
    FACTORY_API_KEY: options.modelKey,
    FACTORY_DROID_AUTO_UPDATE_ENABLED: '0',
    // An unroutable sink keeps telemetry off the network.
    FACTORY_TELEMETRY_INGEST_BASE_URL: 'http://127.0.0.1:9',
    FACTORY_OTEL_ENABLED: '0',
    // Keep the built-in model catalog so the effort test can select a real
    // native ladder. The model endpoint stays on the mock, and the proxy
    // refuses every request to a real host.
    FACTORY_AIRGAP_ENABLED: '0',
    FACTORY_DISABLE_DYNAMIC_CONFIG: '1',
    FACTORY_DISABLE_KEYRING: '1',
  }
}

/** Factory Droid's BYOK settings, which point the model at the mock. */
function droidSettings(options: DroidEnvironmentOptions): Record<string, unknown> {
  const primary = {
    model: options.primary.model,
    id: options.primary.handle,
    index: 0,
    baseUrl: options.baseURL,
    apiKey: options.modelKey,
    displayName: 'Mock Model',
    maxOutputTokens: 8192,
    noImageSupport: false,
    reasoningEffort: 'high',
    provider: 'generic-chat-completion-api',
  }
  return {
    customModels: [
      primary,
      { ...primary, model: options.alternate.model, id: options.alternate.handle, index: 1, displayName: 'Alternate Mock Model' },
    ],
    sessionDefaultSettings: {
      model: options.primary.handle,
      reasoningEffort: 'none',
      autonomyMode: 'normal',
    },
  }
}
