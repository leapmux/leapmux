import type { McpServerLaunch } from './agentEnvironmentInputs'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { requireLoopbackHttpURL, validatedMcpServers } from './agentEnvironmentInputs'
import { findBinaryOnPath } from './binaryOnPath'
import { assertPrivateNativeAncestor } from './nativeConfigurationFile'
import { writeNodeLauncher } from './nodeLauncher'
import { writePrivateJSON } from './privateConfigFile'

export interface MuseEnvironmentOptions {
  runDirectory: string
  homeDir: string
  shimsDirectory: string
  baseURL: string
  modelKey: string
  modelID: string
  /** A second Spark route the catalog lists beside the pinned model, for a native model switch. */
  alternateModelID?: string
  mcpServers?: readonly McpServerLaunch[]
  nativeBinary?: string
  searchPath?: string
}

/** Muse's private settings pin the bearer endpoint for parent and child requests. */
function museSettings(options: MuseEnvironmentOptions): Record<string, unknown> {
  const endpoint = requireLoopbackHttpURL(options.baseURL, 'Muse model endpoint')
  if (!/^\/v1\/?$/.test(endpoint.pathname))
    throw new Error('The Muse model endpoint must end with /v1.')
  if (!options.modelKey.trim() || !options.modelID.trim())
    throw new Error('The Muse mock requires a model key and a model ID.')
  const servers = validatedMcpServers(options.mcpServers, 'Muse', 128)
  // The config model catalog states the routes a local endpoint serves, with the
  // context limit each compaction threshold resolves against: without a resolved
  // window the host answers session/compact with compaction_unavailable, and its
  // model/list serves the pinned model alone. Ordinary turns stay below the
  // thresholds. Compaction specs report provider usage above the soft threshold
  // and below the hard threshold to exercise an explicit native summary.
  const catalogRow = (model_id: string, is_default: boolean) => ({
    model_id,
    display_label: model_id,
    provider_id: 'meta',
    profile_id: 'tbh',
    context_limit: 128000,
    output_limit: 16384,
    cost: null,
    description: null,
    release_date: null,
    is_active: false,
    is_default,
    default_reasoning_effort: 'low',
    variants: ['minimal', 'low', 'medium', 'high'],
  })
  return {
    schema_version: 1,
    provider: 'meta',
    model: options.modelID,
    model_catalog: [
      catalogRow(options.modelID, true),
      ...(options.alternateModelID
        ? [
            catalogRow(options.alternateModelID, false),
          ]
        : []),
    ],
    context_compaction: { strategy: 'summary-preserved-suffix/v1', soft_threshold: 0.5, hard_threshold: 0.8 },
    endpoint_transport: {
      base_url: endpoint.href.replace(/\/$/, ''),
      auth: 'bearer',
    },
    context: { foreign_personal_rules: false, foreign_personal_skills: false },
    telemetry: { enabled: false },
    provider_retry: { max_retries: 0 },
    run: {
      subagent_delegation_mode: 'auto',
      reminder_roster: { agents: [] },
    },
    mcpServers: Object.fromEntries(servers.map(server => [
      server.name,
      {
        command: server.command,
        args: [...server.args],
      },
    ])),
  }
}

/** The private wrapper disables the shell sandbox for a native test host. */
function museWrapperSource(binary: string): string {
  return `import { spawn } from 'node:child_process';
const args = process.argv.slice(2);
if (args.includes('serve')) args.push('--disable-sandbox');
// A Windows launcher is a .cmd file, which node refuses to spawn without a shell.
const isCmdLauncher = process.platform === 'win32' && ${JSON.stringify(binary)}.toLowerCase().endsWith('.cmd');
const child = spawn(${JSON.stringify(binary)}, args, { stdio: 'inherit', env: process.env, shell: isCmdLauncher });
child.once('error', error => { process.stderr.write(error.message + '\\n'); process.exitCode = 1; });
child.once('exit', (code, signal) => { process.exitCode = code ?? (signal ? 1 : 0); });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { child.kill(signal); });
`
}

/** Write isolated native configuration without reading the user's Muse configuration. */
export function createMuseEnvironment(options: MuseEnvironmentOptions): Record<string, string> {
  const settings = museSettings(options)
  const path = join(options.homeDir, '.config', 'muse', 'settings.json')
  assertPrivateNativeAncestor(path, options.runDirectory)
  assertPrivateNativeAncestor(options.shimsDirectory, options.runDirectory)
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
  mkdirSync(options.shimsDirectory, { recursive: true, mode: 0o700 })
  writePrivateJSON(path, settings)
  const binary = options.nativeBinary ?? findBinaryOnPath('muse', options.searchPath ?? process.env.PATH, process.env.PATHEXT)
  if (binary) {
    const script = join(options.shimsDirectory, 'muse-private-host.mjs')
    assertPrivateNativeAncestor(script, options.runDirectory)
    writeFileSync(script, museWrapperSource(binary), { mode: 0o600 })
    writeNodeLauncher(options.shimsDirectory, 'muse', { node: process.execPath, script })
  }
  return { META_API_KEY: options.modelKey, MUSE_NO_AUTO_UPDATE: '1' }
}
