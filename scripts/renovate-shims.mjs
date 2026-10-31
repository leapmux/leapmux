import { accessSync, chmodSync, constants, mkdirSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, isAbsolute, join, resolve } from 'node:path'
import process from 'node:process'
import { validateAuthorizedMap } from './renovate-exemptions.mjs'
import { CRATES_INDEX } from './renovate-registry.mjs'
import { publishTimeCutoff, releaseAgeMs } from './renovate-release-age.mjs'

// Renovate's getChildEnv gives customEnvVariables priority over the inherited environment,
// so this directory leads the PATH of every child process that Renovate starts.
const SHIMMED_TOOLS = Object.freeze(['bun', 'cargo'])

/** Return the paths of the generated shims under a trusted checkout. */
export function toolShimPaths(trusted) {
  if (!isAbsolute(trusted))
    throw new Error('Tool shims require an absolute trusted checkout.')
  const directory = resolve(trusted, '.tmp/renovate-shims')
  return {
    directory,
    bin: join(directory, 'bin'),
    policy: join(directory, 'policy.json'),
    invocationLog: join(directory, 'invocations.jsonl'),
  }
}

function isExecutableFile(path) {
  try {
    accessSync(path, constants.X_OK)
    return statSync(path).isFile()
  }
  catch {
    return false
  }
}

// Compare directories by their real path, so that a symlinked or differently cased spelling
// of the shim directory still matches it.
function canonicalDirectory(directory) {
  try {
    return realpathSync(directory)
  }
  catch {
    return resolve(directory)
  }
}

/** Find a tool on PATH, skipping the shim directory, so that a shim never calls itself. */
export function findRealTool(name, { path = process.env.PATH ?? '', exclude = [] } = {}) {
  const skipped = new Set(exclude.map(canonicalDirectory))
  for (const directory of path.split(delimiter)) {
    if (!directory || !isAbsolute(directory) || skipped.has(canonicalDirectory(directory)))
      continue
    const candidate = join(directory, name)
    if (isExecutableFile(candidate))
      return candidate
  }
  throw new Error(`Cannot find ${name} on PATH outside the Renovate tool shims.`)
}

const quote = value => `'${String(value).replaceAll('\'', '\'\\\'\'')}'`

/**
 * Write the shim policy and the POSIX shims for one Renovate run. The policy fixes the
 * publication cutoff at the start of the run, and copies the prepared authorized map after
 * it validates the map, because the map crossed a JSON file.
 */
export function writeToolShims({ root, policy, prepared, now = Date.now(), env = process.env, cratesIndex = CRATES_INDEX }) {
  // The shims are POSIX shell scripts. Renovate runs only on Linux.
  if (process.platform === 'win32')
    throw new Error('Renovate tool shims require a POSIX shell.')
  if (prepared?.authorized === undefined)
    throw new Error('The prepared file lacks the authorized fixes. Run task prepare-renovate first.')
  validateAuthorizedMap(prepared.authorized)
  const paths = toolShimPaths(root)
  const ageMs = releaseAgeMs(policy)
  // Delete the shims and the invocation records of an earlier run before the lookup
  // below, so that it can never find an earlier shim as the real tool.
  rmSync(paths.directory, { recursive: true, force: true })
  const realTools = Object.fromEntries(SHIMMED_TOOLS.map(tool => [
    tool,
    findRealTool(tool, {
      path: env.PATH,
      exclude: [paths.bin],
    }),
  ]))
  const document = {
    version: 1,
    cutoff: publishTimeCutoff(ageMs, now).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    // Bun reads whole seconds. Rounding up never admits a younger version.
    ageSeconds: Math.ceil(ageMs / 1000),
    realTools,
    authorized: prepared.authorized,
    cratesIndex,
    invocationLog: paths.invocationLog,
  }
  mkdirSync(paths.bin, { recursive: true })
  writeFileSync(paths.policy, `${JSON.stringify(document, null, 2)}\n`)
  const shim = resolve(import.meta.dirname, 'renovate-tool-shim.mjs')
  for (const tool of SHIMMED_TOOLS) {
    const file = join(paths.bin, tool)
    writeFileSync(file, `#!/bin/sh\nexec ${quote(realTools.bun)} ${quote(shim)} ${tool} ${quote(paths.policy)} "$@"\n`)
    chmodSync(file, 0o755)
  }
  return { ...paths, document }
}
