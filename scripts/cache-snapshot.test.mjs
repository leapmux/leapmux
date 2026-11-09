import { spawnSync } from 'node:child_process'
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  symlinkSync,
  utimesSync,
  writeFileSync,
} from 'node:fs'
import { join, posix, win32 } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { activeRootsFor, cacheKey, snapshotPaths } from './cache-policy.mjs'
import {
  activateCacheSnapshot,
  cleanupSnapshotStaging,
  filesystemCapacity,
  isWithinDirectory,
  measureCacheTree,
  prepareCacheSnapshot,
  publishDependencySnapshot,
  trimAptArchives,
} from './cache-snapshot.mjs'
import { createSparseFile, scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('cache-snapshot')
const hash = 'a'.repeat(64)
const recipeHash = 'b'.repeat(64)

function runnerEnv(dir, extra = {}) {
  return {
    RUNNER_OS: 'Linux',
    RUNNER_ARCH: 'X64',
    CACHE_IMAGE: 'ubuntu24',
    GOLANG_VERSION: '1.27.1',
    RUST_VERSION: '1.99.0',
    BUN_VERSION: '1.4.4',
    TASK_VERSION: '3.46.0',
    RUNNER_TEMP: join(dir, 'runner-temp'),
    GITHUB_REF: 'refs/heads/main',
    GITHUB_EVENT_NAME: 'push',
    ...extra,
  }
}

function goEnv(dir, extra = {}) {
  return runnerEnv(dir, {
    GOCACHE: join(dir, 'go-build'),
    GOLANGCI_LINT_CACHE: '',
    ...extra,
  })
}

function writeTree(root, files) {
  for (const [path, content] of Object.entries(files)) {
    const target = join(root, path)
    mkdirSync(join(target, '..'), { recursive: true })
    writeFileSync(target, content)
  }
}

function readdirSafe(directory) {
  try {
    return readdirSync(directory).sort()
  }
  catch {
    return []
  }
}

/** A local archive transport that records which operations actually execute. */
function localTransport() {
  const operations = []
  return {
    operations,
    save(kind) {
      operations.push(`save:${kind}`)
    },
    prune() {
      operations.push('prune')
    },
  }
}

/** Drive the configured lifecycle: prepare, conditional save, prune gate, cleanup. */
async function runLifecycle(kind, key, prepareOptions) {
  const transport = localTransport()
  const prepared = await prepareCacheSnapshot({ kind, key, ...prepareOptions })
  if (prepared.result === 'ready')
    transport.save(kind, key)
  if (prepared.result === 'ready')
    transport.prune()
  cleanupSnapshotStaging({ kind, workspace: prepareOptions.workspace })
  transport.operations.push('cleanup')
  return { prepared, transport }
}

describe('cache snapshot core', () => {
  describe('isWithinDirectory', () => {
    it.each([
      [posix, '/workspace/cache', '/workspace/cache/payload'],
      [win32, 'D:\\workspace\\cache', 'D:\\workspace\\cache\\payload'],
    ])('accepts a child using the filesystem path rules', (paths, parent, child) => {
      expect(isWithinDirectory(parent, child, paths)).toBe(true)
      expect(isWithinDirectory(parent, parent, paths)).toBe(true)
      expect(isWithinDirectory(parent, `${parent}-other`, paths)).toBe(false)
      expect(isWithinDirectory(parent, paths.dirname(parent), paths)).toBe(false)
    })
  })
  describe('measureCacheTree', () => {
    it('counts file bytes and entries without following symbolic links', () => {
      const dir = fixture()
      const tree = join(dir, 'tree')
      writeTree(tree, {
        'zero.bin': '',
        'data.bin': '12345',
        'nested/deep.bin': '1234567',
      })
      const outside = join(dir, 'outside.txt')
      writeFileSync(outside, 'outside content')
      symlinkSync('data.bin', join(tree, 'relative.link'))
      symlinkSync(outside, join(tree, 'absolute.link'))
      const measured = measureCacheTree(tree)
      // The content bytes plus each link at its own size, never its target.
      expect(measured.files).toBe(5)
      expect(measured.bytes).toBe(12 + lstatSync(join(tree, 'relative.link')).size + lstatSync(join(tree, 'absolute.link')).size)
    })

    it('measures a single file and a large sparse file at its apparent size', () => {
      const dir = fixture()
      const file = join(dir, 'sparse.bin')
      writeFileSync(file, '')
      expect(measureCacheTree(file)).toEqual({ bytes: 0, files: 1, directories: 0 })
      createSparseFile(file, 1024 ** 4)
      expect(measureCacheTree(file).bytes).toBe(1024 ** 4)
    })
  })

  describe('filesystemCapacity', () => {
    it('keeps zero free bytes distinct from an unsupported inode count', () => {
      expect(filesystemCapacity('unused', () => ({
        bavail: 0,
        bsize: 4096,
        files: 0,
        ffree: 0,
      }))).toEqual({ freeBytes: 0, availableInodes: null })
      expect(filesystemCapacity('unused', () => ({
        bavail: 100,
        bsize: 4096,
        files: 100,
        ffree: 0,
      }))).toEqual({ freeBytes: 409600, availableInodes: 0 })
    })
    it('reports the free space and inodes of a fixture volume', () => {
      const capacity = filesystemCapacity(fixture())
      if (capacity.freeBytes !== null)
        expect(capacity.freeBytes).toBeGreaterThan(0)
      if (capacity.availableInodes !== null)
        expect(capacity.availableInodes).toBeGreaterThan(0)
    })
  })

  describe('activateCacheSnapshot', () => {
    it.each([
      'go-modules',
      'site-go-modules',
      'bun-packages',
      'cargo-downloads',
    ])('prepares an empty dependency graph after a writer miss', async (kind) => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        GOMODCACHE: join(dir, 'modules'),
        BUN_INSTALL_CACHE_DIR: join(dir, 'bun'),
        CARGO_HOME: join(dir, 'cargo'),
      })
      const key = cacheKey(kind, { hash }, env)
      await activateCacheSnapshot({
        kind,
        key,
        writer: true,
        workspace: dir,
        env,
      })
      for (const root of activeRootsFor(kind, env, dir)) expect(readdirSync(root.payload)).toEqual([])
      expect((await prepareCacheSnapshot({ kind, key, workspace: dir, env })).result).toBe('ready')
    })
    it('repairs a partial read-only module directory while merging a snapshot', async () => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        GOMODCACHE: join(dir, 'active'),
      })
      writeTree(join(env.GOMODCACHE, 'module'), { old: 'existing' })
      chmodSync(join(env.GOMODCACHE, 'module'), 0o555)
      writeTree(join(snapshotPaths('go-modules', dir).payloadRoot, 'gomod', 'module'), { current: 'restored' })
      await activateCacheSnapshot({
        kind: 'go-modules',
        key: cacheKey('go-modules', { hash }, env),
        matchedKey: cacheKey('go-modules', { hash }, env),
        workspace: dir,
        env,
      })
      expect(readFileSync(join(env.GOMODCACHE, 'module', 'current'), 'utf8')).toBe('restored')
    })
    it('activates an exact hit for a compiled kind by replacing the active root', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      mkdirSync(env.GOCACHE, { recursive: true })
      writeFileSync(join(env.GOCACHE, 'stale-entry'), 'stale\n')
      const { payloadRoot } = snapshotPaths('native-go', dir)
      writeTree(join(payloadRoot, 'gocache'), { 'current-entry': 'current\n' })
      const old = new Date('2026-01-01T00:00:00Z')
      utimesSync(join(payloadRoot, 'gocache', 'current-entry'), old, old)
      const activated = await activateCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        writer: true,
        matchedKey: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('activated-exact')
      expect(readFileSync(join(env.GOCACHE, 'current-entry'), 'utf8')).toBe('current\n')
      expect(existsSync(join(env.GOCACHE, 'stale-entry'))).toBe(false)
      // The payload of a compiled kind is dead after activation: the
      // same-filesystem move must have consumed the moved root.
      expect(existsSync(join(payloadRoot, 'gocache'))).toBe(false)
      expect(statMtime(join(env.GOCACHE, 'current-entry'))).toBe(old.getTime())
    })

    it('merges a dependency exact hit into the active downloads and keeps the payload', async () => {
      const dir = fixture()
      const active = join(dir, 'gomod-active')
      const env = runnerEnv(dir, { GOMODCACHE: active })
      mkdirSync(join(active, 'cache', 'download', 'other.mod', '@v'), { recursive: true })
      writeFileSync(join(active, 'cache', 'download', 'other.mod', '@v', 'v1.0.0.info'), 'other\n')
      const { payloadRoot } = snapshotPaths('go-modules', dir)
      writeTree(join(payloadRoot, 'gomod', 'cache', 'download', 'current.mod', '@v'), { 'v2.0.0.info': 'current\n' })
      const activated = await activateCacheSnapshot({
        kind: 'go-modules',
        key: cacheKey('go-modules', { hash }, env),
        writer: true,
        matchedKey: cacheKey('go-modules', { hash }, env),
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('activated-exact')
      expect(readFileSync(join(active, 'cache', 'download', 'current.mod', '@v', 'v2.0.0.info'), 'utf8')).toBe('current\n')
      // The active downloads that the restore did not bring survive.
      expect(readFileSync(join(active, 'cache', 'download', 'other.mod', '@v', 'v1.0.0.info'), 'utf8')).toBe('other\n')
      // The payload stays in place for the archive transport.
      expect(existsSync(join(payloadRoot, 'gomod', 'cache', 'download', 'current.mod'))).toBe(true)
    })

    it('starts a writer from empty owned roots on a miss', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      mkdirSync(env.GOCACHE, { recursive: true })
      writeFileSync(join(env.GOCACHE, 'inherited'), 'must not survive\n')
      const activated = await activateCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        writer: true,
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('writer-empty')
      expect(existsSync(env.GOCACHE)).toBe(true)
      expect(readdirSafe(env.GOCACHE)).toEqual([])
      // The writer owns an empty payload root for the population or the save.
      expect(readdirSafe(snapshotPaths('native-go', dir).payloadRoot)).toEqual([])
    })

    it('keeps the active dependency downloads of a reader miss', async () => {
      const dir = fixture()
      const active = join(dir, 'gomod-active')
      const env = runnerEnv(dir, { GOMODCACHE: active })
      mkdirSync(join(active, 'cache'), { recursive: true })
      writeFileSync(join(active, 'cache', 'kept'), 'kept\n')
      const activated = await activateCacheSnapshot({
        kind: 'go-modules',
        key: cacheKey('go-modules', { hash }, env),
        writer: false,
        matchedKey: null,
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('miss')
      expect(readFileSync(join(active, 'cache', 'kept'), 'utf8')).toBe('kept\n')
    })

    it('derives an exact hit from a matched key equal to the requested key, ignoring letter case', async () => {
      // actions/cache sets cache-hit when the matched key equals the key with
      // a case-insensitive comparison, so the matched key alone decides.
      for (const spell of [
        key => key,
        key => key.toUpperCase(),
      ]) {
        const dir = fixture()
        const env = goEnv(dir)
        const key = cacheKey('native-go', { hash, week: '2027-W01' }, env)
        writeTree(join(snapshotPaths('native-go', dir).payloadRoot, 'gocache'), { entry: 'exact\n' })
        const activated = await activateCacheSnapshot({
          kind: 'native-go',
          key,
          writer: true,
          matchedKey: spell(key),
          workspace: dir,
          env,
        })
        expect(activated.outcome).toBe('activated-exact')
        expect(activated.cacheHit).toBe(true)
        expect(activated.saveRequired).toBe(false)
      }
    })

    it('refuses a matched key that is not a key string', async () => {
      // The activation once took a boolean. A caller that still passes one
      // must fail, not lose its fallback in silence.
      const dir = fixture()
      const env = goEnv(dir)
      for (const matchedKey of [true, false, 1]) {
        await expect(activateCacheSnapshot({
          kind: 'native-go',
          key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
          writer: false,
          matchedKey,
          workspace: dir,
          env,
        })).rejects.toThrow('The matched cache key must be a key string or null.')
      }
    })

    it('activates a reader fallback but refuses one for a writer', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      const { payloadRoot } = snapshotPaths('native-go', dir)
      writeTree(join(payloadRoot, 'gocache'), { 'fallback-entry': 'fallback\n' })
      const key = cacheKey('native-go', { hash, week: '2027-W01' }, env)
      const fallbackKey = cacheKey('native-go', { hash: recipeHash, week: '2026-W44' }, env)
      const reader = await activateCacheSnapshot({
        kind: 'native-go',
        key,
        writer: false,
        matchedKey: fallbackKey,
        workspace: dir,
        env,
      })
      expect(reader.outcome).toBe('activated-fallback')
      expect(readFileSync(join(env.GOCACHE, 'fallback-entry'), 'utf8')).toBe('fallback\n')
      expect(existsSync(join(payloadRoot, 'gocache'))).toBe(false)
      const writerDir = fixture()
      const writerEnv = goEnv(writerDir)
      writeTree(join(snapshotPaths('native-go', writerDir).payloadRoot, 'gocache'), { entry: 'x\n' })
      await expect(activateCacheSnapshot({
        kind: 'native-go',
        key,
        writer: true,
        matchedKey: fallbackKey,
        workspace: writerDir,
        env: writerEnv,
      })).rejects.toThrow('must not restore a fallback')
    })

    it('ignores a restored archive whose key predates a key-shape change', async () => {
      // A kind that drops a key segment, as tauri-tools dropped its weekly
      // epoch, leaves archives whose keys extend the new key of the same
      // hash. The cache transport matches such a key as an extension of the
      // exact key; it must not become a fallback.
      const dir = fixture()
      const env = runnerEnv(dir, {
        HOME: join(dir, 'home'),
      })
      const key = cacheKey('tauri-tools', { hash }, env)
      const stale = `${key}-2026-W45`
      const { payloadRoot } = snapshotPaths('tauri-tools', dir)
      writeTree(join(payloadRoot, 'tauri'), { 'linuxdeploy-x86_64.AppImage': 'stale\n' })
      const logs = []
      const activated = await activateCacheSnapshot({
        kind: 'tauri-tools',
        key,
        writer: true,
        matchedKey: stale,
        workspace: dir,
        env,
        log: message => logs.push(message),
      })
      expect(activated.outcome).toBe('writer-empty')
      expect(activated.saveRequired).toBe(true)
      expect(logs.join('\n')).toContain(`Ignoring the restored tauri-tools archive ${stale}`)
      // The stale payload is gone and the writer starts from empty roots.
      expect(readdirSafe(snapshotPaths('tauri-tools', dir).payloadRoot)).toEqual([])
      expect(readdirSafe(activeRootsFor('tauri-tools', env, dir)[0].active)).toEqual([])
    })

    it('reports a stale-shape restore as a miss for a reader', async () => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        HOME: join(dir, 'home'),
      })
      const key = cacheKey('tauri-tools', { hash }, env)
      writeTree(join(snapshotPaths('tauri-tools', dir).payloadRoot, 'tauri'), { stale: 'stale\n' })
      const activated = await activateCacheSnapshot({
        kind: 'tauri-tools',
        key,
        writer: false,
        matchedKey: `${key}-2026-W45`,
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('miss')
      expect(activated.cacheHit).toBe(false)
    })

    it('ignores an archive that extends the exact key of a writer, even in the current key shape', async () => {
      // The transport tries the exact key, then any key that extends it. An
      // extra hash segment keeps such a key valid for a kind without a
      // weekly epoch, so only its position shows that no restore-keys
      // produced it.
      const dir = fixture()
      const env = runnerEnv(dir, {
        HOME: join(dir, 'home'),
      })
      const key = cacheKey('cli-tools', { hash }, env)
      const extension = `${key}-${recipeHash}`
      writeTree(join(snapshotPaths('cli-tools', dir).payloadRoot, 'tools'), { protoc: 'stale\n' })
      const logs = []
      const activated = await activateCacheSnapshot({
        kind: 'cli-tools',
        key,
        writer: true,
        matchedKey: extension,
        workspace: dir,
        env,
        log: message => logs.push(message),
      })
      expect(activated.outcome).toBe('writer-empty')
      expect(activated.saveRequired).toBe(true)
      expect(logs.join('\n')).toContain(`Ignoring the restored cli-tools archive ${extension}`)
      expect(readdirSafe(snapshotPaths('cli-tools', dir).payloadRoot)).toEqual([])
    })

    it('refuses a writer fallback from restore-keys in an older key shape', async () => {
      // A key that does not extend the exact key reaches a writer only
      // through restore-keys, which a writer must not pass, whatever the
      // shape of the matched key.
      const dir = fixture()
      const env = runnerEnv(dir, {
        HOME: join(dir, 'home'),
      })
      writeTree(join(snapshotPaths('tauri-tools', dir).payloadRoot, 'tauri'), { stale: 'stale\n' })
      await expect(activateCacheSnapshot({
        kind: 'tauri-tools',
        key: cacheKey('tauri-tools', { hash }, env),
        writer: true,
        matchedKey: `${cacheKey('tauri-tools', { hash: recipeHash }, env)}-2026-W45`,
        workspace: dir,
        env,
      })).rejects.toThrow('must not restore a fallback')
    })

    it('treats a partial restore as a miss even when the transport reported an exact hit', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      mkdirSync(env.GOCACHE, { recursive: true })
      writeFileSync(join(env.GOCACHE, 'inherited'), 'inherited\n')
      const logs = []
      const { payloadRoot } = snapshotPaths('native-go', dir)
      // An unusable payload: the required gocache root is absent.
      writeTree(join(payloadRoot, 'unrelated'), { entry: 'x\n' })
      const activated = await activateCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        writer: true,
        matchedKey: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        log: message => logs.push(message),
      })
      expect(activated.outcome).toBe('writer-empty')
      expect(logs.join('\n')).toContain('unusable payload')
      expect(existsSync(join(env.GOCACHE, 'inherited'))).toBe(false)
      // The unusable restore is gone; the writer owns an empty payload root.
      expect(existsSync(join(payloadRoot, 'unrelated'))).toBe(false)
    })

    it('restores the file roots of the Renovate engine into their workspace paths', async () => {
      const dir = fixture()
      const env = runnerEnv(dir)
      const { payloadRoot } = snapshotPaths('renovate-re2', dir)
      // For a file root the payload entry is the file itself.
      mkdirSync(payloadRoot, { recursive: true })
      writeFileSync(join(payloadRoot, 're2-node'), 'engine\n')
      writeFileSync(join(payloadRoot, 'task-checksum'), 'checksum\n')
      writeFileSync(join(payloadRoot, 'task-record'), 'record\n')
      const activated = await activateCacheSnapshot({
        kind: 'renovate-re2',
        key: cacheKey('renovate-re2', { hash: `${hash}-${recipeHash}`, week: '2027-W01' }, env),
        writer: true,
        matchedKey: cacheKey('renovate-re2', { hash: `${hash}-${recipeHash}`, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('activated-exact')
      const roots = activeRootsFor('renovate-re2', env, dir)
      expect(readFileSync(roots[0].active, 'utf8')).toBe('engine\n')
      expect(readFileSync(roots[1].active, 'utf8')).toBe('checksum\n')
      expect(readFileSync(roots[2].active, 'utf8')).toBe('record\n')
      // The payload of a non-dependency kind is consumed by the activation.
      for (const root of roots)
        expect(existsSync(root.payload), root.name).toBe(false)
    })

    it('never follows a symbolic link while it copies, and preserves both link forms', async () => {
      const dir = fixture()
      const active = join(dir, 'gomod-active')
      const env = runnerEnv(dir, { GOMODCACHE: active })
      const { payloadRoot } = snapshotPaths('go-modules', dir)
      writeTree(join(payloadRoot, 'gomod', 'cache'), { 'real.txt': 'real\n' })
      symlinkSync('real.txt', join(payloadRoot, 'gomod', 'cache', 'relative.link'))
      symlinkSync('/usr/bin/strip', join(payloadRoot, 'gomod', 'cache', 'external.link'))
      const externalTarget = readlinkSync(join(payloadRoot, 'gomod', 'cache', 'external.link'))
      const activated = await activateCacheSnapshot({
        kind: 'go-modules',
        key: cacheKey('go-modules', { hash }, env),
        writer: true,
        matchedKey: cacheKey('go-modules', { hash }, env),
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('activated-exact')
      const copied = join(active, 'cache')
      expect(readlinkSync(join(copied, 'relative.link'))).toBe('real.txt')
      expect(readFileSync(join(copied, 'relative.link'), 'utf8')).toBe('real\n')
      expect(readlinkSync(join(copied, 'external.link'))).toBe(externalTarget)
    })

    it('moves across volumes with a checked copy when a rename crosses a filesystem', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      const { payloadRoot } = snapshotPaths('native-go', dir)
      writeTree(join(payloadRoot, 'gocache'), { entry: 'x\n' })
      const logs = []
      const exdev = () => {
        throw Object.assign(new Error('cross-device link'), { code: 'EXDEV' })
      }
      const activated = await activateCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        writer: true,
        matchedKey: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        log: message => logs.push(message),
        fsImpl: {
          rename: exdev,
          capacity: () => ({ freeBytes: 2 ** 40, availableInodes: 1000 }),
        },
      })
      expect(activated.outcome).toBe('activated-exact')
      expect(readFileSync(join(env.GOCACHE, 'entry'), 'utf8')).toBe('x\n')
      expect(logs.join('\n')).toContain('across volumes')
      expect(existsSync(join(payloadRoot, 'gocache'))).toBe(false)
    })

    it('refuses a cross-volume copy when the destination volume lacks capacity', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      const { payloadRoot } = snapshotPaths('native-go', dir)
      writeTree(join(payloadRoot, 'gocache'), { entry: 'x\n' })
      const exdev = () => {
        throw Object.assign(new Error('cross-device link'), { code: 'EXDEV' })
      }
      await expect(activateCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        writer: true,
        matchedKey: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        fsImpl: {
          rename: exdev,
          capacity: () => ({ freeBytes: 0, availableInodes: 0 }),
        },
      })).rejects.toThrow('capacity')
      expect(readFileSync(join(payloadRoot, 'gocache', 'entry'), 'utf8')).toBe('x\n')
    })

    it('restores a snapshot whose paths contain spaces', async () => {
      const dir = fixture()
      const active = join(dir, 'go mod cache')
      const env = runnerEnv(dir, { GOMODCACHE: active })
      const { payloadRoot } = snapshotPaths('go-modules', dir)
      writeTree(join(payloadRoot, 'gomod', 'cache dir'), { 'entry with space.txt': 'spaced\n' })
      const activated = await activateCacheSnapshot({
        kind: 'go-modules',
        key: cacheKey('go-modules', { hash }, env),
        writer: true,
        matchedKey: cacheKey('go-modules', { hash }, env),
        workspace: dir,
        env,
      })
      expect(activated.outcome).toBe('activated-exact')
      expect(readFileSync(join(active, 'cache dir', 'entry with space.txt'), 'utf8')).toBe('spaced\n')
    })
  })

  describe('prepareCacheSnapshot', () => {
    it('checks capacity before creating a copy of a compiled cache', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      writeTree(env.GOCACHE, { entry: 'current\n' })
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        capacityImpl: () => {
          expect(existsSync(join(snapshotPaths('native-go', dir).payloadRoot, 'gocache', 'entry'))).toBe(false)
          return { freeBytes: 0, availableInodes: 0 }
        },
      })
      expect(prepared.result).toBe('skipped-capacity')
      expect(readFileSync(join(env.GOCACHE, 'entry'), 'utf8')).toBe('current\n')
    })

    it('stages regular file roots without turning them into directories', async () => {
      const dir = fixture()
      const env = runnerEnv(dir)
      const roots = activeRootsFor('renovate-re2', env, dir)
      for (const root of roots) writeTree(join(root.active, '..'), { [root.active.split(/[/\\]/).at(-1)]: root.name })
      const prepared = await prepareCacheSnapshot({
        kind: 'renovate-re2',
        key: cacheKey('renovate-re2', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(prepared.result).toBe('ready')
      for (const root of roots) expect(readFileSync(root.payload, 'utf8')).toBe(root.name)
    })
    it('fails an absent required output without publishing anything', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      // No GOCACHE directory exists.
      await expect(prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })).rejects.toThrow('absent')
      // Nothing was published: the reset staging payload stays empty.
      expect(existsSync(snapshotPaths('native-go', dir).payloadRoot)).toBe(true)
      expect(readdirSync(snapshotPaths('native-go', dir).payloadRoot)).toEqual([])
    })

    it('publishes an empty optional root and measures a required root', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      writeTree(env.GOCACHE, { 'build/aa/entry': 'x\n' })
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(prepared.result).toBe('ready')
      expect(prepared.archivePath).toBe(snapshotPaths('native-go', dir).payloadRoot)
      expect(prepared.report['files-after-trim']).toBe(1)
      expect(prepared.report['bytes-after-trim']).toBe(2)
      expect(prepared.report['free-bytes']).toBeNumber()
      expect(existsSync(join(prepared.archivePath, 'gocache', 'build', 'aa', 'entry'))).toBe(true)
    })

    it('validates a populated dependency payload without rebuilding it', async () => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        GOMODCACHE: join(dir, 'modules'),
      })
      const key = cacheKey('go-modules', { hash }, env)
      await expect(prepareCacheSnapshot({
        kind: 'go-modules',
        key,
        workspace: dir,
        env,
      })).rejects.toThrow('unusable')
      const { payloadRoot } = snapshotPaths('go-modules', dir)
      writeTree(join(payloadRoot, 'gomod'), { module: 'download' })
      const result = await prepareCacheSnapshot({
        kind: 'go-modules',
        key,
        workspace: dir,
        env,
      })
      expect(result.result).toBe('ready')
      expect(readFileSync(join(payloadRoot, 'gomod', 'module'), 'utf8')).toBe('download')
    })

    it('reports skipped-capacity and removes the staging payload when capacity is short', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      writeTree(env.GOCACHE, { entry: 'x\n' })
      const logs = []
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        log: message => logs.push(message),
        capacityImpl: () => ({ freeBytes: 0, availableInodes: Number.MAX_SAFE_INTEGER }),
      })
      expect(prepared.result).toBe('skipped-capacity')
      expect(prepared.archivePath).toBe('')
      expect(logs.join('\n')).toContain('Skipping the optional')
      expect(existsSync(snapshotPaths('native-go', dir).payloadRoot)).toBe(false)
    })

    it('reports skipped-capacity on an inode shortage', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      writeTree(env.GOCACHE, { entry: 'x\n' })
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        capacityImpl: () => ({ freeBytes: Number.MAX_SAFE_INTEGER, availableInodes: 0 }),
      })
      expect(prepared.result).toBe('skipped-capacity')
    })

    it('counts empty directories when checking inode capacity', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      mkdirSync(join(env.GOCACHE, 'empty', 'nested'), { recursive: true })
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
        capacityImpl: () => ({ freeBytes: Number.MAX_SAFE_INTEGER, availableInodes: 1 }),
      })
      expect(prepared.result).toBe('skipped-capacity')
    })

    it('skips a real publication when a large sparse payload exceeds the free space', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      writeTree(env.GOCACHE, { entry: 'x\n' })
      const capacity = filesystemCapacity(dir)
      expect(capacity.freeBytes, 'The fixture requires free-space reporting.').not.toBeNull()
      createSparseFile(join(env.GOCACHE, 'entry'), capacity.freeBytes * 4)
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(prepared.result).toBe('skipped-capacity')
    })

    it('refuses a writer scope that is not a main-branch push', async () => {
      const dir = fixture()
      const env = goEnv(dir, { GITHUB_REF: 'refs/pull/1/merge', GITHUB_EVENT_NAME: 'pull_request' })
      mkdirSync(env.GOCACHE, { recursive: true })
      writeFileSync(join(env.GOCACHE, 'entry'), 'x\n')
      await expect(prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })).rejects.toThrow('main-branch')
    })

    it('reports the compressed size as unavailable because the transport logs none', async () => {
      const dir = fixture()
      const env = goEnv(dir)
      mkdirSync(env.GOCACHE, { recursive: true })
      writeFileSync(join(env.GOCACHE, 'entry'), 'x\n')
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(prepared.report['compressed-size']).toBe('unavailable')
    })

    it('keeps two kinds that prepare concurrently independent', async () => {
      const dir = fixture()
      const goEnvironment = goEnv(dir)
      writeTree(goEnvironment.GOCACHE, { 'go-entry': 'go\n' })
      const mod = join(dir, 'gomod-active')
      const modEnv = runnerEnv(dir, { GOMODCACHE: mod })
      writeTree(join(mod, 'cache', 'download', 'current.mod', '@v'), { 'v1.info': 'x\n' })
      writeTree(join(snapshotPaths('go-modules', dir).payloadRoot, 'gomod', 'cache', 'download', 'current.mod', '@v'), { 'v2.info': 'x\n' })
      const [goPrepared, modPrepared] = await Promise.all([
        prepareCacheSnapshot({
          kind: 'native-go',
          key: cacheKey('native-go', { hash, week: '2027-W01' }, goEnvironment),
          workspace: dir,
          env: goEnvironment,
        }),
        prepareCacheSnapshot({
          kind: 'go-modules',
          key: cacheKey('go-modules', { hash }, modEnv),
          workspace: dir,
          env: modEnv,
        }),
      ])
      expect(goPrepared.result).toBe('ready')
      expect(modPrepared.result).toBe('ready')
      expect(goPrepared.archivePath).not.toBe(modPrepared.archivePath)
      expect(existsSync(join(goPrepared.archivePath, 'gocache', 'go-entry'))).toBe(true)
      expect(existsSync(join(modPrepared.archivePath, 'gomod', 'cache', 'download', 'current.mod'))).toBe(true)
    })
  })

  describe('cleanupSnapshotStaging', () => {
    it('removes only the staging root and never traverses a link out of it', () => {
      const dir = fixture()
      const precious = join(dir, 'precious')
      mkdirSync(precious, { recursive: true })
      writeFileSync(join(precious, 'target.txt'), 'kept\n')
      const { payloadRoot, stagingRoot } = snapshotPaths('native-go', dir)
      writeTree(join(payloadRoot, 'gocache'), { entry: 'x\n' })
      symlinkSync(precious, join(payloadRoot, 'gocache', 'link-to-outside'))
      cleanupSnapshotStaging({ kind: 'native-go', workspace: dir })
      expect(existsSync(stagingRoot)).toBe(false)
      expect(readFileSync(join(precious, 'target.txt'), 'utf8')).toBe('kept\n')
    })
  })

  describe('trimRustSnapshot', () => {
    it('trims the target tree to dependency artifacts of every profile and selects the sources that reuse requires', async () => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        CARGO_HOME: join(dir, 'cargo-home'),
      })
      // A real fixture workspace: the app crate plus two outside path
      // dependencies, so cargo metadata reports the dependency inventory
      // without network access. The second dependency lives where a Git
      // checkout would live, which exercises the checkout selection.
      writeTree(join(dir, 'desktop', 'rust'), {
        'src/lib.rs': 'pub fn value() -> u32 { 0 }\n',
        'Cargo.toml': [
          '[package]',
          'name = "app"',
          'version = "0.1.0"',
          'edition = "2021"',
          '',
          '[dependencies]',
          'deplib = { path = "../../deplib" }',
          'prefixdep = { path = "../rust-dependency" }',
          'git-sys = { path = "../../cargo-home/git/checkouts/myrepo/rev1" }',
          '',
        ].join('\n'),
      })
      writeTree(join(dir, 'desktop', 'rust-dependency'), {
        'Cargo.toml': '[package]\nname = "prefixdep"\nversion = "0.1.0"\nedition = "2021"\n',
        'src/lib.rs': 'pub fn value() -> u32 { 3 }\n',
      })
      writeTree(join(dir, 'deplib'), {
        'Cargo.toml': [
          '[package]',
          'name = "deplib"',
          'version = "0.2.0"',
          'edition = "2021"',
        ].join('\n'),
        'src/lib.rs': 'pub fn value() -> u32 { 1 }\n',
      })
      writeTree(join(dir, 'cargo-home', 'git', 'checkouts', 'myrepo', 'rev1'), {
        'Cargo.toml': [
          '[package]',
          'name = "git-sys"',
          'version = "0.3.0"',
          'edition = "2021"',
        ].join('\n'),
        'src/lib.rs': 'pub fn value() -> u32 { 2 }\n',
      })
      writeTree(join(dir, 'cargo-home', 'registry', 'src', 'index.crates.io-fixture'), {
        'git-sys-0.3.0/src/lib.rs': 'extracted source\n',
        'othercrate-9.9.9/src/lib.rs': 'not selected\n',
      })
      const target = join(dir, 'desktop', 'rust', 'target')
      writeTree(target, {
        'CACHEDIR.TAG': 'marker\n',
        'leapmux-app': 'final binary\n',
        'rustc-info.json': 'rustc\n',
        'debug/deps/libdeplib-abc123.rlib': 'dep artifact\n',
        'debug/deps/libprefixdep-abc123.rlib': 'external prefix dependency\n',
        'debug/deps/libapp-def456.rlib': 'workspace artifact\n',
        'debug/deps/git_sys-abc123.bin': 'dep artifact\n',
        'debug/incremental/cache.bin': 'incremental\n',
        'debug/.fingerprint/deplib-abc123/dep-lib': 'dep fingerprint\n',
        'debug/.fingerprint/app-def456/dep-app': 'workspace fingerprint\n',
        'debug/build/deplib-abc123/output': 'dep build output\n',
        'debug/build/git-sys-abc123/output': 'sys build output\n',
        'debug/build/app-def456/output': 'workspace build output\n',
        'release/deps/libdeplib-xyz789.rlib': 'release dep artifact\n',
        'release/leapmux-desktop': 'release binary\n',
        'release/bundle/app/app.AppImage': 'bundle\n',
        'tests/target/probe': 'nested test workspace\n',
        'tests/leftover': 'removed\n',
      })
      const { payloadRoot } = snapshotPaths('native-rust', dir)
      const logs = []
      // Preparation executes the trim rule and stages the payload.
      const prepared = await prepareCacheSnapshot({
        kind: 'native-rust',
        key: cacheKey('native-rust', { hash }, env),
        workspace: dir,
        env,
        log: message => logs.push(message),
      })
      expect(prepared.result).toBe('ready')
      expect(existsSync(join(payloadRoot, 'target', 'debug', 'deps', 'libprefixdep-abc123.rlib'))).toBe(true)
      // Trimmed in place: binaries, incremental data, workspace artifacts,
      // and bundles are gone; dependency artifacts of every profile remain.
      expect(existsSync(join(target, 'leapmux-app'))).toBe(false)
      expect(existsSync(join(target, 'rustc-info.json'))).toBe(false)
      expect(existsSync(join(target, 'CACHEDIR.TAG'))).toBe(true)
      expect(existsSync(join(target, 'debug', 'deps', 'libdeplib-abc123.rlib'))).toBe(true)
      expect(existsSync(join(target, 'debug', 'deps', 'libapp-def456.rlib'))).toBe(false)
      expect(existsSync(join(target, 'debug', 'deps', 'git_sys-abc123.bin'))).toBe(true)
      expect(existsSync(join(target, 'debug', 'incremental'))).toBe(false)
      expect(existsSync(join(target, 'debug', '.fingerprint', 'deplib-abc123'))).toBe(true)
      expect(existsSync(join(target, 'debug', '.fingerprint', 'app-def456'))).toBe(false)
      expect(existsSync(join(target, 'debug', 'build', 'deplib-abc123'))).toBe(true)
      expect(existsSync(join(target, 'debug', 'build', 'git-sys-abc123'))).toBe(true)
      expect(existsSync(join(target, 'debug', 'build', 'app-def456'))).toBe(false)
      expect(existsSync(join(target, 'release', 'deps', 'libdeplib-xyz789.rlib'))).toBe(true)
      expect(existsSync(join(target, 'release', 'leapmux-desktop'))).toBe(false)
      expect(existsSync(join(target, 'release', 'bundle'))).toBe(false)
      // The nested test workspace survives; its stray files do not.
      expect(existsSync(join(target, 'tests', 'target', 'probe'))).toBe(false)
      expect(existsSync(join(target, 'tests', 'leftover'))).toBe(false)
      // The payload holds the trimmed target plus the selected sources.
      expect(existsSync(join(payloadRoot, 'target', 'debug', 'deps', 'libdeplib-abc123.rlib'))).toBe(true)
      expect(existsSync(join(payloadRoot, 'registry-src', 'index.crates.io-fixture', 'git-sys-0.3.0', 'src', 'lib.rs'))).toBe(true)
      expect(existsSync(join(payloadRoot, 'registry-src', 'index.crates.io-fixture', 'othercrate-9.9.9'))).toBe(false)
      expect(existsSync(join(payloadRoot, 'git-checkouts', 'myrepo', 'rev1', 'Cargo.toml'))).toBe(true)
      expect(prepared.report['bytes-before-trim']).toBeGreaterThan(prepared.report['bytes-after-trim'])
      expect(logs.join('\n')).toContain('Trimming')
    })

    it('fails the preparation when the cargo inventory is unavailable, and publishes nothing', async () => {
      const dir = fixture()
      const env = runnerEnv(dir, {
        CARGO_HOME: join(dir, 'cargo-home'),
      })
      writeTree(join(dir, 'desktop', 'rust'), {
        // A broken workspace: no Cargo.toml at all.
        'not-a-manifest.txt': 'x\n',
      })
      await expect(prepareCacheSnapshot({
        kind: 'native-rust',
        key: cacheKey('native-rust', { hash }, env),
        workspace: dir,
        env,
      })).rejects.toThrow()
      // The reset staging payload holds no published target tree.
      expect(existsSync(join(snapshotPaths('native-rust', dir).payloadRoot, 'target'))).toBe(false)
    })
  })

  describe('trimAptArchives', () => {
    it('selects current installed versions even when installation needs no download', async () => {
      const dir = fixture()
      const archiveDir = join(dir, 'apt')
      writeTree(archiveDir, {
        'current.deb': 'current package',
        'old.deb': 'old version',
        'other-arch.deb': 'other architecture',
      })
      const options = {
        workspace: dir,
        env: runnerEnv(dir, { LEAPMUX_APT_ARCHIVE_DIR: archiveDir }),
        run: (command, args) => {
          if (command === 'dpkg-query')
            return { status: 0, stdout: 'libgtk-3-dev\t2.0\tamd64\tinstall ok installed\n' }
          if (command === 'dpkg-deb') {
            const file = args.at(-1)
            return {
              status: 0,
              stdout: file.endsWith('current.deb')
                ? 'libgtk-3-dev\t2.0\tamd64\n'
                : file.endsWith('old.deb')
                  ? 'libgtk-3-dev\t1.0\tamd64\n'
                  : 'libgtk-3-dev\t2.0\tarm64\n',
            }
          }
          return { status: 0, stdout: '0 upgraded, 0 newly installed\n' }
        },
      }
      expect(trimAptArchives(options).selected).toBe(1)
      const prepared = await prepareCacheSnapshot({
        ...options,
        kind: 'apt-packages',
        key: cacheKey('apt-packages', { hash, week: '2027-W01' }, options.env),
      })
      expect(prepared.result).toBe('ready')
      expect(readdirSync(join(snapshotPaths('apt-packages', dir).payloadRoot, 'apt-archives'))).toEqual(['current.deb'])
      expect(existsSync(join(archiveDir, 'old.deb'))).toBe(true)
    })
    it.skipIf(process.platform !== 'linux')('reads real archive metadata for an already-installed package', async () => {
      const dir = fixture()
      const archiveDir = join(dir, 'archives')
      const packageDir = join(dir, 'package')
      mkdirSync(join(packageDir, 'DEBIAN'), { recursive: true })
      mkdirSync(archiveDir)
      const installed = spawnSync('dpkg-query', ['--show', `--showformat=${['Version', 'Architecture'].map(field => `\${${field}}`).join('\t')}`, 'base-files'], { encoding: 'utf8' })
      expect(installed.status, installed.stderr).toBe(0)
      const [version, architecture] = installed.stdout.split('\t')
      writeFileSync(join(packageDir, 'DEBIAN', 'control'), `Package: base-files\nVersion: ${version}\nArchitecture: ${architecture}\nMaintainer: Fixture <fixture@example.invalid>\nDescription: Cache fixture\n`)
      const built = spawnSync('dpkg-deb', [
        '--build',
        '--root-owner-group',
        packageDir,
        join(archiveDir, 'current.deb'),
      ], { encoding: 'utf8' })
      expect(built.status, built.stderr).toBe(0)
      const env = runnerEnv(dir, { LEAPMUX_APT_ARCHIVE_DIR: archiveDir })
      const prepared = await prepareCacheSnapshot({
        kind: 'apt-packages',
        key: cacheKey('apt-packages', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      expect(prepared.result).toBe('ready')
      expect(readdirSync(join(prepared.archivePath, 'apt-archives'))).toEqual(['current.deb'])
    })
  })
})

describe('cache transport lifecycle', () => {
  it('saves the archive only after a ready preparation and cleans the staging in every outcome', async () => {
    const dir = fixture()
    const env = goEnv(dir)
    writeTree(env.GOCACHE, { entry: 'x\n' })
    const key = cacheKey('native-go', { hash, week: '2027-W01' }, env)
    const ready = await runLifecycle('native-go', key, { workspace: dir, env })
    expect(ready.prepared.result).toBe('ready')
    expect(ready.transport.operations).toEqual(['save:native-go', 'prune', 'cleanup'])
    expect(existsSync(snapshotPaths('native-go', dir).stagingRoot)).toBe(false)

    const skipDir = fixture()
    const skipEnv = goEnv(skipDir)
    writeTree(skipEnv.GOCACHE, { entry: 'x\n' })
    const skipped = await runLifecycle('native-go', key, {
      workspace: skipDir,
      env: skipEnv,
      capacityImpl: () => ({ freeBytes: 0, availableInodes: Number.MAX_SAFE_INTEGER }),
    })
    expect(skipped.prepared.result).toBe('skipped-capacity')
    expect(skipped.transport.operations).toEqual(['cleanup'])
    expect(existsSync(snapshotPaths('native-go', skipDir).stagingRoot)).toBe(false)
  })

  it('records no save and no prune when the preparation fails', async () => {
    const dir = fixture()
    const env = goEnv(dir)
    // The required build-output root is absent: preparation must fail.
    const transport = localTransport()
    let outcome
    try {
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: cacheKey('native-go', { hash, week: '2027-W01' }, env),
        workspace: dir,
        env,
      })
      outcome = prepared.result
    }
    catch {
      outcome = 'failed'
    }
    expect(outcome).toBe('failed')
    if (outcome === 'ready')
      transport.save('native-go', 'key')
    cleanupSnapshotStaging({ kind: 'native-go', workspace: dir })
    transport.operations.push('cleanup')
    expect(transport.operations).toEqual(['cleanup'])
    expect(existsSync(snapshotPaths('native-go', dir).stagingRoot)).toBe(false)
  })
})

describe('cache snapshot CLI', () => {
  const script = join(import.meta.dirname, 'cache-snapshot.mjs')

  function runCli(args, cwd, env) {
    return spawnSync(process.execPath, [script, ...args], {
      cwd,
      encoding: 'utf8',
      env: {
        ...process.env,
        ...env,
      },
    })
  }

  function outputRecords(path) {
    // A failed CLI writes no runner output at all, so a missing file means
    // no publishable result.
    const records = {}
    if (!existsSync(path))
      return records
    for (const line of readFileSync(path, 'utf8').split('\n').filter(Boolean)) {
      const index = line.indexOf('=')
      records[line.slice(0, index)] = line.slice(index + 1)
    }
    return records
  }

  it('writes the activation outcome and active path to GITHUB_OUTPUT', () => {
    const dir = fixture()
    const active = join(dir, 'gomod-active')
    const env = runnerEnv(dir, { GOMODCACHE: active })
    const key = cacheKey('go-modules', { hash }, env)
    const output = join(dir, 'github-output')
    const result = runCli(
      [
        'activate',
        '--kind',
        'go-modules',
        '--key',
        key,
        '--writer',
        'true',
      ],
      dir,
      { ...env, GITHUB_OUTPUT: output },
    )
    expect(result.status, result.stderr).toBe(0)
    const records = outputRecords(output)
    expect(records.outcome).toBe('writer-empty')
    expect(records['active-path']).toBe(active)
    expect(existsSync(active)).toBe(true)
    // The stdout copy mirrors the runner file for local runs.
    expect(result.stdout).toContain('outcome=writer-empty')
  })

  it('treats an empty matched key as no match', () => {
    const dir = fixture()
    const env = goEnv(dir)
    writeTree(join(snapshotPaths('native-go', dir).payloadRoot, 'gocache'), { entry: 'restored' })
    const output = join(dir, 'activation-output')
    const result = runCli([
      'activate',
      '--kind',
      'native-go',
      '--key',
      cacheKey('native-go', { hash, week: '2027-W01' }, env),
      '--writer',
      'false',
      '--matched-key',
      '',
    ], dir, { ...env, GITHUB_OUTPUT: output })
    expect(result.status, result.stderr).toBe(0)
    expect(outputRecords(output)).toMatchObject({ 'outcome': 'miss', 'cache-hit': 'false' })
    expect(`${result.stdout}${result.stderr}`).not.toContain('Ignoring the restored')
  })

  it('activates the fallback that the matched key identifies', () => {
    const dir = fixture()
    const env = goEnv(dir)
    writeTree(join(snapshotPaths('native-go', dir).payloadRoot, 'gocache'), { entry: 'restored' })
    const output = join(dir, 'activation-output')
    const result = runCli([
      'activate',
      '--kind',
      'native-go',
      '--key',
      cacheKey('native-go', { hash, week: '2027-W01' }, env),
      '--writer',
      'false',
      '--matched-key',
      cacheKey('native-go', {
        hash: 'b'.repeat(64),
        week: '2026-W52',
      }, env),
    ], dir, { ...env, GITHUB_OUTPUT: output })
    expect(result.status, result.stderr).toBe(0)
    expect(outputRecords(output)).toMatchObject({ 'outcome': 'activated-fallback', 'cache-hit': 'false', 'save-required': 'false' })
    expect(readFileSync(join(env.GOCACHE, 'entry'), 'utf8')).toBe('restored')
  })

  it('repopulates a partial exact hit without publishing over an occupied immutable key', () => {
    const dir = fixture()
    const env = runnerEnv(dir, {
      GOMODCACHE: join(dir, 'active'),
    })
    const key = cacheKey('go-modules', { hash }, env)
    writeTree(snapshotPaths('go-modules', dir).payloadRoot, { partial: 'missing gomod root' })
    const output = join(dir, 'activation-output')
    const result = runCli([
      'activate',
      '--kind',
      'go-modules',
      '--key',
      key,
      '--writer',
      'true',
      '--matched-key',
      key,
    ], dir, { ...env, GITHUB_OUTPUT: output })
    expect(result.status, result.stderr).toBe(0)
    expect(outputRecords(output)).toMatchObject({ 'outcome': 'writer-empty', 'cache-hit': 'false', 'save-required': 'false' })
    writeTree(join(snapshotPaths('go-modules', dir).payloadRoot, 'gomod'), { current: 'recovered' })
    publishDependencySnapshot({
      kind: 'go-modules',
      key,
      workspace: dir,
      env,
    })
    cleanupSnapshotStaging({ kind: 'go-modules', workspace: dir })
    expect(readFileSync(join(env.GOMODCACHE, 'current'), 'utf8')).toBe('recovered')
  })

  it('prepares through the CLI and cleans the staging afterwards', () => {
    const dir = fixture()
    const active = join(dir, 'gomod-active')
    const env = runnerEnv(dir, { GOMODCACHE: active })
    const key = cacheKey('go-modules', { hash }, env)
    const output = join(dir, 'github-output')
    // An isolated-download kind carries the producer output that the
    // population writes; without it, preparation must fail loudly and
    // expose no publishable result.
    const prepare = runCli([
      'prepare',
      '--kind',
      'go-modules',
      '--key',
      key,
    ], dir, {
      ...env,
      GITHUB_OUTPUT: output,
    })
    expect(prepare.status).not.toBe(0)
    expect(prepare.stderr).toContain('unusable')
    // No runner output exists: the failed step exposes no publishable result.
    expect(existsSync(output)).toBe(false)

    const payloadRoot = snapshotPaths('go-modules', dir).payloadRoot
    writeTree(join(payloadRoot, 'gomod', 'cache', 'download', 'current.mod', '@v'), { 'v1.info': 'x\n' })
    const ready = runCli([
      'prepare',
      '--kind',
      'go-modules',
      '--key',
      key,
    ], dir, {
      ...env,
      GITHUB_OUTPUT: output,
    })
    expect(ready.status, ready.stderr).toBe(0)
    const readyRecords = outputRecords(output)
    expect(readyRecords.result).toBe('ready')
    expect(readyRecords['archive-path']).toBe(payloadRoot)
    expect(readyRecords['compressed-size']).toBe('unavailable')

    const cleanup = runCli(['cleanup', '--kind', 'go-modules'], dir, env)
    expect(cleanup.status, cleanup.stderr).toBe(0)
    expect(existsSync(snapshotPaths('go-modules', dir).stagingRoot)).toBe(false)
  })

  it('validates keys and prints prefixes from the policy registry', () => {
    const dir = fixture()
    const env = runnerEnv(dir)
    const good = runCli([
      'validate-key',
      '--kind',
      'go-modules',
      '--key',
      cacheKey('go-modules', { hash }, env),
    ], dir, env)
    expect(good.status).toBe(0)
    const older = runCli([
      'validate-key',
      '--kind',
      'go-modules',
      '--key',
      `go-modules-1-Linux-${hash}`,
    ], dir, env)
    expect(older.status).not.toBe(0)
    const prefix = runCli(['prefix', '--kind', 'go-modules'], dir, env)
    expect(prefix.status).toBe(0)
    expect(prefix.stdout.trim()).toBe(`go-modules-2-Linux-`)
  })

  it('refuses an unknown kind, an unknown command, and a missing kind', () => {
    const dir = fixture()
    expect(runCli(['prefix', '--kind', 'unknown'], dir, {}).status).not.toBe(0)
    expect(runCli(['nonsense', '--kind', 'go-modules'], dir, {}).status).not.toBe(0)
    expect(runCli(['prefix'], dir, {}).status).not.toBe(0)
  })
})

describe('cache dependency lifecycle', () => {
  it('checks the active volume before copying mandatory downloads', () => {
    const dir = fixture()
    const env = runnerEnv(dir, {
      GOMODCACHE: join(dir, 'active'),
    })
    writeTree(join(snapshotPaths('go-modules', dir).payloadRoot, 'gomod'), { current: 'download' })
    expect(() => publishDependencySnapshot({
      kind: 'go-modules',
      key: cacheKey('go-modules', { hash }, env),
      workspace: dir,
      env,
      capacityImpl: () => ({ freeBytes: 0, availableInodes: 0 }),
    })).toThrow('capacity')
    expect(existsSync(join(env.GOMODCACHE, 'current'))).toBe(false)
  })
  it('archives only the producer files of the current declarations beside inherited active downloads', async () => {
    const dir = fixture()
    const active = join(dir, 'gomod-active')
    const env = runnerEnv(dir, { GOMODCACHE: active })
    const key = cacheKey('go-modules', { hash }, env)
    // A writer missed its exact key: activation empties the payload but keeps
    // the active downloads, which still hold an obsolete module.
    writeTree(join(active, 'cache', 'download', 'obsolete.mod', '@v', 'v1.0.0'), { 'v1.0.0.info': 'obsolete\n' })
    writeTree(join(active, 'cache', 'download', 'current.mod', '@v', 'v2.0.0'), { 'v2.0.0.info': 'current\n' })
    const activated = await activateCacheSnapshot({
      kind: 'go-modules',
      key,
      writer: true,
      workspace: dir,
      env,
    })
    expect(activated.outcome).toBe('writer-empty')
    const { payloadRoot } = snapshotPaths('go-modules', dir)
    expect(existsSync(payloadRoot)).toBe(true)
    // The population downloads the current declarations into the isolated
    // payload root and nothing else.
    writeTree(join(payloadRoot, 'gomod', 'cache', 'download', 'current.mod', '@v', 'v2.0.0'), { 'v2.0.0.info': 'current\n' })
    // Immediate publication copies the payload into the active downloads
    // without removing anything, so later consumers keep reading them.
    publishDependencySnapshot({
      kind: 'go-modules',
      key,
      workspace: dir,
      env,
    })
    expect(existsSync(join(active, 'cache', 'download', 'obsolete.mod'))).toBe(true)
    const prepared = await prepareCacheSnapshot({
      kind: 'go-modules',
      key,
      workspace: dir,
      env,
    })
    expect(prepared.result).toBe('ready')
    expect(readdirSafe(join(payloadRoot, 'gomod', 'cache', 'download'))).toEqual(['current.mod'])
    cleanupSnapshotStaging({ kind: 'go-modules', workspace: dir })
    expect(existsSync(snapshotPaths('go-modules', dir).stagingRoot)).toBe(false)
    // The consumer of this job still reads the active dependency.
    expect(readFileSync(join(active, 'cache', 'download', 'current.mod', '@v', 'v2.0.0', 'v2.0.0.info'), 'utf8')).toBe('current\n')
  })

  it('fails the publication when the isolated producer root is absent', async () => {
    const dir = fixture()
    const env = runnerEnv(dir, {
      GOMODCACHE: join(dir, 'gomod-active'),
    })
    mkdirSync(env.GOMODCACHE, { recursive: true })
    const key = cacheKey('go-modules', { hash }, env)
    expect(() => publishDependencySnapshot({
      kind: 'go-modules',
      key,
      workspace: dir,
      env,
    })).toThrow('did not complete')
    await expect(prepareCacheSnapshot({
      kind: 'go-modules',
      key,
      workspace: dir,
      env,
    })).rejects.toThrow('unusable')
  })

  it('refuses the publication for a kind that is not isolated-download', () => {
    const dir = fixture()
    const env = goEnv(dir)
    const key = cacheKey('native-go', { hash, week: '2027-W01' }, env)
    expect(() => publishDependencySnapshot({
      kind: 'native-go',
      key,
      workspace: dir,
      env,
    })).toThrow('isolated-download')
  })
})

function statMtime(path) {
  return lstatSync(path).mtimeMs
}
