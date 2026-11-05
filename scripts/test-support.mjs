import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { chmodSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, truncateSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { CRATES_INDEX } from './renovate-registry.mjs'

// The shared fixtures of the script tests. bun test runs only `*.test.*` and `*.spec.*`
// files, so it never runs this module as a test.

const root = resolve(import.meta.dirname, '..')

/** Create a zero-filled sparse fixture without allocating its logical length. */
export function createSparseFile(file, size) {
  if (!Number.isSafeInteger(size) || size < 0)
    throw new RangeError('The sparse fixture size must be a non-negative safe integer.')
  const sparseFile = resolve(file)
  if (process.platform === 'win32') {
    // Windows files need FSCTL_SET_SPARSE before extension. fsutil requires
    // administrator privileges, so the helper uses the owned file handle.
    const result = spawnSync('powershell.exe', [
      '-NoLogo',
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      readFileSync(join(import.meta.dirname, 'test-support', 'create-sparse-file.ps1'), 'utf8'),
    ], {
      encoding: 'utf8',
      env: {
        ...process.env,
        TEMP: dirname(sparseFile),
        TMP: dirname(sparseFile),
        LEAPMUX_TEST_SPARSE_FILE: sparseFile,
        LEAPMUX_TEST_SPARSE_SIZE: String(size),
      },
    })
    if (result.error || result.status !== 0)
      throw new Error(`Cannot create sparse fixture ${sparseFile}: ${result.error?.message || result.stderr?.trim() || result.stdout?.trim() || `exit ${result.status}`}`)
    return
  }
  writeFileSync(sparseFile, '')
  truncateSync(sparseFile, size)
}

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
      for (const directory of created.splice(0)) {
        try {
          rmSync(directory, { recursive: true, force: true })
        }
        catch {
          // Tool caches mark directories read-only, and removing an entry
          // needs a writable parent. Restore the owner's write bit on every
          // directory and remove again.
          const stack = [directory]
          while (stack.length > 0) {
            const current = stack.pop()
            let stat
            try {
              stat = lstatSync(current)
            }
            catch {
              continue
            }
            if (stat.isSymbolicLink() || !stat.isDirectory())
              continue
            try {
              chmodSync(current, stat.mode | 0o200)
            }
            catch {}
            for (const entry of readdirSync(current, { withFileTypes: true }))
              stack.push(join(current, entry.name))
          }
          rmSync(directory, { recursive: true, force: true })
        }
      }
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

// --- A minimal ZIP writer for the Go module proxy fixture ------------------

const CRC32_TABLE = (() => {
  const table = new Int32Array(256)
  for (let n = 0; n < 256; n++) {
    let c = n
    for (let k = 0; k < 8; k++) {
      c = (c & 1) !== 0
        ? 0xEDB88320 ^ (c >>> 1)
        : c >>> 1
    }
    table[n] = c
  }
  return table
})()

function crc32(buffer) {
  let crc = -1
  for (const byte of buffer)
    crc = CRC32_TABLE[(crc ^ byte) & 0xFF] ^ (crc >>> 8)
  return (crc ^ -1) >>> 0
}

/** Write a stored (uncompressed) ZIP archive of `{name: content}` entries. */
export function createStoredZip(files) {
  const entries = Object.entries(files)
  const chunks = []
  const central = []
  let offset = 0
  for (const [name, content] of entries) {
    const data = Buffer.isBuffer(content)
      ? content
      : Buffer.from(content)
    const nameBytes = Buffer.from(name)
    const crc = crc32(data)
    const local = Buffer.alloc(30 + nameBytes.length)
    local.writeUInt32LE(0x04034B50, 0)
    local.writeUInt16LE(20, 4)
    local.writeUInt16LE(0, 6)
    local.writeUInt16LE(0, 8)
    local.writeUInt16LE(0, 10)
    local.writeUInt16LE(0, 12)
    local.writeUInt32LE(crc, 14)
    local.writeUInt32LE(data.length, 18)
    local.writeUInt32LE(data.length, 22)
    local.writeUInt16LE(nameBytes.length, 26)
    local.writeUInt16LE(0, 28)
    nameBytes.copy(local, 30)
    chunks.push(local, data)
    const entry = Buffer.alloc(46 + nameBytes.length)
    entry.writeUInt32LE(0x02014B50, 0)
    entry.writeUInt16LE(20, 4)
    entry.writeUInt16LE(20, 6)
    entry.writeUInt16LE(0, 8)
    entry.writeUInt16LE(0, 10)
    entry.writeUInt16LE(0, 12)
    entry.writeUInt16LE(0, 14)
    entry.writeUInt32LE(crc, 16)
    entry.writeUInt32LE(data.length, 20)
    entry.writeUInt32LE(data.length, 24)
    entry.writeUInt16LE(nameBytes.length, 28)
    entry.writeUInt16LE(0, 30)
    entry.writeUInt16LE(0, 32)
    entry.writeUInt16LE(0, 34)
    entry.writeUInt16LE(0, 36)
    entry.writeUInt32LE(0, 38)
    entry.writeUInt32LE(offset, 42)
    nameBytes.copy(entry, 46)
    central.push(entry)
    offset += local.length + data.length
  }
  const directory = Buffer.concat(central)
  const end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054B50, 0)
  end.writeUInt16LE(0, 4)
  end.writeUInt16LE(0, 6)
  end.writeUInt16LE(entries.length, 8)
  end.writeUInt16LE(entries.length, 10)
  end.writeUInt32LE(directory.length, 12)
  end.writeUInt32LE(offset, 16)
  end.writeUInt16LE(0, 20)
  return Buffer.concat([...chunks, directory, end])
}
