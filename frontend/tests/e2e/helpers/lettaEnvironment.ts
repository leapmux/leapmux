import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { agentSearchPath, findBinary } from './binaryOnPath'
import { writePrivateJSON } from './privateConfigFile'

export interface LettaEnvironmentOptions {
  /** The run directory, which holds the local backend. */
  runDirectory: string
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The private temporary root of the run, which both setup commands receive. */
  temporaryEnv: Record<'TMPDIR' | 'TEMP' | 'TMP', string>
}

/**
 * Letta Code's isolated configuration, and the local backend that its App Server needs before an agent starts.
 *
 * `LETTA_LOCAL_BACKEND_DIR` moves the flat-file store, `LETTA_HOME` the agent
 * settings and transcripts. The PATH must contain a real node plus the `letta`
 * bin: subagents re-exec `letta`, and a mise shim fails under an isolated HOME.
 */
export function createLettaEnvironment(options: LettaEnvironmentOptions): Record<string, string> {
  const lettaHome = join(options.homeDir, '.letta')
  const backendDir = join(options.runDirectory, 'letta-backend')
  mkdirSync(lettaHome, { recursive: true })
  mkdirSync(join(backendDir, 'providers'), { recursive: true })
  writePrivateJSON(join(backendDir, 'providers', 'auth.json'), lettaAuth(options))
  // `letta backend local` writes this. Without it the App Server creates agents
  // against the cloud API and runtime_start fails 401.
  writePrivateJSON(join(lettaHome, 'settings.json'), { preferredBackendMode: 'local' })
  const env = lettaEnv(lettaHome, backendDir, options.modelKey)
  prepareLettaBackend(lettaHome, env, options)
  return env
}

function lettaEnv(lettaHome: string, backendDir: string, modelKey: string): Record<string, string> {
  return {
    LETTA_HOME: lettaHome,
    LETTA_LOCAL_BACKEND_DIR: backendDir,
    // The App Server refuses to start a runtime without an API key in the
    // environment, even when the local backend has a provider record.
    LETTA_API_KEY: modelKey,
    LETTA_CODE_TELEM: '0',
    DO_NOT_TRACK: '1',
    LETTA_CODE_OFFLINE: '1',
    LETTA_DISABLE_MODS: '1',
    // Letta Code runs `npm install -g @letta-ai/letta-code` from its startup path,
    // which replaces the operator's global install. `letta server` and the other
    // subcommands exit before that path, but each subagent is a `letta` child that
    // takes it, and the child inherits this environment. Only the exact value `1`
    // counts. The worker pins it too.
    DISABLE_AUTOUPDATER: '1',
  }
}

/** Letta Code's provider credential record, which points the model at the mock. */
function lettaAuth(options: LettaEnvironmentOptions): Record<string, unknown> {
  return {
    version: 1,
    providers: {
      'openai-compatible': {
        auth: { type: 'api', key: options.modelKey },
        base_url: options.baseURL,
      },
      'openai': {
        id: 'local-provider-openai',
        name: 'openai',
        provider_type: 'openai',
        provider_category: 'byok',
        auth: { type: 'api', key: options.modelKey },
        base_url: options.baseURL,
      },
    },
  }
}

/**
 * Run the two CLI steps the App Server needs before an agent starts.
 *
 * `letta backend local` pins the local backend, and `letta connect
 * openai-compatible` discovers the mock's models so the agent's model handle
 * resolves. Both write the isolated store; neither reaches a real account.
 * Each step fails on a repeat run, which is fine: the store is already
 * prepared.
 */
function prepareLettaBackend(lettaHome: string, lettaEnvironment: Record<string, string>, options: LettaEnvironmentOptions): void {
  // The real install dirs go first on PATH: the `letta` entry is a JS file
  // whose `#!/usr/bin/env node` must resolve to a real node, not a mise shim.
  // HOME must be the isolated home too: `letta model list` reads the model
  // catalog under HOME, not only LETTA_HOME, and the real HOME holds the
  // developer's own records.
  //
  // Every proxy variable is stripped: the loopback mock must be reached
  // direct, or the model-discovery request never arrives and the catalog is
  // empty.
  const base = { ...process.env }
  for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY', 'all_proxy'])
    delete base[key]
  const env = { ...base, ...options.temporaryEnv, ...lettaEnvironment, HOME: lettaHome, PATH: agentSearchPath(process.env.PATH), NO_PROXY: '127.0.0.1,localhost', no_proxy: '127.0.0.1,localhost' }
  const letta = findBinary('letta', env)
  if (letta === null)
    return
  for (const args of [
    ['backend', 'local'],
    ['connect', 'openai-compatible', '--base-url', options.baseURL, '--api-key', options.modelKey],
  ]) {
    try {
      execFileSync(letta, args, { env, encoding: 'utf8', timeout: 60_000, stdio: ['ignore', 'pipe', 'pipe'] })
    }
    catch {
      // A repeat run fails each step against a store that is already prepared.
    }
  }
}
