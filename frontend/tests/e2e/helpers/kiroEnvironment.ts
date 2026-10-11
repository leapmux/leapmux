import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

/**
 * The switches that stop Kiro from calling a model or a host outside a turn a test
 * scripts. Kiro's engine reads each `KIRO_DISABLE_*` switch as the word `true`.
 *
 * - The session title is a model call of its own at the first prompt.
 * - The recap is a model call when a reader returns to a session.
 * - The experiment configuration is a request to Kiro's own host.
 * - Telemetry, the update check and the remote changelog are requests to hosts
 *   that no setting points at the mock.
 */
const KIRO_QUIET_ENV: Readonly<Record<string, string>> = {
  KIRO_DISABLE_SESSION_TITLE_LLM: 'true',
  KIRO_DISABLE_RECAP: 'true',
  KIRO_DISABLE_EXPERIMENT_CONFIG: 'true',
  KIRO_DISABLE_TELEMETRY: '1',
  KIRO_NO_AUTO_UPDATE: '1',
  KIRO_NO_REMOTE_CHANGELOG: '1',
}

export interface KiroEnvironmentOptions {
  /** The run directory, which holds Kiro's data. */
  runDirectory: string
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which serves Kiro's own service. */
  origin: string
  /** The bearer key that Kiro sends. Kiro validates only its local `ksk_` prefix. */
  apiKey: string
}

/**
 * Point Kiro at the mock: every service in its settings, its remote endpoints in the environment, and no AWS
 * profile or credential of the developer.
 */
export function createKiroEnvironment(options: KiroEnvironmentOptions): Record<string, string> {
  // Kiro keeps its settings here, and its sessions under `~/.kiro/sessions` of HOME.
  const kiroHome = join(options.homeDir, '.kiro')
  const settingsDir = join(kiroHome, 'settings')
  const dataDir = join(options.runDirectory, 'kiro-data')
  mkdirSync(settingsDir, { recursive: true })
  mkdirSync(dataDir, { recursive: true })
  writePrivateJSON(join(settingsDir, 'cli.json'), kiroSettings(options.origin))
  return {
    // Kiro reads its settings, and so its endpoints, from KIRO_HOME, which a
    // developer's own value would otherwise point at their real configuration.
    KIRO_HOME: kiroHome,
    KIRO_DATA_DIR: dataDir,
    KIRO_API_KEY: options.apiKey,
    KIRO_REMOTE_SESSIONS_ENDPOINT: options.origin,
    CLOUD_CONFIG_ENDPOINT: options.origin,
    ...KIRO_QUIET_ENV,
    // The AWS SDKs of Kiro read a profile and a credential from these, and the
    // developer's own values would otherwise pass through to the agent. The files
    // are the defaults under the isolated HOME, which hold nothing, and the
    // profile is the default one of those files. The SDKs read an empty key as
    // no key.
    AWS_PROFILE: 'default',
    AWS_CONFIG_FILE: join(options.homeDir, '.aws', 'config'),
    AWS_SHARED_CREDENTIALS_FILE: join(options.homeDir, '.aws', 'credentials'),
    AWS_ACCESS_KEY_ID: '',
    AWS_SECRET_ACCESS_KEY: '',
    AWS_SESSION_TOKEN: '',
  }
}

/**
 * Kiro's settings: every service it calls, pinned to the mock.
 *
 * Kiro's v3 engine reads its runtime service from `api.krs.service` and its control
 * plane from `api.cps.service`, and it honors an `http` endpoint on loopback alone.
 * The three older keys are the v2 engine's, which LeapMux does not start. They stay
 * pinned so no Kiro engine can reach its real service. Telemetry and the update
 * check are off.
 */
function kiroSettings(origin: string): Record<string, unknown> {
  const service = { endpoint: origin, region: 'us-east-1' }
  return {
    'api.krs.service': service,
    'api.cps.service': service,
    'api.codewhisperer.service': service,
    'api.q.service': origin,
    'api.kiroauth.service': origin,
    'telemetry.enabled': false,
    'app.disableAutoupdates': true,
  }
}
