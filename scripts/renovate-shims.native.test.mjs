import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { gzipSync } from 'node:zlib'
import { afterEach, describe, expect, it } from 'bun:test'
import { lockedVersions } from './renovate-lockfile-versions.mjs'
import { toolShimPaths, writeToolShims } from './renovate-shims.mjs'
import { git, gitRepository, scratchDirectories, writeFiles } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
const servers = []
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true)
  scratch.cleanup()
})

const old = '2026-01-01T00:00:00.000Z'
const young = () => new Date(Date.now() - 3600000).toISOString()

const fixture = () => scratch.create('renovate-shims-native')

// The test process serves the registries, so every tool runs asynchronously.
async function run(command, args, { cwd, env }) {
  const child = Bun.spawn([command, ...args], {
    cwd,
    env,
    stdout: 'pipe',
    stderr: 'pipe',
  })
  const [stdout, stderr, status] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ])
  return { status, stdout, stderr }
}

// Commit every change of the repository as a new base.
function commit(directory) {
  git(directory, ['add', '--all'])
  git(directory, [
    'commit',
    '--quiet',
    '--message',
    'base lockfile',
  ])
}

// The prepared map keys the roots of each lockfile by its path in the repository.
function shims(directory, { authorized = {}, cratesIndex } = {}) {
  writeToolShims({
    root: directory,
    policy: { minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: null },
    prepared: { authorized },
    env: { PATH: process.env.PATH },
    ...(cratesIndex
      ? { cratesIndex }
      : {}),
  })
  return toolShimPaths(directory)
}

function invocations(paths) {
  return existsSync(paths.invocationLog)
    ? readFileSync(paths.invocationLog, 'utf8').trim().split('\n').map(line => JSON.parse(line))
    : []
}
const identities = versions => [...versions].flatMap(([name, set]) => [...set].map(version => `${name}@${version}`)).sort()

function tarball(files) {
  const blocks = []
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content)
    const header = Buffer.alloc(512)
    header.write(name, 0, 100)
    header.write('0000644\0', 100)
    header.write('0000000\0', 108)
    header.write('0000000\0', 116)
    header.write(`${data.length.toString(8).padStart(11, '0')}\0`, 124)
    header.write('00000000000\0', 136)
    header.write('        ', 148)
    header.write('0', 156)
    header.write('ustar\0', 257)
    header.write('00', 263)
    let sum = 0
    for (const byte of header)
      sum += byte
    header.write(`${sum.toString(8).padStart(6, '0')}\0 `, 148)
    blocks.push(header, data, Buffer.alloc((512 - data.length % 512) % 512))
  }
  blocks.push(Buffer.alloc(1024))
  return gzipSync(Buffer.concat(blocks))
}

// An npm registry whose packuments carry the publication time of each version.
function npmRegistry(packages) {
  const tarballs = new Map()
  const server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch(request) {
      const path = decodeURIComponent(new URL(request.url).pathname).slice(1)
      if (tarballs.has(path))
        return new Response(tarballs.get(path))
      const versions = packages[path]
      if (!versions)
        return Response.json({ error: 'not found' }, { status: 404 })
      const body = {
        'name': path,
        'dist-tags': {
          latest: Object.keys(versions).at(-1),
        },
        'time': { created: old, modified: old },
        'versions': {},
      }
      for (const [version, { time, dependencies = {} }] of Object.entries(versions)) {
        const file = `${path}/-/${path.split('/').at(-1)}-${version}.tgz`
        const data = tarball({
          'package/package.json': JSON.stringify({ name: path, version, dependencies }),
        })
        tarballs.set(file, data)
        body.time[version] = time
        body.versions[version] = {
          name: path,
          version,
          dependencies,
          dist: {
            tarball: `http://127.0.0.1:${server.port}/${file}`,
            shasum: createHash('sha1').update(data).digest('hex'),
            integrity: `sha512-${createHash('sha512').update(data).digest('base64')}`,
          },
        }
      }
      return Response.json(body)
    },
  })
  servers.push(server)
  return `http://127.0.0.1:${server.port}/`
}

function crateIndexPath(name) {
  return name.length <= 2
    ? `${name.length}/${name}`
    : name.length === 3
      ? `3/${name[0]}/${name}`
      : `${name.slice(0, 2)}/${name.slice(2, 4)}/${name}`
}

// A sparse index whose entries carry the publication time of each version, as the
// crates.io index does. The test changes `crates` between the base and the head, as a
// registry gains releases after main locks its versions. `index` is the source that cargo
// reads, and `shimIndex` is the same index as the shim reads it.
function crateRegistry(crates) {
  const server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch(request) {
      const path = new URL(request.url).pathname
      const base = `http://127.0.0.1:${server.port}`
      if (path === '/index/config.json')
        return Response.json({ dl: `${base}/dl`, api: base })
      for (const [name, versions] of Object.entries(crates)) {
        if (path === `/index/${crateIndexPath(name)}`) {
          return new Response(Object.entries(versions).map(([vers, { time, deps = {} }]) => JSON.stringify({
            name,
            vers,
            deps: Object.entries(deps).map(([dependency, req]) => ({
              name: dependency,
              req,
              features: [],
              optional: false,
              default_features: true,
              target: null,
              kind: 'normal',
            })),
            cksum: '0'.repeat(64),
            features: {},
            yanked: false,
            // crates.io writes whole seconds, and cargo rejects an entry with fractional seconds.
            pubtime: new Date(time).toISOString().replace(/\.\d{3}Z$/, 'Z'),
          })).join('\n'))
        }
      }
      return new Response('not found', { status: 404 })
    },
  })
  servers.push(server)
  return { index: `sparse+http://127.0.0.1:${server.port}/index/`, shimIndex: `http://127.0.0.1:${server.port}/index` }
}

function toolEnvironment(directory, paths, extra = {}) {
  const env = {
    ...process.env,
    PATH: `${paths.bin}${delimiter}${process.env.PATH}`,
    NO_COLOR: '1',
    ...extra,
  }
  for (const key of Object.keys(env)) {
    if (/^(?:npm_config_|bun_config_)/i.test(key))
      delete env[key]
  }
  return env
}

// The shims are POSIX shell scripts. Renovate runs only on Linux.
describe.skipIf(process.platform === 'win32')('the generated tool shims with real tools', () => {
  it('starts under Node and writes shims that run the real Bun', async () => {
    const directory = fixture()
    writeFiles(directory, {
      'frontend/bun.lock': readFileSync(resolve(root, 'frontend/bun.lock')),
      'desktop/rust/Cargo.lock': readFileSync(resolve(root, 'desktop/rust/Cargo.lock')),
    })
    const source = `const {writeToolShims}=await import(${JSON.stringify(new URL('./renovate-shims.mjs', import.meta.url).href)}); const {readFileSync}=await import('node:fs'); writeToolShims({root:process.argv[1],policy:JSON.parse(readFileSync(process.argv[2],'utf8')),prepared:{authorized:{'desktop/rust/Cargo.lock':{'glib@0.20.0':[]}}}}); process.stdout.write(process.versions.bun??'node')`
    const started = spawnSync('node', [
      '--input-type=module',
      '-e',
      source,
      directory,
      resolve(root, 'renovate.json'),
    ], { encoding: 'utf8' })
    expect(started.status, started.stderr).toBe(0)
    expect(started.stdout).toBe('node')
    const paths = toolShimPaths(directory)
    const policy = JSON.parse(readFileSync(paths.policy, 'utf8'))
    expect(policy).toEqual({
      version: 1,
      cutoff: expect.stringMatching(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/),
      ageSeconds: 259200,
      realTools: {
        bun: Bun.which('bun'),
        cargo: Bun.which('cargo'),
      },
      authorized: {
        'desktop/rust/Cargo.lock': { 'glib@0.20.0': [] },
      },
      cratesIndex: 'https://index.crates.io',
      invocationLog: paths.invocationLog,
    })
    for (const tool of ['bun', 'cargo']) {
      const shim = join(paths.bin, tool)
      expect(statSync(shim).mode & 0o777, tool).toBe(0o755)
      expect(readFileSync(shim, 'utf8'), tool).toBe(`#!/bin/sh\nexec '${Bun.which('bun')}' '${resolve(root, 'scripts/renovate-tool-shim.mjs')}' ${tool} '${paths.policy}' "$@"\n`)
    }
    const version = await run(join(paths.bin, 'bun'), ['--version'], {
      cwd: join(directory, 'frontend'),
      env: toolEnvironment(directory, paths),
    })
    expect(version.status, version.stderr).toBe(0)
    expect(version.stdout.trim()).toBe(Bun.version)
    expect(invocations(paths)).toEqual([
      expect.objectContaining({
        tool: 'bun',
        args: ['--version'],
        effectiveArgs: ['--version'],
        exitCode: 0,
      }),
    ])
  })

  describe('bun', () => {
    const packages = () => {
      const recent = young()
      return {
        'x': {
          '1.0.0': {
            time: old,
            dependencies: { 'x-core': '1.0.0' },
          },
          '1.1.0': {
            time: recent,
            dependencies: { 'x-core': '1.1.0' },
          },
          '1.2.0': { time: recent },
        },
        'x-core': {
          '1.0.0': { time: old },
          '1.1.0': { time: recent },
        },
        '@s/y': {
          '1.0.0': { time: old },
          '1.0.1': { time: recent },
          '1.0.2': { time: recent },
        },
        'w': {
          '1.0.0': {
            time: old,
            dependencies: { '@s/y': '1.0.2' },
          },
        },
        'z': {
          '1.0.0': { time: old },
          '2.0.0': { time: recent },
        },
      }
    }
    function project(dependencies, authorized = {}) {
      const directory = fixture()
      const registry = npmRegistry(packages())
      const repository = join(directory, 'repository')
      gitRepository(repository, {
        files: {
          'frontend/package.json': `${JSON.stringify({ name: 'app', private: true, dependencies }, null, 2)}\n`,
          'frontend/bunfig.toml': `[install]\nregistry = "${registry}"\n`,
        },
      })
      const app = join(repository, 'frontend')
      const paths = shims(directory, {
        authorized: { 'frontend/bun.lock': authorized },
      })
      // Isolate HOME, so that a global bunfig cannot set its own release age.
      const env = toolEnvironment(directory, paths, {
        HOME: join(directory, 'home'),
        BUN_INSTALL_CACHE_DIR: join(directory, 'bun-cache'),
      })
      const install = () => run(join(paths.bin, 'bun'), ['install', '--ignore-scripts'], { cwd: app, env })
      const declare = next => writeFileSync(join(app, 'package.json'), `${JSON.stringify({ name: 'app', private: true, dependencies: next }, null, 2)}\n`)
      const locked = () => identities(lockedVersions('bun', readFileSync(join(app, 'bun.lock'), 'utf8')))
      return {
        app,
        repository,
        paths,
        env,
        install,
        declare,
        locked,
      }
    }
    it('resolves a routine range to the old-enough version', async () => {
      const { install, locked, paths } = project({ x: '^1.0.0' })
      const result = await install()
      expect(result.status, result.stderr).toBe(0)
      expect(locked()).toEqual(['x-core@1.0.0', 'x@1.0.0'])
      expect(invocations(paths)).toEqual([
        expect.objectContaining({
          tool: 'bun',
          cwd: 'frontend',
          args: ['install', '--ignore-scripts'],
          effectiveArgs: ['install', '--minimum-release-age=259200', '--ignore-scripts'],
          activeRoots: [],
          baseExcludes: 0,
          exitCode: 0,
        }),
      ])
    })
    it('resolves lockfile maintenance to the old-enough version and keeps the base identities', async () => {
      const { app, repository, install, locked, paths } = project({ x: '^1.0.0' })
      expect((await install()).status).toBe(0)
      commit(repository)
      rmSync(join(app, 'bun.lock'))
      const result = await install()
      expect(result.status, result.stderr).toBe(0)
      expect(locked()).toEqual(['x-core@1.0.0', 'x@1.0.0'])
      expect(invocations(paths).at(-1)).toMatchObject({
        effectiveArgs: [
          'install',
          '--minimum-release-age=259200',
          '--minimum-release-age-excludes=…(2 entries)',
          '--ignore-scripts',
        ],
        baseExcludes: 2,
      })
    })
    it('installs a declared security fix and its exact pin, and rejects any other young version', async () => {
      const { app, repository, install, declare, locked, paths } = project({ x: '^1.0.0' }, {
        'x@1.1.0': ['x-core@1.1.0'],
      })
      expect((await install()).status).toBe(0)
      commit(repository)
      declare({ x: '1.1.0' })
      const fixed = await install()
      expect(fixed.status, fixed.stderr).toBe(0)
      expect(locked()).toEqual(['x-core@1.1.0', 'x@1.1.0'])
      expect(invocations(paths).at(-1)).toMatchObject({
        activeRoots: ['x@1.1.0'],
        exitCode: 0,
      })
      const fixedLock = readFileSync(join(app, 'bun.lock'), 'utf8')
      declare({ x: '1.2.0' })
      const rejected = await install()
      expect(rejected.status).not.toBe(0)
      expect(rejected.stderr).toContain('minimum-release-age')
      expect(readFileSync(join(app, 'bun.lock'), 'utf8')).toBe(fixedLock)
      expect(invocations(paths).at(-1)).toMatchObject({ activeRoots: [], exitCode: rejected.status })
    })
    it('admits a scoped package through its exact exclude, and no other young version of it', async () => {
      const { app, declare, install, locked, paths } = project({ '@s/y': '1.0.1' }, { '@s/y@1.0.1': [] })
      const result = await install()
      expect(result.status, result.stderr).toBe(0)
      expect(locked()).toEqual(['@s/y@1.0.1'])
      expect(invocations(paths).at(-1)).toMatchObject({
        activeRoots: ['@s/y@1.0.1'],
        exitCode: 0,
      })
      // w is old enough, but it pins the young @s/y@1.0.2, which the exclude does not admit.
      const before = readFileSync(join(app, 'bun.lock'), 'utf8')
      declare({ '@s/y': '1.0.1', 'w': '1.0.0' })
      const rejected = await install()
      expect(rejected.status).not.toBe(0)
      expect(rejected.stderr).toContain('minimum-release-age')
      expect(readFileSync(join(app, 'bun.lock'), 'utf8')).toBe(before)
    })
    it('keeps a young version that the base already locks and rejects a newly introduced one', async () => {
      const { app, repository, env, declare, install, locked, paths } = project({ x: '1.1.0' })
      // Main locked the young fix while its alert was open. Real Bun writes that base.
      const based = await run(Bun.which('bun'), ['install', '--ignore-scripts'], { cwd: app, env })
      expect(based.status, based.stderr).toBe(0)
      commit(repository)
      rmSync(join(app, 'bun.lock'))
      declare({ x: '1.1.0', z: '>=1.0.0' })
      const result = await install()
      expect(result.status, result.stderr).toBe(0)
      expect(locked()).toEqual(['x-core@1.1.0', 'x@1.1.0', 'z@1.0.0'])
      expect(invocations(paths).at(-1)).toMatchObject({ activeRoots: [], baseExcludes: 2 })
    })
  })

  describe('cargo', () => {
    const crate = (time, deps) => ({
      time,
      ...(deps
        ? { deps }
        : {}),
    })
    // A crate at desktop/rust/ of a repository, which resolves through a fake crates.io.
    function project(dependencies, base, { authorized = {} } = {}) {
      const directory = fixture()
      const crates = structuredClone(base)
      const registry = crateRegistry(crates)
      const cargoHome = join(directory, 'cargo-home')
      writeFiles(cargoHome, { 'config.toml': `[source.crates-io]\nreplace-with = "local"\n[source.local]\nregistry = "${registry.index}"\n` })
      const manifest = deps => `[package]\nname = "app"\nversion = "0.1.0"\nedition = "2021"\n\n[dependencies]\n${Object.entries(deps).map(([name, req]) => `${name} = "${req}"`).join('\n')}\n`
      const repository = join(directory, 'repository')
      const app = join(repository, 'desktop/rust')
      writeFiles(app, {
        'Cargo.toml': manifest(dependencies),
        'src/lib.rs': '',
      })
      const paths = shims(directory, {
        authorized: { 'desktop/rust/Cargo.lock': authorized },
        cratesIndex: registry.shimIndex,
      })
      const env = toolEnvironment(directory, paths, { CARGO_HOME: cargoHome })
      return {
        app,
        paths,
        // Main locks the base with the real cargo, through no aged index.
        async lockBase() {
          const result = await run(Bun.which('cargo'), ['update'], { cwd: app, env })
          expect(result.status, result.stderr).toBe(0)
          gitRepository(repository)
        },
        publish(name, versions) {
          crates[name] = {
            ...crates[name],
            ...versions,
          }
        },
        declare: deps => writeFileSync(join(app, 'Cargo.toml'), manifest(deps)),
        cargo: args => run(join(paths.bin, 'cargo'), args, { cwd: app, env }),
        lockText: () => readFileSync(join(app, 'Cargo.lock'), 'utf8'),
        locked: () => identities(lockedVersions('cargo', readFileSync(join(app, 'Cargo.lock'), 'utf8'))),
      }
    }
    it('resolves a transitive crate to its newest old-enough version in lockfile maintenance while another line of the crate stays', async () => {
      const fixture = project({ parent: '1', foo: '0.9' }, {
        parent: {
          '1.0.0': crate(old, { foo: '^1.4.0' }),
        },
        foo: {
          '0.9.0': crate(old),
          '1.4.0': crate(old),
        },
      })
      await fixture.lockBase()
      fixture.publish('foo', {
        '1.4.1': crate(old),
        '1.4.2': crate(young()),
      })
      const result = await fixture.cargo([
        'update',
        '--manifest-path',
        join(fixture.app, 'Cargo.toml'),
      ])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.locked()).toEqual(['foo@0.9.0', 'foo@1.4.1', 'parent@1.0.0'])
      expect(invocations(fixture.paths)).toEqual([
        expect.objectContaining({
          tool: 'cargo',
          cwd: 'desktop/rust',
          activeRoots: [],
          exitCode: 0,
        }),
      ])
      // The aged index replaces the source only to resolve.
      expect(fixture.lockText()).not.toContain('127.0.0.1')
      expect(fixture.lockText()).toContain('source = "registry+https://github.com/rust-lang/crates.io-index"')
    })
    it('keeps a young version that the base already locks, as the aged index still serves it', async () => {
      const fixture = project({ foo: '1' }, {
        foo: {
          '1.4.1': crate(old),
          '1.4.2': crate(young()),
        },
      })
      // Main locked the young fix while its alert was open. The real cargo writes that base.
      await fixture.lockBase()
      expect(fixture.locked()).toEqual(['foo@1.4.2'])
      const before = fixture.lockText()
      const result = await fixture.cargo(['update'])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.lockText()).toBe(before)
    })
    it('fails when a new exact pin forces a downgrade, even when every release is old enough', async () => {
      const fixture = project({ parent: '=1.0.0' }, {
        parent: {
          '1.0.0': crate(old, { foo: '^1.4.0' }),
        },
        foo: {
          '1.4.1': crate(old),
          '1.4.2': crate(old),
        },
      })
      await fixture.lockBase()
      fixture.publish('parent', {
        '1.1.0': crate(old, { foo: '=1.4.1' }),
      })
      fixture.declare({ parent: '=1.1.0' })
      const before = fixture.lockText()
      const result = await fixture.cargo(['update', '--workspace'])
      expect(result.status).toBe(1)
      expect(result.stderr).toContain('cargo would downgrade foo from 1.4.2 to 1.4.1')
      expect(fixture.lockText()).toBe(before)
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        exitCode: 1,
        error: expect.stringContaining('cargo would downgrade foo from 1.4.2 to 1.4.1'),
      })
    })
    it('keeps an authorized precise fix and its exact pin, and resolves another young version of the pinned crate to an old-enough one', async () => {
      const fixture = project({ fix: '2' }, {
        fix: {
          '2.0.0': crate(old),
        },
        pin: {
          '0.4.0': crate(old),
        },
      }, {
        authorized: {
          'fix@2.0.1': ['pin@1.0.5'],
        },
      })
      await fixture.lockBase()
      const recent = young()
      fixture.publish('fix', {
        '2.0.1': crate(recent, { pin: '=1.0.5' }),
      })
      fixture.publish('pin', {
        '0.4.1': crate(recent),
        '1.0.5': crate(recent),
      })
      fixture.declare({ fix: '2', pin: '0.4' })
      const result = await fixture.cargo([
        'update',
        '--manifest-path',
        join(fixture.app, 'Cargo.toml'),
        '--package',
        'fix@2.0.0',
        '--precise',
        '2.0.1',
      ])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.locked()).toEqual(['fix@2.0.1', 'pin@0.4.0', 'pin@1.0.5'])
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        activeRoots: ['fix@2.0.1'],
        exitCode: 0,
      })
    })
    it('keeps an authorized lock-only fix through the workspace update that follows its precise update', async () => {
      // Renovate's lock-only Cargo update runs these two commands in order.
      const fixture = project({ fix: '2' }, {
        fix: {
          '2.0.0': crate(old),
        },
      }, {
        authorized: {
          'fix@2.0.1': ['pin@1.0.5'],
        },
      })
      await fixture.lockBase()
      const recent = young()
      fixture.publish('fix', {
        '2.0.1': crate(recent, { pin: '=1.0.5' }),
      })
      fixture.publish('pin', {
        '1.0.5': crate(recent),
      })
      const manifest = [
        '--manifest-path',
        join(fixture.app, 'Cargo.toml'),
      ]
      const precise = await fixture.cargo([
        'update',
        ...manifest,
        '--package',
        'fix@2.0.0',
        '--precise',
        '2.0.1',
      ])
      expect(precise.status, precise.stderr).toBe(0)
      const workspace = await fixture.cargo(['update', ...manifest, '--workspace'])
      expect(workspace.status, workspace.stderr).toBe(0)
      expect(fixture.locked()).toEqual(['fix@2.0.1', 'pin@1.0.5'])
      expect(invocations(fixture.paths).map(record => [record.activeRoots, record.exitCode])).toEqual([
        [
          ['fix@2.0.1'],
          0,
        ],
        [
          ['fix@2.0.1'],
          0,
        ],
      ])
    })
    it('rejects a young precise target that no open alert authorizes, in cargo itself', async () => {
      const fixture = project({ fix: '2' }, {
        fix: {
          '2.0.0': crate(old),
        },
      })
      await fixture.lockBase()
      fixture.publish('fix', {
        '2.0.1': crate(young()),
      })
      const before = fixture.lockText()
      const result = await fixture.cargo([
        'update',
        '--package',
        'fix@2.0.0',
        '--precise',
        '2.0.1',
      ])
      expect(result.status).not.toBe(0)
      expect(result.stderr).toContain('fix')
      expect(fixture.lockText()).toBe(before)
    })
    it('fails in cargo when a crate has no old-enough version, and leaves the lockfile alone', async () => {
      const fixture = project({}, {})
      await fixture.lockBase()
      const recent = young()
      fixture.publish('foo', {
        '1.4.1': crate(recent),
        '1.4.2': crate(recent),
      })
      fixture.declare({ foo: '1' })
      const before = fixture.lockText()
      const result = await fixture.cargo(['update', '--workspace'])
      expect(result.status).not.toBe(0)
      expect(result.stderr).toContain('foo')
      expect(fixture.lockText()).toBe(before)
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        tool: 'cargo',
        exitCode: result.status,
      })
    })
    it('resolves a chain of young crates to old-enough versions in one update', async () => {
      const fixture = project({}, {})
      await fixture.lockBase()
      const recent = young()
      fixture.publish('foo', {
        '1.4.1': crate(old, { bar: '2' }),
        '1.4.2': crate(recent),
      })
      fixture.publish('bar', {
        '2.0.0': crate(old),
        '2.0.1': crate(recent),
      })
      fixture.declare({ foo: '1' })
      const result = await fixture.cargo(['update', '--workspace'])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.locked()).toEqual(['bar@2.0.0', 'foo@1.4.1'])
    })
    it('keeps a young crate family that pins itself exactly at its versions and updates the other crates', async () => {
      // As the wasm-bindgen crates do, two young crates pin a third young crate exactly.
      // The aged index serves none of them, so cargo keeps the old family together.
      const fixture = project({ futures: '0.4', sys: '0.3', other: '1' }, {
        futures: {
          '0.4.79': crate(old, { js: '=0.3.106' }),
        },
        sys: {
          '0.3.106': crate(old, { js: '=0.3.106' }),
        },
        js: {
          '0.3.106': crate(old),
        },
        other: {
          '1.0.0': crate(old),
        },
      })
      await fixture.lockBase()
      const recent = young()
      fixture.publish('futures', {
        '0.4.80': crate(recent, { js: '=0.3.107' }),
      })
      fixture.publish('sys', {
        '0.3.107': crate(recent, { js: '=0.3.107' }),
      })
      fixture.publish('js', {
        '0.3.107': crate(recent),
      })
      fixture.publish('other', {
        '1.1.0': crate(old),
      })
      const result = await fixture.cargo([
        'update',
        '--manifest-path',
        join(fixture.app, 'Cargo.toml'),
      ])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.locked()).toEqual([
        'futures@0.4.79',
        'js@0.3.106',
        'other@1.1.0',
        'sys@0.3.106',
      ])
      const record = invocations(fixture.paths).at(-1)
      expect(record).toMatchObject({ tool: 'cargo', exitCode: 0 })
      expect(record).not.toHaveProperty('excluded')
    })
    it('keeps a crate that pins a young version exactly at its version in a full update', async () => {
      const fixture = project({ parent: '1', other: '1' }, {
        parent: {
          '1.0.0': crate(old, { foo: '^1.4.0' }),
        },
        foo: {
          '1.4.1': crate(old),
        },
        other: {
          '1.0.0': crate(old),
        },
      })
      await fixture.lockBase()
      fixture.publish('parent', {
        '1.1.0': crate(old, { foo: '=1.4.2' }),
      })
      fixture.publish('foo', {
        '1.4.2': crate(young()),
      })
      fixture.publish('other', {
        '1.1.0': crate(old),
      })
      const result = await fixture.cargo(['update'])
      expect(result.status, result.stderr).toBe(0)
      expect(fixture.locked()).toEqual(['foo@1.4.1', 'other@1.1.0', 'parent@1.0.0'])
      expect(invocations(fixture.paths).at(-1)).toMatchObject({ exitCode: 0 })
    })
    it('fails in cargo when a dependency pins a young version exactly, and leaves the lockfile alone', async () => {
      const fixture = project({}, {})
      await fixture.lockBase()
      fixture.publish('parent', {
        '1.0.0': crate(old, { foo: '=1.4.2' }),
      })
      fixture.publish('foo', {
        '1.4.1': crate(old),
        '1.4.2': crate(young()),
      })
      fixture.declare({ parent: '1' })
      const before = fixture.lockText()
      const result = await fixture.cargo(['update', '--workspace'])
      expect(result.status).not.toBe(0)
      expect(result.stderr).toContain('foo')
      expect(fixture.lockText()).toBe(before)
    })
  })
})
