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
  return {
    schema_version: 1,
    provider: 'meta',
    model: options.modelID,
    endpoint_transport: { base_url: endpoint.href.replace(/\/$/, ''), auth: 'bearer' },
    context: { foreign_personal_rules: false, foreign_personal_skills: false },
    telemetry: { enabled: false },
    provider_retry: { max_retries: 0 },
    run: { subagent_delegation_mode: 'auto', reminder_roster: { agents: [] } },
    mcpServers: Object.fromEntries(servers.map(server => [server.name, { command: server.command, args: [...server.args] }])),
  }
}

/** The private wrapper disables the shell sandbox for a native test host. */
function museWrapperSource(binary: string): string {
  return `import { spawn } from 'node:child_process';
const args = process.argv.slice(2);
if (args.includes('serve')) args.push('--disable-sandbox');
const child = spawn(${JSON.stringify(binary)}, args, { stdio: 'inherit', env: process.env });
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
