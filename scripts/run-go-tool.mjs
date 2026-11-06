import { spawnSync } from 'node:child_process'
import { constants } from 'node:os'
import { delimiter, isAbsolute, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { GO_TOOL_NAMES } from './go-tools.mjs'

const root = resolve(import.meta.dirname, '..')

function exitStatus(result) {
  if (result.error)
    throw result.error
  return result.status ?? (result.signal
    ? 128 + (constants.signals[result.signal] ?? 1)
    : 1)
}

/** Build with the tool's graph, then execute with the caller's directory and environment. */
export function runGoTool(name, args, { checkout = root, cwd = process.cwd(), env = process.env, run = spawnSync } = {}) {
  if (!GO_TOOL_NAMES.includes(name))
    throw new Error(`Unknown Go tool: ${name ?? '(missing)'}.`)
  // Application build flags can select another module file or cross-compile a binary
  // that cannot run here. They apply to the tool's work, never to its compilation.
  // An empty GOFLAGS falls back to the user's persisted Go settings.
  const buildEnv = { ...env, GOWORK: 'off', GOFLAGS: '-mod=readonly' }
  const host = run('go', [
    'env',
    '-json',
    'GOHOSTOS',
    'GOHOSTARCH',
    'GOROOT',
  ], {
    cwd: resolve(checkout, 'tools', name),
    env: buildEnv,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'inherit'],
  })
  const hostStatus = exitStatus(host)
  if (hostStatus !== 0)
    return hostStatus
  const { GOHOSTOS, GOHOSTARCH, GOROOT } = JSON.parse(host.stdout)
  const built = run('go', ['tool', '-n', name], {
    cwd: resolve(checkout, 'tools', name),
    env: { ...buildEnv, GOOS: GOHOSTOS, GOARCH: GOHOSTARCH },
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'inherit'],
  })
  const buildStatus = exitStatus(built)
  if (buildStatus !== 0)
    return buildStatus
  // No tool arguments follow the name, so stdout is one executable path, including spaces.
  const executable = built.stdout.trim()
  if (!isAbsolute(executable) || /[\r\n]/.test(executable))
    throw new Error(`Go returned an invalid executable path for ${name}.`)
  // Match go tool: subprocesses use the Go toolchain that compiled the tool.
  const pathKey = Object.keys(env).find(key => key.toLowerCase() === 'path') ?? 'PATH'
  return exitStatus(run(executable, args, {
    cwd,
    env: { ...env, [pathKey]: `${join(GOROOT, 'bin')}${delimiter}${env[pathKey] ?? ''}` },
    stdio: 'inherit',
  }))
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    process.exitCode = runGoTool(process.argv[2], process.argv.slice(3))
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
