import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { agentSearchPathEnv, findBinary } from './binaryOnPath'
import { writePrivateJSON } from './privateConfigFile'
import { quotePosixShellArgument } from './shellArguments'

/** Pi addresses a model through a named provider in its own `models.json`. */
const PI_PROVIDER_ID = 'zai'

export interface PiEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model. */
  modelID: string
  /** The second model, which takes a reasoning effort. */
  flashModelID: string
  mcpEchoServer: McpProbeServer
  /** The developer's own HOME, where the Pi packages that the specs load are installed. Absent, Pi loads none. */
  realHomeDir: string | undefined
  /** The directory of private command wrappers, first on the run's PATH. */
  shimsDirectory: string
}

/** The agent directory of Pi under the isolated HOME, which holds its configuration and sessions. */
export function piAgentDirectory(homeDir: string): string {
  return join(homeDir, '.pi', 'agent')
}

/**
 * Point Pi at the mock through a provider of its own `models.json`, with the echo server, no management request, and
 * no context file.
 */
export function createPiEnvironment(options: PiEnvironmentOptions): Record<string, string> {
  writePiWrapper(options.shimsDirectory)
  const piAgentDir = piAgentDirectory(options.homeDir)
  mkdirSync(piAgentDir, { recursive: true })
  writePrivateJSON(join(piAgentDir, 'models.json'), piModels(options))
  writePrivateJSON(join(piAgentDir, 'settings.json'), {
    defaultProvider: PI_PROVIDER_ID,
    defaultModel: options.modelID,
    compaction: { keepRecentTokens: 32 },
    packages: piPackagePaths(options.realHomeDir),
  })
  writePrivateJSON(join(piAgentDir, 'mcp.json'), {
    mcpServers: { [options.mcpEchoServer.name]: { command: options.mcpEchoServer.command, args: [...options.mcpEchoServer.args], exposure: 'direct' } },
  })
  return {
    PI_CODING_AGENT_DIR: piAgentDir,
    PI_OFFLINE: '1',
    PI_SKIP_VERSION_CHECK: '1',
    // EMPTY, which Pi, omp and the worker's session readers all read as unset.
    // omp reads this variable whatever its profile, so a directory here would put
    // omp's sessions beside Pi's, and each session picker would list the other's.
    // Pi keeps its sessions under its agent directory instead, the path the
    // worker's Pi reader resolves from PI_CODING_AGENT_DIR. Empty rather than
    // absent, because a developer's own value would otherwise reach both agents.
    PI_CODING_AGENT_SESSION_DIR: '',
  }
}

/**
 * Put a private `pi` wrapper first on the run's PATH, which starts each session of the installed Pi with
 * `--no-context-files`.
 *
 * Pi 1.0.0 reads the first of `AGENTS.override.md`, `AGENTS.md`, `AGENTS.MD`, `CLAUDE.md` and `CLAUDE.MD` from its
 * working directory and from each directory above it, up to the root of the file system (`loadProjectContextFiles`,
 * `dist/core/resource-loader.js`). A git repository does not stop that walk, and no setting or environment variable
 * turns it off. Only the flag does, and it turns off the files of the working directory too. The sentinel files of the
 * run root (./ancestorInstructions.ts) are above every working directory, and no spec relies on a context file.
 *
 * The Worker, the startup wrapper of a spec, and `pi/scriptedModel.ts` all find Pi through PATH, so each one starts the
 * wrapper. Pi reads a subcommand (`install`, `config`, `mcp` and the rest) from its first argument, and the subcommand
 * refuses an option that it does not know. So the wrapper adds the flag only when the first argument is an option, or
 * absent, as in each start of a session (`--mode rpc`).
 *
 * Windows gets no wrapper: a POSIX shell script does not start there. Pi then reads the sentinel files on Windows.
 */
function writePiWrapper(shimsDirectory: string): void {
  if (process.platform === 'win32')
    return
  // The search path of the agents, which puts the install directory of a mise tool before its shim. A mise shim cannot
  // start a tool under the isolated HOME.
  const installed = findBinary('pi', { ...process.env, ...agentSearchPathEnv() })
  if (installed === null)
    return
  // The wrapper directory is first on the PATH of each agent. A wrapper that started itself would start itself again.
  if (realpathSync(dirname(installed)) === realpathSync(shimsDirectory))
    throw new Error(`The pi on PATH (${installed}) is the private wrapper itself. Remove ${shimsDirectory} from the PATH of the test run.`)
  const pi = quotePosixShellArgument(installed)
  writeFileSync(join(shimsDirectory, 'pi'), `#!/bin/sh\ncase "\${1-}" in\n  ''|-*) exec ${pi} --no-context-files "$@" ;;\nesac\nexec ${pi} "$@"\n`, { mode: 0o755 })
}

function piModels(options: PiEnvironmentOptions): Record<string, unknown> {
  const model = (id: string, name: string) => ({
    id,
    name,
    reasoning: true,
    input: ['text', 'image'],
    contextWindow: 128_000,
    maxTokens: 16_000,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  })
  return {
    providers: {
      [PI_PROVIDER_ID]: {
        baseUrl: options.baseURL,
        api: 'openai-completions',
        apiKey: options.modelKey,
        models: [model(options.modelID, 'GLM-5.3'), { ...model(options.flashModelID, 'GLM-5.3 Flash'), compat: { supportsReasoningEffort: true } }],
      },
    },
  }
}

function piPackagePaths(realHomeDir: string | undefined): string[] {
  if (!realHomeDir)
    return []
  const modules = join(realHomeDir, '.pi', 'agent', 'npm', 'node_modules')
  return [
    '@gotgenes/pi-nocd',
    '@juicesharp/rpiv-args',
    '@tintinweb/pi-subagents',
    '@juicesharp/rpiv-ask-user-question',
    '@narumitw/pi-plan-mode',
    'pi-goal-x',
    '@juicesharp/rpiv-todo',
  ].map(packageName => join(modules, packageName))
}
