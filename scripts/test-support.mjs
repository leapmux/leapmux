import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { CRATES_INDEX } from './renovate-registry.mjs'

// The shared fixtures of the script tests. bun test runs only `*.test.*` and `*.spec.*`
// files, so it never runs this module as a test.

const root = resolve(import.meta.dirname, '..')

/**
 * Return a factory of scratch directories under `.tmp` of the checkout, and the cleanup
 * that removes every directory that it created. A test file registers the cleanup with
 * `afterEach(scratch.cleanup)`.
 */
export function scratchDirectories() {
  const created = []
  return {
    create(prefix) {
      mkdirSync(join(root, '.tmp'), { recursive: true })
      const directory = mkdtempSync(join(root, '.tmp', `${prefix}-`))
      created.push(directory)
      return directory
    },
    cleanup() {
      for (const directory of created.splice(0))
        rmSync(directory, { recursive: true, force: true })
    },
  }
}

/** Write each file of `files` under `directory`, creating its parents. A null content removes the file. */
export function writeFiles(directory, files) {
  for (const [path, content] of Object.entries(files)) {
    const file = resolve(directory, path)
    if (content === null) {
      rmSync(file, { force: true })
      continue
    }
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, content)
  }
}

/** Run git in `directory`, return its trimmed standard output, and fail on a nonzero status. */
export function git(directory, args) {
  const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
  if (result.error || result.status !== 0)
    throw new Error(`git ${args.join(' ')} failed: ${result.error?.message ?? result.stderr.trim()}`)
  return result.stdout.trim()
}

// A global `core.fsmonitor=true` starts a daemon that outlives git and writes under `.git/`,
// which races the removal of the scratch directory. The configuration of the repository
// pins it off for every later git command, as `-c` on one command cannot. The empty hooks
// directory keeps the hooks of the user's own configuration out of the fixture.
const PINNED_CONFIG = Object.freeze({
  'core.fsmonitor': 'false',
  'gc.auto': '0',
  'maintenance.auto': 'false',
  'commit.gpgsign': 'false',
  'tag.gpgsign': 'false',
  'user.name': 'Fixture',
  'user.email': 'fixture@example.invalid',
})

/**
 * Create a git repository on `main` in `directory` with the pinned configuration and the
 * entries of `config`. A repository that is not bare also gets `files`, committed as
 * `base`. Return the commit of `main`, or null for a bare repository.
 */
export function gitRepository(directory, { files = {}, config = {}, bare = false } = {}) {
  mkdirSync(directory, { recursive: true })
  git(directory, [
    'init',
    '--quiet',
    '--initial-branch=main',
    ...(bare
      ? ['--bare']
      : []),
  ])
  const hooks = join(bare
    ? directory
    : join(directory, '.git'), 'no-hooks')
  mkdirSync(hooks)
  for (const [key, value] of Object.entries({
    ...PINNED_CONFIG,
    'core.hooksPath': hooks,
    ...config,
  }))
    git(directory, ['config', key, value])
  if (bare)
    return null
  writeFiles(directory, files)
  git(directory, ['add', '--all'])
  git(directory, [
    'commit',
    '--quiet',
    '--allow-empty',
    '--message',
    'base',
  ])
  return git(directory, ['rev-parse', 'HEAD'])
}

/** Return whether a registry read asks the crates.io sparse index for a crate file. */
export function isCrateIndexRead(url) {
  return url.startsWith(`${CRATES_INDEX}/`)
}

/** Answer a crates.io sparse index read with one line for each `{ vers, pubtime, yanked }` entry. */
export function crateIndexResponse(entries) {
  return new Response(entries.map(entry => JSON.stringify(entry)).join('\n'))
}
