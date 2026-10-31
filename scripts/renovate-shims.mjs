import { accessSync, chmodSync, constants, mkdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, isAbsolute, join, resolve } from 'node:path'
import process from 'node:process'
import { publishTimeCutoff, releaseAgeMs } from './renovate-release-age.mjs'

// Renovate's getChildEnv gives customEnvVariables priority over the inherited environment,
// so this directory leads the PATH of every child process that Renovate starts.
export const SHIMMED_TOOLS = Object.freeze(['bun', 'cargo'])

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
    crates: join(directory, 'crates'),
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

/** Find a tool on PATH, skipping the shim directory, so that a shim never calls itself. */
export function findRealTool(name, { path = process.env.PATH ?? '', exclude = [] } = {}) {
  const skipped = new Set(exclude.map(directory => resolve(directory)))
  for (const directory of path.split(delimiter)) {
    if (!directory || !isAbsolute(directory) || skipped.has(resolve(directory)))
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
 * publication cutoff at the start of the run and copies the npm and crates roots of the
 * prepared authorized map.
 */
export function writeToolShims({ root, policy, prepared, now = Date.now(), env = process.env, cratesApi = 'https://crates.io/api/v1', maxRepairRounds = 10 }) {
  // The shims are POSIX shell scripts. Renovate runs only on Linux.
  if (process.platform === 'win32')
    throw new Error('Renovate tool shims require a POSIX shell.')
  if (!prepared?.authorized || typeof prepared.authorized !== 'object')
    throw new Error('The prepared file lacks the authorized fixes. Run task prepare-renovate first.')
  const paths = toolShimPaths(root)
  const ageMs = releaseAgeMs(policy)
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
    authorized: { npm: prepared.authorized.npm ?? {}, crates: prepared.authorized.crates ?? {} },
    cratesApi,
    maxRepairRounds,
    invocationLog: paths.invocationLog,
  }
  // A crate cache from an earlier run could hold a stale release list.
  rmSync(paths.directory, { recursive: true, force: true })
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
