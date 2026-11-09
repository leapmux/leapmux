// Real-tool snapshot tests. They run tiny Go and Rust builds against local
// fixture dependencies, drive a full snapshot round trip, and prove reuse
// through the tools' own records: a build that runs offline, a `-x` build
// log without a dependency compile command, and Cargo's fresh compiler
// artifacts with preserved source timestamps. No external registry download happens.
import { spawnSync } from 'node:child_process'
import {
  cpSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { snapshotPaths } from './cache-policy.mjs'
import {
  activateCacheSnapshot,
  cleanupSnapshotStaging,
  prepareCacheSnapshot,
  publishDependencySnapshot,
} from './cache-snapshot.mjs'
import { createStoredZip, gitRepository, scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('cache-snapshot-native')
const hash = 'a'.repeat(64)

function runnerEnv(dir, extra = {}) {
  return {
    RUNNER_OS: process.platform === 'win32'
      ? 'Windows'
      : process.platform === 'darwin'
        ? 'macOS'
        : 'Linux',
    RUNNER_ARCH: process.arch === 'arm64'
      ? 'ARM64'
      : 'X64',
    CACHE_IMAGE: 'ubuntu24',
    GOLANG_VERSION: '1.27.1',
    RUST_VERSION: '1.99.0',
    RUNNER_TEMP: join(dir, 'runner-temp'),
    GITHUB_REF: 'refs/heads/main',
    GITHUB_EVENT_NAME: 'push',
    ...extra,
  }
}

function run(executable, args, options) {
  return spawnSync(executable, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options })
}

function expectOk(result, what) {
  expect(result.error, `${what}: ${result.error?.message ?? ''}`).toBeUndefined()
  expect(result.status, `${what}: ${(result.stderr ?? result.stdout ?? '').slice(0, 2000)}`).toBe(0)
  return result
}

function expectFreshCargoLibrary(result, crate) {
  const artifacts = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line)).filter(message => message.reason === 'compiler-artifact'
    && message.target.name === crate.replaceAll('-', '_')
    && message.target.kind.includes('lib'))
  expect(artifacts).toHaveLength(1)
  expect(artifacts[0].fresh).toBe(true)
}

// --- Go fixtures ------------------------------------------------------------

function writeGoFixture(dir, depVersion, { appRequires } = {}) {
  const proxy = join(dir, 'proxy', 'example.com', 'dep', '@v')
  mkdirSync(proxy, { recursive: true })
  const mod = [
    'module example.com/dep',
    '',
    'go 1.24',
    '',
  ].join('\n')
  const source = [
    'package dep',
    '',
    `const Version = "${depVersion}"`,
    '',
    'func Value() int { return 6 * 7 }',
    '',
  ].join('\n')
  const zip = createStoredZip({
    [`example.com/dep@${depVersion}/go.mod`]: mod,
    [`example.com/dep@${depVersion}/dep.go`]: source,
  })
  writeFileSync(join(proxy, `${depVersion}.zip`), zip)
  writeFileSync(join(proxy, `${depVersion}.mod`), mod)
  writeFileSync(join(proxy, `${depVersion}.info`), `{"Version":"${depVersion}","Time":"2026-01-01T00:00:00Z"}\n`)
  writeFileSync(join(proxy, 'list'), `${depVersion}\n`)
  const workspace = join(dir, 'app')
  mkdirSync(join(workspace), { recursive: true })
  const requires = appRequires ?? depVersion
  writeFileSync(join(workspace, 'go.mod'), [
    'module example.com/app',
    '',
    'go 1.24',
    '',
    `require example.com/dep ${requires}`,
    '',
  ].join('\n'))
  writeFileSync(join(workspace, 'main.go'), [
    'package main',
    '',
    'import (',
    '\t"fmt"',
    '',
    '\t"example.com/dep"',
    ')',
    '',
    'func main() { fmt.Println(dep.Version, dep.Value()) }',
    '',
  ].join('\n'))
  return { workspace, proxy }
}

// --- The Rust fixture registry ----------------------------------------------
// A Cargo local registry replaces crates.io with an index and packed .crate
// files. Cargo extracts them into its registry source directory, so the tests
// exercise source timestamps without a remote registry or a loopback server.

function writeSysCrateSources(stage) {
  writeFileSync(join(stage, 'Cargo.toml'), [
    '[package]',
    'name = "git-sys"',
    'version = "0.1.0"',
    'edition = "2021"',
    'links = "nativefixture"',
    '',
  ].join('\n'))
  writeFileSync(join(stage, 'src', 'lib.rs'), 'pub fn value() -> u32 { 42 }\n')
  // A timestamp-sensitive build script: it fails when it runs again, which
  // only happens if the reused source data did not survive the round trip.
  writeFileSync(join(stage, 'build.rs'), [
    'use std::env;',
    'use std::fs;',
    'use std::path::Path;',
    '',
    'fn main() {',
    '    let out = env::var("OUT_DIR").unwrap();',
    '    let marker = Path::new(&out).join("built.rs");',
    '    if marker.exists() {',
    '        panic!("the git-sys build script ran again; its source data was not reused");',
    '    }',
    '    fs::write(&marker, "pub const BUILT: bool = true;\\n").unwrap();',
    '}',
    '',
  ].join('\n'))
}

// Build a cargo local-registry layout that serves one crate: the packed
// .crate, its index record, and the registry config. COPYFILE_DISABLE
// keeps macOS tar from adding AppleDouble entries that cargo rejects.
function buildLocalRegistry(repoDir, stage, name, version) {
  const { createHash } = require('node:crypto')
  const registryDir = join(repoDir, 'fixture-registry')
  const indexDir = join(registryDir, 'index', name.slice(0, 2), name.slice(2, 4))
  mkdirSync(indexDir, { recursive: true })
  const crateFile = join(registryDir, `${name}-${version}.crate`)
  expectOk(run('tar', ['-czf', crateFile, `${name}-${version}`], {
    cwd: join(stage, '..'),
    env: { ...process.env, COPYFILE_DISABLE: '1' },
  }), 'tar the fixture crate')
  const packed = readFileSync(crateFile)
  const checksum = createHash('sha256').update(packed).digest('hex')
  const indexLine = JSON.stringify({
    name,
    vers: version,
    deps: [],
    cksum: checksum,
    features: {},
    yanked: false,
  })
  writeFileSync(join(indexDir, name), `${indexLine}\n`)
  writeFileSync(join(registryDir, 'config.json'), `${JSON.stringify({ dl: `file://${registryDir}/{crate}/{version}`, sparse: true })}\n`)
  return registryDir
}

/** Populate the isolated payload of a dependency kind, as the workflow does. */
function isolatedGoDownload(workspace, payloadRoot, env) {
  expectOk(run('go', ['mod', 'download'], {
    cwd: workspace,
    env: {
      ...env,
      GOMODCACHE: join(payloadRoot, 'gomod'),
    },
  }), 'the isolated Go download')
}

function isolatedCargoFetch(workspace, payloadRoot, env) {
  expectOk(run('cargo', ['fetch', '--locked'], {
    cwd: workspace,
    env: {
      ...env,
      CARGO_HOME: join(payloadRoot, 'cargo-home'),
    },
  }), 'the isolated Cargo fetch')
}

// --- The tests --------------------------------------------------------------

describe('cache snapshot native', () => {
  it('reuses a real Go dependency build across a snapshot round trip, offline', async () => {
    const dir = fixture()
    const { workspace } = writeGoFixture(dir, 'v1.0.0')
    const activeMod = join(dir, 'gomod-active')
    const writerBuild = join(dir, 'writer-build')
    const environment = runnerEnv(dir)
    const writerEnv = {
      ...process.env,
      // GOWORK=off keeps the fixture module outside the repository's
      // workspace, whose tool dependencies the fixture proxy cannot serve.
      GOWORK: 'off',
      GOPROXY: pathToFileURL(join(dir, 'proxy')).href,
      GOSUMDB: 'off',
      GOFLAGS: '-mod=mod',
      GOPATH: join(dir, 'gopath'),
      GOMODCACHE: activeMod,
      GOCACHE: writerBuild,
    }
    // An inherited obsolete module sits in the active downloads; the
    // isolated population cannot retain it.
    mkdirSync(join(activeMod, 'cache', 'download', 'example.com', 'dep', '@v'), { recursive: true })
    writeFileSync(join(activeMod, 'cache', 'download', 'example.com', 'dep', '@v', 'v0.9.0.info'), '{"Version":"v0.9.0"}\n')

    // The dependency population writes into the empty payload and publishes
    // to the active downloads, exactly as the workflow action does.
    const depKey = `go-modules-2-${environment.RUNNER_OS}-${hash}`
    const modPayload = snapshotPaths('go-modules', dir).payloadRoot
    isolatedGoDownload(workspace, modPayload, writerEnv)
    publishDependencySnapshot({
      kind: 'go-modules',
      key: depKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        GOMODCACHE: activeMod,
      },
    })
    expect(existsSync(join(activeMod, 'cache', 'download', 'example.com', 'dep', '@v', 'v1.0.0.zip'))).toBe(true)
    expect(existsSync(join(activeMod, 'cache', 'download', 'example.com', 'dep', '@v', 'v0.9.0.info'))).toBe(true)
    await prepareCacheSnapshot({
      kind: 'go-modules',
      key: depKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        GOMODCACHE: activeMod,
      },
    })
    expect(existsSync(join(modPayload, 'gomod', 'cache', 'download', 'example.com', 'dep', '@v', 'v1.0.0.zip'))).toBe(true)
    expect(existsSync(join(modPayload, 'gomod', 'cache', 'download', 'example.com', 'dep', '@v', 'v0.9.0.info'))).toBe(false)

    const trace = expectOk(run('go', ['build', '-x', './...'], { cwd: workspace, env: writerEnv }), 'the first build')
    expect(trace.stderr).toContain('-p example.com/dep')
    const buildKey = `go-build-2-${environment.RUNNER_OS}-${environment.RUNNER_ARCH}-${environment.CACHE_IMAGE}-go${environment.GOLANG_VERSION}-${hash}-2027-W01`
    await prepareCacheSnapshot({
      kind: 'native-go',
      key: buildKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        GOCACHE: writerBuild,
        GOLANGCI_LINT_CACHE: '',
      },
    })

    // The reader restores at the canonical tool paths, as CI does: Go's
    // action IDs and fingerprints cover the module cache path, so a job's
    // caches are only reusable at the paths the tools default to.
    const readerEnv = {
      ...process.env,
      ...environment,
      GOWORK: 'off',
      GOMODCACHE: activeMod,
      GOCACHE: writerBuild,
    }
    await activateCacheSnapshot({
      kind: 'go-modules',
      key: depKey,
      writer: false,
      matchedKey: depKey,
      workspace: dir,
      env: readerEnv,
    })
    cleanupSnapshotStaging({ kind: 'go-modules', workspace: dir })
    expect(existsSync(join(activeMod, 'cache', 'download', 'example.com', 'dep', '@v', 'v1.0.0.zip'))).toBe(true)
    await activateCacheSnapshot({
      kind: 'native-go',
      key: buildKey,
      writer: false,
      matchedKey: buildKey,
      workspace: dir,
      env: readerEnv,
    })
    cleanupSnapshotStaging({ kind: 'native-go', workspace: dir })
    expect(existsSync(writerBuild)).toBe(true)

    // Offline, no download can happen, so the successful build proves the
    // restored dependencies; the missing `-p example.com/dep` compile
    // command proves the restored build outputs.
    const offline = { ...readerEnv, GOPROXY: 'off' }
    const rebuilt = expectOk(run('go', ['build', '-x', './...'], { cwd: workspace, env: offline }), 'the offline reader build')
    expect(rebuilt.stderr).not.toContain('-p example.com/dep')
    const binary = process.platform === 'win32'
      ? join(workspace, 'app.exe')
      : join(workspace, 'app')
    expect(existsSync(binary)).toBe(true)
  })

  it('excludes the previous dependency version from a new writer generation', async () => {
    const dir = fixture()
    writeGoFixture(dir, 'v1.0.0')
    const environment = runnerEnv(dir)
    const activeMod = join(dir, 'gomod-active')
    const writerEnv = {
      ...process.env,
      GOWORK: 'off',
      GOPROXY: pathToFileURL(join(dir, 'proxy')).href,
      GOSUMDB: 'off',
      GOPATH: join(dir, 'gopath'),
      GOMODCACHE: activeMod,
    }
    const firstPayload = snapshotPaths('go-modules', dir).payloadRoot
    isolatedGoDownload(join(dir, 'app'), firstPayload, writerEnv)
    const firstKey = `go-modules-2-${environment.RUNNER_OS}-${hash}`
    await prepareCacheSnapshot({
      kind: 'go-modules',
      key: firstKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        GOMODCACHE: activeMod,
      },
    })
    expect(existsSync(join(firstPayload, 'gomod', 'cache', 'download', 'example.com', 'dep', '@v', 'v1.0.0.zip'))).toBe(true)
    cleanupSnapshotStaging({ kind: 'go-modules', workspace: dir })

    // The dependency moves to v1.1.0: the next generation's writer
    // downloads into an empty payload, which cannot retain v1.0.0.
    rmSync(join(dir, 'proxy'), { recursive: true, force: true })
    writeGoFixture(dir, 'v1.1.0')
    writeFileSync(join(dir, 'app', 'go.mod'), [
      'module example.com/app',
      '',
      'go 1.24',
      '',
      'require example.com/dep v1.1.0',
      '',
    ].join('\n'))
    rmSync(join(dir, 'app', 'go.sum'), { force: true })
    rmSync(activeMod, { recursive: true, force: true })
    mkdirSync(activeMod, { recursive: true })
    const secondPayload = snapshotPaths('go-modules', dir).payloadRoot
    isolatedGoDownload(join(dir, 'app'), secondPayload, writerEnv)
    const secondKey = `go-modules-2-${environment.RUNNER_OS}-${'b'.repeat(64)}`
    await prepareCacheSnapshot({
      kind: 'go-modules',
      key: secondKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        GOMODCACHE: activeMod,
      },
    })
    expect(existsSync(join(secondPayload, 'gomod', 'cache', 'download', 'example.com', 'dep', '@v', 'v1.1.0.zip'))).toBe(true)
    expect(existsSync(join(secondPayload, 'gomod', 'cache', 'download', 'example.com', 'dep', '@v', 'v1.0.0.zip'))).toBe(false)
    cleanupSnapshotStaging({ kind: 'go-modules', workspace: dir })
  })

  it.each(['always', 'never'])('reuses the -sys sources without rerunning the build script with color set to %s', async (color) => {
    const dir = fixture()
    const name = 'git-sys'
    const version = '0.1.0'
    const stage = join(dir, 'stage', `${name}-${version}`)
    mkdirSync(join(stage, 'src'), { recursive: true })
    writeSysCrateSources(stage)
    // A literal backslash on POSIX exercises the TOML escaping that Windows paths need.
    const registryParent = join(dir, process.platform === 'win32'
      ? 'registry storage'
      : 'registry\\storage')
    mkdirSync(registryParent)
    // The fixture registry directory lives in the scratch tree.
    const registryDir = buildLocalRegistry(registryParent, stage, name, version)
    const workspace = join(dir, 'desktop', 'rust')
    mkdirSync(join(workspace, 'src'), { recursive: true })
    mkdirSync(join(workspace, '.cargo'), { recursive: true })
    writeFileSync(join(workspace, '.cargo', 'config.toml'), [
      '[source.crates-io]',
      'replace-with = "fixture-registry"',
      '',
      '[source.fixture-registry]',
      `local-registry = ${JSON.stringify(registryDir)}`,
      '',
    ].join('\n'))
    writeFileSync(join(workspace, 'Cargo.toml'), [
      '[package]',
      'name = "consumer"',
      'version = "0.1.0"',
      'edition = "2021"',
      '',
      '[dependencies]',
      `${name} = "${version}"`,
      '',
    ].join('\n'))
    writeFileSync(join(workspace, 'src', 'lib.rs'), 'pub fn value() -> u32 { git_sys::value() }\n')

    const writerHome = join(dir, 'writer-cargo')
    const writerEnv = { ...process.env, CARGO_HOME: writerHome, CARGO_TERM_COLOR: color }
    expectOk(run('cargo', ['generate-lockfile'], { cwd: workspace, env: writerEnv }), 'generate the lockfile')
    expectOk(run('cargo', ['build', '--locked'], { cwd: workspace, env: writerEnv }), 'the writer build')
    const registryRoot = join(writerHome, 'registry', 'src')
    const registryId = readdirSync(registryRoot)[0]
    const extracted = join(registryRoot, registryId, `${name}-${version}`)
    expect(existsSync(extracted)).toBe(true)
    const srcStat = statSync(join(extracted, 'src', 'lib.rs'))

    const environment = runnerEnv(dir)
    const downloadKey = `cargo-downloads-2-${environment.RUNNER_OS}-${hash}`
    const downloadPayload = snapshotPaths('cargo-downloads', dir).payloadRoot
    isolatedCargoFetch(workspace, downloadPayload, writerEnv)
    await prepareCacheSnapshot({
      kind: 'cargo-downloads',
      key: downloadKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        CARGO_HOME: writerHome,
      },
    })
    // A local registry unpacks into the source directory instead of a
    // .crate cache, so the payload carries the extracted sources.
    expect(existsSync(join(downloadPayload, 'cargo-home', 'registry', 'src', registryId, `${name}-${version}`, 'src', 'lib.rs'))).toBe(true)

    const rustKey = `rust-build-2-${environment.RUNNER_OS}-${environment.RUNNER_ARCH}-${environment.CACHE_IMAGE}-rust${environment.RUST_VERSION}-${hash}`
    await prepareCacheSnapshot({
      kind: 'native-rust',
      key: rustKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        CARGO_HOME: writerHome,
      },
    })
    const rustPayload = snapshotPaths('native-rust', dir).payloadRoot
    expect(existsSync(join(rustPayload, 'registry-src', registryId, `${name}-${version}`, 'src', 'lib.rs'))).toBe(true)

    // The reader starts from a fresh Cargo home at the SAME path, which
    // the dependency fingerprints require, and restores both archives as
    // the transport delivers them.
    rmSync(writerHome, { recursive: true, force: true })
    const readerHome = writerHome
    mkdirSync(readerHome, { recursive: true })
    cpSync(join(downloadPayload, 'cargo-home'), readerHome, { recursive: true })
    await activateCacheSnapshot({
      kind: 'native-rust',
      key: rustKey,
      writer: false,
      matchedKey: rustKey,
      workspace: dir,
      env: {
        ...process.env,
        CARGO_HOME: readerHome,
        ...environment,
      },
    })
    cleanupSnapshotStaging({ kind: 'native-rust', workspace: dir })
    const readerExtracted = join(readerHome, 'registry', 'src', registryId, `${name}-${version}`)
    expect(existsSync(join(readerExtracted, 'src', 'lib.rs'))).toBe(true)
    // The extracted source timestamps survive the round trip.
    expect(statSync(join(readerExtracted, 'src', 'lib.rs')).mtimeMs).toBe(srcStat.mtimeMs)

    const readerBuild = expectOk(run('cargo', ['build', '--locked', '--message-format=json'], {
      cwd: workspace,
      env: { ...writerEnv, CARGO_HOME: readerHome },
    }), 'the reader build')
    // Cargo's compiler-artifact event proves reuse without parsing colored status text.
    expectFreshCargoLibrary(readerBuild, name)
  })

  it.each(['always', 'never'])('keeps Git dependency artifacts reusable with color set to %s', async (color) => {
    const dir = fixture()
    const repo = join(dir, 'git-repo')
    gitRepository(repo, {
      files: {
        'Cargo.toml': [
          '[package]',
          'name = "gitdep"',
          'version = "0.2.0"',
          'edition = "2021"',
        ].join('\n'),
        'src/lib.rs': 'pub fn value() -> u32 { 9 }\n',
      },
    })
    const head = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).stdout.trim()
    const workspace = join(dir, 'desktop', 'rust')
    mkdirSync(join(workspace, 'src'), { recursive: true })
    writeFileSync(join(workspace, 'Cargo.toml'), [
      '[package]',
      'name = "consumer"',
      'version = "0.1.0"',
      'edition = "2021"',
      '',
      '[dependencies]',
      `gitdep = { git = "${pathToFileURL(repo).href}", rev = "${head}" }`,
      '',
    ].join('\n'))
    writeFileSync(join(workspace, 'src', 'lib.rs'), 'pub fn value() -> u32 { gitdep::value() }\n')

    const writerHome = join(dir, 'writer-cargo')
    const writerEnv = { ...process.env, CARGO_HOME: writerHome, CARGO_TERM_COLOR: color }
    expectOk(run('cargo', ['generate-lockfile'], { cwd: workspace, env: writerEnv }), 'generate the lockfile')
    expectOk(run('cargo', ['build', '--locked'], { cwd: workspace, env: writerEnv }), 'the writer build')
    const checkoutDir = join(writerHome, 'git', 'checkouts')
    const repoDir = readdirSync(checkoutDir)[0]
    expect(existsSync(join(writerHome, 'git', 'db', repoDir))).toBe(true)

    const environment = runnerEnv(dir)
    const downloadKey = `cargo-downloads-2-${environment.RUNNER_OS}-${hash}`
    const downloadPayload = snapshotPaths('cargo-downloads', dir).payloadRoot
    isolatedCargoFetch(workspace, downloadPayload, writerEnv)
    await prepareCacheSnapshot({
      kind: 'cargo-downloads',
      key: downloadKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        CARGO_HOME: writerHome,
      },
    })

    const rustKey = `rust-build-2-${environment.RUNNER_OS}-${environment.RUNNER_ARCH}-${environment.CACHE_IMAGE}-rust${environment.RUST_VERSION}-${hash}`
    await prepareCacheSnapshot({
      kind: 'native-rust',
      key: rustKey,
      workspace: dir,
      env: {
        ...process.env,
        ...environment,
        CARGO_HOME: writerHome,
      },
    })
    expect(existsSync(join(snapshotPaths('native-rust', dir).payloadRoot, 'git-checkouts', repoDir))).toBe(true)

    // The reader starts from a fresh Cargo home at the SAME path, which
    // the dependency fingerprints require, and restores both archives as
    // the transport delivers them.
    rmSync(writerHome, { recursive: true, force: true })
    const readerHome = writerHome
    mkdirSync(readerHome, { recursive: true })
    cpSync(join(downloadPayload, 'cargo-home'), readerHome, { recursive: true })
    await activateCacheSnapshot({
      kind: 'native-rust',
      key: rustKey,
      writer: false,
      matchedKey: rustKey,
      workspace: dir,
      env: {
        ...process.env,
        CARGO_HOME: readerHome,
        ...environment,
      },
    })
    cleanupSnapshotStaging({ kind: 'native-rust', workspace: dir })

    // The git database and the checkout both survived, and the dependency
    // builds again without re-fetching: the origin repository is removed.
    expect(existsSync(join(readerHome, 'git', 'db', repoDir))).toBe(true)
    expect(existsSync(join(readerHome, 'git', 'checkouts', repoDir))).toBe(true)
    rmSync(repo, { recursive: true, force: true })
    const readerBuild = expectOk(run('cargo', ['build', '--locked', '--message-format=json'], {
      cwd: workspace,
      env: { ...writerEnv, CARGO_HOME: readerHome },
    }), 'the reader build')
    expectFreshCargoLibrary(readerBuild, 'gitdep')
  })
})
