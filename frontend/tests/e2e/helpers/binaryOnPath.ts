import type { Dirent } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { accessSync, constants, readdirSync, realpathSync, statSync } from 'node:fs'
import { basename, delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

/**
 * The executable file of this name that a spawn takes from a search path, found
 * WITHOUT running it, or null when the path holds none.
 *
 * Executable discovery runs in the Playwright process, which carries the developer's own
 * HOME. Some agents write into their configuration directory on every start, so a
 * check that ran `<binary> --version` there would write into the developer's own
 * configuration. This check reads the file system and runs nothing.
 *
 * The directories are tried in order and the first match wins, as a spawn does,
 * because a later match is not the file that the worker starts.
 *
 * `pathExt` is the Windows list of executable extensions (`PATHEXT`). Windows finds
 * `mimo.cmd` for `mimo`, so each extension is tried there beside the bare name.
 */
export function findBinaryOnPath(binary: string, searchPath: string | undefined, pathExt?: string): string | null {
  const extensions = [
    '',
    ...(pathExt ?? '').split(';').filter(extension => extension !== ''),
  ]
  for (const directory of (searchPath ?? '').split(delimiter)) {
    if (directory === '')
      continue
    for (const extension of extensions) {
      const path = join(directory, `${binary}${extension}`)
      if (isExecutableFile(path))
        return path
    }
  }
  return null
}

/**
 * The spelling of the .exe extension as PATHEXT states it, or '.exe' when PATHEXT
 * holds none. Windows resolves a bare name through PATHEXT, and the lookup reports
 * the spelling it probed with, so a controlled `letta.exe` on the path is found as
 * the `letta.EXE` that a UPPERCASE PATHEXT names.
 */
export function exeExtensionOnPath(pathExt: string | undefined = process.env.PATHEXT): string {
  return pathExt?.split(';').find(extension => extension.toLowerCase() === '.exe') ?? '.exe'
}

/**
 * The executable file of this name that a spawn in the E2E run takes, or null when
 * the run's search path holds none. The run's search path is
 * {@link agentSearchPath} of the PATH of env. See {@link findBinaryOnPath}.
 */
export function findBinary(binary: string, env: NodeJS.ProcessEnv = process.env): string | null {
  // A private environment that states PATH still resolves executables the way this
  // process does: PATHEXT is a system variable on Windows, and without it no name
  // resolves to its .exe file.
  const pathExt = process.platform === 'win32'
    ? env.PATHEXT ?? process.env.PATHEXT
    : undefined
  return findBinaryOnPath(binary, agentSearchPath(env.PATH), pathExt)
}

/**
 * Why the E2E run cannot start the CLI at this path, or null when nothing stops it.
 *
 * The one case is a mise shim. A mise shim is a link to the mise executable, and
 * mise reads its configuration and its trust records under HOME. The run gives each
 * agent an isolated HOME, where mise does not trust the developer's configuration:
 * mise 2026.9 stops with "Config files in ~/.config/mise/config.toml are not
 * trusted" and starts no tool. Discovery runs with the developer's own HOME,
 * where the same shim works, so a check that ran the shim would pass and the agent
 * start would then fail.
 *
 * {@link agentSearchPath} puts the real install directories before each directory
 * of shims, so the run finds a shim first only when mise does not list the tool.
 */
export function unusableBinaryReason(binary: string, path: string): string | null {
  if (miseExecutableOf(path) === null)
    return null
  return `The ${binary} on PATH (${path}) is a mise shim, and mise refuses to start it `
    + `under the isolated HOME of the E2E run. Put the directory that \`mise which ${binary}\` `
    + `prints first on PATH.`
}

/**
 * The executable name of a CLI, or the names that the worker probes for it in
 * preference order. The worker starts the first name that its shell finds, so a
 * lookup asks about the names in the same order.
 */
export type ExecutableNames = string | readonly string[]

/** Resolve the first executable that the Worker starts, or fail with its installation error. */
export function requireBinary(binary: ExecutableNames, reason: string, env: NodeJS.ProcessEnv = process.env): string {
  const names = typeof binary === 'string'
    ? [binary]
    : binary
  if (names.length === 0)
    throw new Error('A binary lookup needs at least one executable name.')
  for (const name of names) {
    const path = findBinary(name, env)
    if (path === null)
      continue
    const unusable = unusableBinaryReason(name, path)
    if (unusable !== null)
      throw new Error(`${asSentence(reason)} ${unusable}`)
    return path
  }
  throw new Error(reason)
}

/** End `text` with a period unless it already ends with a sentence mark. */
function asSentence(text: string): string {
  return /[.!?]$/.test(text)
    ? text
    : `${text}.`
}

/**
 * The directories that mise lists for the tools that its shims start, or null when
 * mise cannot list them.
 */
export type MiseBinPaths = (mise: string) => string[] | null

/** Each search path that {@link agentSearchPath} resolved with the real mise. */
const resolvedSearchPaths = new Map<string, string>()

/**
 * The search path that the E2E run gives the worker and every agent.
 *
 * It is the developer's own path, with the install directories of mise's tools put
 * before each directory of mise shims. A shim cannot start a tool under the run's
 * isolated HOME (see {@link unusableBinaryReason}), and the install directory is
 * what `mise activate` puts on PATH. So a shell that holds only the shims, such as
 * one that ran `mise activate --shims`, or one that ran `mise activate` before a
 * tool was installed, runs the same programs as a shell that mise activated after
 * the install.
 *
 * The shim directory stays after the new directories. A directory that holds a link
 * to mise beside other programs therefore keeps each of them.
 *
 * `binPaths` runs mise, the tool manager and no agent, in the Playwright process
 * with the developer's own HOME, where mise trusts its configuration. It runs once
 * for each distinct path. The path comes back unchanged when it holds no shim
 * directory, or when mise lists nothing.
 */
export function agentSearchPath(searchPath: string | undefined, binPaths: MiseBinPaths = miseBinPaths): string | undefined {
  if (!searchPath)
    return searchPath
  const cached = binPaths === miseBinPaths
    ? resolvedSearchPaths.get(searchPath)
    : undefined
  if (cached !== undefined)
    return cached
  const entries: string[] = []
  let changed = false
  for (const entry of searchPath.split(delimiter)) {
    const mise = entry === ''
      ? null
      : miseBehindShimDirectory(entry)
    const installs = mise === null
      ? null
      : binPaths(mise)
    if (installs !== null && installs.length > 0) {
      entries.push(...installs)
      changed = true
    }
    entries.push(entry)
  }
  const resolved = changed
    ? entries.join(delimiter)
    : searchPath
  if (binPaths === miseBinPaths)
    resolvedSearchPaths.set(searchPath, resolved)
  return resolved
}

/**
 * The `PATH` entry of the E2E run's environment: {@link agentSearchPath} of env's
 * PATH, or nothing when that is env's PATH already. Leaving the variable out keeps
 * the inherited spelling on Windows, where the name is `Path`.
 */
export function agentSearchPathEnv(env: NodeJS.ProcessEnv = process.env): Record<string, string> {
  const resolved = agentSearchPath(env.PATH)
  return resolved === undefined || resolved === env.PATH
    ? {}
    : { PATH: resolved }
}

/**
 * The repository root. mise reads the configuration of the directory that it runs
 * in and of each directory above it, and the run's working directories lie under
 * this root.
 */
const REPOSITORY_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../../../..')

/** Ask mise, at the repository root, for the install directory of each tool. */
function miseBinPaths(mise: string): string[] | null {
  try {
    const output = execFileSync(mise, ['bin-paths'], {
      cwd: REPOSITORY_ROOT,
      encoding: 'utf-8',
      stdio: ['ignore', 'pipe', 'ignore'],
      timeout: 30_000,
    })
    return output.split('\n').map(line => line.trim()).filter(line => line !== '')
  }
  catch {
    return null
  }
}

/**
 * The mise executable that this directory's shims start, or null when the
 * directory holds no mise shim.
 */
function miseBehindShimDirectory(directory: string): string | null {
  let entries: Dirent[]
  try {
    entries = readdirSync(directory, { withFileTypes: true })
  }
  catch {
    return null
  }
  // mise installs each shim as a link on macOS and Linux, so only a link needs the
  // resolution. A directory such as /usr/bin holds a thousand other files.
  for (const entry of entries) {
    if (!entry.isSymbolicLink())
      continue
    const mise = miseExecutableOf(join(directory, entry.name))
    if (mise !== null)
      return mise
  }
  return null
}

/**
 * The mise executable that the file at path starts when the file is a mise shim, or
 * null. A shim carries the name of its tool and resolves to mise, so the mise
 * executable itself is not a shim.
 */
function miseExecutableOf(path: string): string | null {
  let target: string
  try {
    target = realpathSync(path)
  }
  catch {
    return null
  }
  const name = basename(target).toLowerCase()
  if (name !== 'mise' && name !== 'mise.exe')
    return null
  return basename(path).toLowerCase() === name
    ? null
    : target
}

function isExecutableFile(path: string): boolean {
  try {
    if (!statSync(path).isFile())
      return false
    accessSync(path, constants.X_OK)
    return true
  }
  catch {
    return false
  }
}
