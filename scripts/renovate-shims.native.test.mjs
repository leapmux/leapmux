import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { gzipSync } from 'node:zlib'
import { afterEach, describe, expect, it } from 'bun:test'
import { lockedVersions } from './renovate-lockfile-versions.mjs'
import { toolShimPaths, writeToolShims } from './renovate-shims.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
const servers = []
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true)
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

const old = '2026-01-01T00:00:00.000Z'
const young = () => new Date(Date.now() - 3600000).toISOString()

function fixture() {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(root, '.tmp/renovate-shims-native-'))
  fixtures.push(directory)
  return directory
}

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

function write(directory, files) {
  for (const [file, content] of Object.entries(files)) {
    mkdirSync(resolve(directory, file, '..'), { recursive: true })
    writeFileSync(resolve(directory, file), content)
  }
}

// Pin the repository configuration, as testutil.NewGitRepo does: a global fsmonitor
// daemon would outlive the command and race the fixture cleanup.
function repository(directory, files) {
  write(directory, files)
  const env = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' }
  for (const args of [
    ['init', '-q', '-b', 'main'],
    ['config', 'core.fsmonitor', 'false'],
    ['config', 'gc.auto', '0'],
    ['config', 'maintenance.auto', 'false'],
    ['config', 'user.email', 'fixture@example.com'],
    ['config', 'user.name', 'Fixture'],
    ['config', 'commit.gpgsign', 'false'],
    ['add', '-A'],
    ['commit', '-q', '-m', 'base'],
  ]) {
    const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8', env })
    expect(result.status, `git ${args.join(' ')}: ${result.stderr}`).toBe(0)
  }
  return directory
}

function commit(directory) {
  const env = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' }
  for (const args of [
    ['add', '-A'],
    ['commit', '-q', '-m', 'base lockfile'],
  ]) {
    const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8', env })
    expect(result.status, result.stderr).toBe(0)
  }
}

function shims(directory, { npm = {}, crates = {}, cratesApi, maxRepairRounds } = {}) {
  writeToolShims({
    root: directory,
    policy: { minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: null },
    prepared: {
      authorized: { npm, crates, go: {} },
    },
    env: { PATH: process.env.PATH },
    ...(cratesApi
      ? { cratesApi }
      : {}),
    ...(maxRepairRounds
      ? { maxRepairRounds }
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

// A sparse index with a crates API. The test changes `crates` between the base and the
// head, as a registry gains releases after main locks its versions.
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
          return new Response(Object.entries(versions).map(([vers, { deps = {} }]) => JSON.stringify({
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
          })).join('\n'))
        }
        if (path === `/api/v1/crates/${name}`) {
          return Response.json({
            versions: Object.entries(versions).map(([num, { time }]) => ({ num, created_at: time, yanked: false })),
          })
        }
      }
      return new Response('not found', { status: 404 })
    },
  })
  servers.push(server)
  return { index: `sparse+http://127.0.0.1:${server.port}/index/`, api: `http://127.0.0.1:${server.port}/api/v1` }
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
    write(directory, {
      'frontend/bun.lock': readFileSync(resolve(root, 'frontend/bun.lock')),
      'desktop/rust/Cargo.lock': readFileSync(resolve(root, 'desktop/rust/Cargo.lock')),
    })
    const source = `const {writeToolShims}=await import(${JSON.stringify(new URL('./renovate-shims.mjs', import.meta.url).href)}); const {readFileSync}=await import('node:fs'); writeToolShims({root:process.argv[1],policy:JSON.parse(readFileSync(process.argv[2],'utf8')),prepared:{authorized:{npm:{},crates:{'glib@0.20.0':[]},go:{}}}}); process.stdout.write(process.versions.bun??'node')`
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
        npm: {},
        crates: { 'glib@0.20.0': [] },
      },
      cratesApi: 'https://crates.io/api/v1',
      maxRepairRounds: 10,
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
      const app = repository(join(directory, 'app'), {
        'package.json': `${JSON.stringify({ name: 'app', private: true, dependencies }, null, 2)}\n`,
        'bunfig.toml': `[install]\nregistry = "${registry}"\n`,
      })
      const paths = shims(directory, { npm: authorized })
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
          cwd: '.',
          args: ['install', '--ignore-scripts'],
          effectiveArgs: ['install', '--ignore-scripts', '--minimum-release-age=259200'],
          activeRoots: [],
          baseExcludes: 0,
          exitCode: 0,
        }),
      ])
    })
    it('resolves lockfile maintenance to the old-enough version and keeps the base identities', async () => {
      const { app, install, locked, paths } = project({ x: '^1.0.0' })
      expect((await install()).status).toBe(0)
      commit(app)
      rmSync(join(app, 'bun.lock'))
      const result = await install()
      expect(result.status, result.stderr).toBe(0)
      expect(locked()).toEqual(['x-core@1.0.0', 'x@1.0.0'])
      expect(invocations(paths).at(-1)).toMatchObject({
        effectiveArgs: [
          'install',
          '--ignore-scripts',
          '--minimum-release-age=259200',
          '--minimum-release-age-excludes=…(2 entries)',
        ],
        baseExcludes: 2,
      })
    })
    it('installs a declared security fix and its exact pin, and rejects any other young version', async () => {
      const { app, install, declare, locked, paths } = project({ x: '^1.0.0' }, {
        'x@1.1.0': ['x-core@1.1.0'],
      })
      expect((await install()).status).toBe(0)
      commit(app)
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
      const { app, env, declare, install, locked, paths } = project({ x: '1.1.0' })
      // Main locked the young fix while its alert was open. Real Bun writes that base.
      const based = await run(Bun.which('bun'), ['install', '--ignore-scripts'], { cwd: app, env })
      expect(based.status, based.stderr).toBe(0)
      commit(app)
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
    function project(dependencies, base, { authorized = {}, maxRepairRounds } = {}) {
      const directory = fixture()
      const crates = structuredClone(base)
      const registry = crateRegistry(crates)
      const cargoHome = join(directory, 'cargo-home')
      write(cargoHome, { 'config.toml': `[source.crates-io]\nreplace-with = "local"\n[source.local]\nregistry = "${registry.index}"\n` })
      const manifest = deps => `[package]\nname = "app"\nversion = "0.1.0"\nedition = "2021"\n\n[dependencies]\n${Object.entries(deps).map(([name, req]) => `${name} = "${req}"`).join('\n')}\n`
      const app = join(directory, 'app')
      write(app, {
        'Cargo.toml': manifest(dependencies),
        'src/lib.rs': '',
      })
      const paths = shims(directory, { crates: authorized, cratesApi: registry.api, maxRepairRounds })
      const env = toolEnvironment(directory, paths, { CARGO_HOME: cargoHome })
      return {
        app,
        paths,
        // Main locks the base with the real cargo before the registry gains releases.
        async lockBase() {
          const result = await run(Bun.which('cargo'), ['update'], { cwd: app, env })
          expect(result.status, result.stderr).toBe(0)
          repository(app, {})
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
    it('repairs a young transitive crate in lockfile maintenance while another line of the crate stays', async () => {
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
          repairs: [
            { from: 'foo@1.4.2', to: 'foo@1.4.1' },
          ],
          exitCode: 0,
        }),
      ])
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
    it('keeps an authorized precise fix and its exact pin, and repairs another young version of the pinned crate', async () => {
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
        repairs: [
          { from: 'pin@0.4.1', to: 'pin@0.4.0' },
        ],
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
      expect(invocations(fixture.paths).map(record => [record.activeRoots, record.repairs])).toEqual([
        [
          ['fix@2.0.1'],
          [],
        ],
        [
          ['fix@2.0.1'],
          [],
        ],
      ])
    })
    it('fails when a young crate has no old-enough version', async () => {
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
      expect(result.status).toBe(1)
      expect(result.stderr).toContain('Cargo release-age repair failed: no eligible version')
      expect(result.stderr).toContain('Cargo.lock was restored to its state before this command.')
      expect(fixture.lockText()).toBe(before)
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        tool: 'cargo',
        exitCode: 1,
        error: expect.stringContaining('Cargo release-age repair failed: no eligible version'),
      })
    })
    it('fails when a chain of repairs exceeds the round limit', async () => {
      const fixture = project({}, {}, { maxRepairRounds: 1 })
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
      const before = fixture.lockText()
      const result = await fixture.cargo(['update', '--workspace'])
      expect(result.status).toBe(1)
      expect(result.stderr).toContain('Cargo release-age repair failed: round limit reached\n- bar@2.0.1')
      expect(result.stderr).toContain('Cargo.lock was restored to its state before this command.')
      expect(fixture.lockText()).toBe(before)
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        tool: 'cargo',
        exitCode: 1,
        error: expect.stringContaining('Cargo release-age repair failed: round limit reached'),
      })
    })
    it('keeps a young crate family that pins itself exactly at its versions and updates the other crates', async () => {
      // As the wasm-bindgen crates do, two young crates pin a third young crate exactly,
      // so no single --precise command can move any of them back.
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
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        tool: 'cargo',
        exitCode: 0,
        repairs: [],
        excluded: ['futures', 'js', 'sys'],
      })
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
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        exitCode: 0,
        excluded: ['foo', 'parent'],
      })
    })
    it('fails when cargo rejects the repair because a dependency pins the young version', async () => {
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
      expect(result.status).toBe(1)
      expect(result.stderr).toContain('Cargo release-age repair failed: cargo rejected --precise 1.4.1')
      expect(result.stderr).toContain('Cargo.lock was restored to its state before this command.')
      expect(fixture.lockText()).toBe(before)
      expect(invocations(fixture.paths).at(-1)).toMatchObject({
        tool: 'cargo',
        exitCode: 1,
        error: expect.stringContaining('Cargo release-age repair failed: cargo rejected --precise 1.4.1'),
      })
    })
  })
})
