// Validate the production Docker cache stages with a cold writer and two
// fresh readers. The archive round trip uses the snapshot implementation.
// Every reader imports the restored files and forces the builder stage to
// execute, so a cached image layer cannot hide a missing Go cache.
import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { closeSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, readdirSync, readFileSync, readlinkSync, readSync, renameSync, rmdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { basename, join, relative, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { activeRootsFor, cacheKey, snapshotPaths } from './cache-policy.mjs'
import { activateCacheSnapshot, cleanupSnapshotStaging, prepareCacheSnapshot } from './cache-snapshot.mjs'

const DOCKER_FILE = 'docker/Dockerfile'
const EXPORT_TARGET = 'go-cache-files'
const IMPORT_TARGET = 'go-cache-import'

function baseBuildArgs(env, variant) {
  const values = {
    BASE_IMAGE: variant === 'ubuntu'
      ? env.UBUNTU_IMAGE
      : env.ALPINE_IMAGE,
    BUF_VERSION: env.BUF_VERSION,
    TASK_VERSION: env.TASK_VERSION,
    S6_OVERLAY_VERSION: env.S6_OVERLAY_VERSION,
    BUN_VERSION: env.BUN_VERSION,
    NODE_VERSION: env.NODE_VERSION,
    GOLANG_VERSION: env.GOLANG_VERSION,
  }
  const missing = Object.entries(values).filter(([, value]) => !value).map(([name]) => name)
  if (missing.length > 0)
    throw new Error(`Run the Docker cache check through Task; missing tool versions: ${missing.join(', ')}.`)
  return Object.entries(values).flatMap(([name, value]) => ['--build-arg', `${name}=${value}`])
}

function realDocker(args) {
  const result = spawnSync('docker', args, { encoding: 'utf8', maxBuffer: 256 * 1024 * 1024, timeout: 600000 })
  return { status: result.status, stderr: result.error?.message ?? result.stderr ?? '', stdout: result.stdout ?? '' }
}

function realArchive(args) {
  const result = spawnSync('tar', args, {
    encoding: 'utf8',
    maxBuffer: 1024 * 1024,
    env: { ...process.env, COPYFILE_DISABLE: '1' },
  })
  return { status: result.status, stderr: result.error?.message ?? result.stderr ?? '', stdout: result.stdout ?? '' }
}

function buildCommand({ builder, platform, version, executionToken, dockerFile = DOCKER_FILE, baseArgs = [] }) {
  if (typeof executionToken !== 'string' || executionToken === '')
    throw new Error('A cache stage command requires a unique execution token.')
  return [
    'buildx',
    'build',
    '--progress',
    'plain',
    '--builder',
    builder,
    '--file',
    dockerFile,
    ...(platform
      ? ['--platform', platform]
      : []),
    ...baseArgs,
    '--build-arg',
    `VERSION=${version}`,
    '--build-arg',
    'GOFLAGS=-x',
  ]
}

export function exportCommand(options) {
  return [
    ...buildCommand(options),
    '--target',
    EXPORT_TARGET,
    '--build-arg',
    `GO_CACHE_EXPORT_TOKEN=${options.executionToken}`,
    '--output',
    `type=local,dest=${options.dest}`,
    '.',
  ]
}

export function importCommand(options) {
  return [
    ...buildCommand(options),
    '--target',
    IMPORT_TARGET,
    '--build-arg',
    `GO_CACHE_IMPORT_TOKEN=${options.executionToken}`,
    '--build-context',
    `go-cache-input=${options.source}`,
    '--output',
    'type=cacheonly',
    '.',
  ]
}

export function warmBuildCommand(options) {
  return [
    ...buildCommand(options),
    '--target',
    'builder',
    '--build-arg',
    `GO_CACHE_BUILD_TOKEN=${options.executionToken}`,
    '--output',
    'type=cacheonly',
    '.',
  ]
}

function fingerprint(directory) {
  const hash = createHash('sha256')
  let files = 0
  const buffer = Buffer.alloc(64 * 1024)
  function visit(path) {
    const stat = lstatSync(path)
    const name = relative(directory, path).replaceAll('\\', '/')
    if (stat.isDirectory()) {
      for (const entry of readdirSync(path).sort()) visit(join(path, entry))
      return
    }
    files++
    hash.update(`${name}\0${stat.size}\0`)
    if (stat.isSymbolicLink()) {
      hash.update(readlinkSync(path))
      return
    }
    const fd = openSync(path, 'r')
    try {
      let length = readSync(fd, buffer, 0, buffer.length, null)
      while (length > 0) {
        hash.update(buffer.subarray(0, length))
        length = readSync(fd, buffer, 0, buffer.length, null)
      }
    }
    finally {
      closeSync(fd)
    }
  }
  visit(directory)
  return {
    digest: hash.digest('hex'),
    files,
  }
}

export function dependencyCompilations(output, modules) {
  const packages = new Set()
  for (const match of output.matchAll(/\/compile(?:\.exe)?\s[^\n]*?\s-p\s+(\S+)/g)) {
    const pkg = match[1]
    if (pkg !== 'main' && !modules.some(module => pkg === module || pkg.startsWith(`${module}/`)))
      packages.add(pkg)
  }
  return [...packages].sort()
}

/** Validate a real snapshot archive and prove Go dependency reuse in fresh builders. */
export async function runDockerCacheCheck({
  docker = realDocker,
  archive = realArchive,
  workspace = process.cwd(),
  platform = null,
  variant = 'alpine',
  versionBase = 'cache-check',
  log = () => {},
  buildArgs = null,
} = {}) {
  if (!['alpine', 'ubuntu'].includes(variant))
    throw new Error(`Unknown image variant ${variant}.`)
  const baseArgs = buildArgs ?? baseBuildArgs(process.env, variant)
  const parent = join(workspace, '.tmp', 'docker-cache-check')
  mkdirSync(parent, { recursive: true })
  const stageRoot = mkdtempSync(join(parent, 'run-'))
  const runId = basename(stageRoot)
  const logsPath = join(workspace, '.tmp', 'docker-cache-check-reports', runId)
  mkdirSync(logsPath, { recursive: true })
  const snapshotWorkspace = join(stageRoot, 'snapshot-workspace')
  mkdirSync(snapshotWorkspace)
  const modules = ['backend/go.mod', 'desktop/go/go.mod'].flatMap((file) => {
    const source = join(workspace, file)
    return existsSync(source)
      ? [/^module\s+(\S+)/m.exec(readFileSync(source, 'utf8'))?.[1]].filter(Boolean)
      : []
  })
  const builders = new Set()
  let commandIndex = 0
  function run(executable, runner, args, label) {
    log(`${label}: ${executable} ${args.slice(0, 4).join(' ')}...`)
    const started = performance.now()
    const result = runner(args)
    writeFileSync(join(logsPath, `${++commandIndex}-${executable}.log`), `${result.stdout ?? ''}\n${result.stderr ?? ''}\nEXIT: ${result.status}\n`)
    if (result.status !== 0)
      throw new Error(`${label} failed: ${(result.stderr ?? '').slice(-1500).trim()}. Logs: ${logsPath}`)
    return { ...result, seconds: (performance.now() - started) / 1000 }
  }
  function createBuilder(label) {
    const builder = `leapmux-cache-${runId}-${label}`
    run('docker', docker, ['buildx', 'create', '--name', builder], `Create ${label} builder`)
    builders.add(builder)
    return builder
  }
  function removeBuilder(builder) {
    try {
      const result = docker(['buildx', 'rm', '--force', builder])
      if (result.status !== 0)
        throw new Error(`Could not remove ${builder}: ${result.stderr ?? ''}`)
      builders.delete(builder)
      return null
    }
    catch (error) {
      log(error.message)
      return error
    }
  }
  const env = {
    ...process.env,
    RUNNER_OS: process.platform === 'win32'
      ? 'Windows'
      : process.platform === 'darwin'
        ? 'macOS'
        : 'Linux',
    RUNNER_ARCH: process.arch === 'arm64'
      ? 'ARM64'
      : 'X64',
    GOLANG_VERSION: process.env.GOLANG_VERSION ?? '0.0.0',
    RUNNER_TEMP: stageRoot,
    GITHUB_REF: 'refs/heads/main',
    GITHUB_EVENT_NAME: 'push',
  }
  const key = cacheKey('docker-go', {
    hash: 'c'.repeat(64),
    week: '2030-W01',
  }, env)
  const active = activeRootsFor('docker-go', env, snapshotWorkspace)[0].active
  const payload = snapshotPaths('docker-go', snapshotWorkspace).payloadRoot
  const readers = []
  let result
  let failure
  try {
    const writer = createBuilder('writer')
    const exports = []
    const writerRuns = []
    for (const suffix of ['a', 'b']) {
      const dest = join(stageRoot, `export-${suffix}`)
      writerRuns.push(run('docker', docker, exportCommand({
        builder: writer,
        dest,
        platform,
        version: `${versionBase}-${suffix}`,
        executionToken: `${runId}-export-${suffix}`,
        baseArgs,
      }), `Export writer ${suffix}`))
      if (!existsSync(dest) || readdirSync(dest).length === 0)
        throw new Error(`The ${suffix} export is empty.`)
      exports.push(fingerprint(dest))
    }
    if (exports[0].digest === exports[1].digest)
      throw new Error('The second export replayed the first instead of reading the changed mount.')
    const coldCompilations = dependencyCompilations(`${writerRuns[0].stdout}\n${writerRuns[0].stderr}`, modules)
    if (coldCompilations.length === 0)
      throw new Error(`The cold build recorded no dependency compilation; reuse cannot be verified. Logs: ${logsPath}`)
    rmSync(join(stageRoot, 'export-a'), { recursive: true, force: true })
    renameSync(join(stageRoot, 'export-b'), active)
    const prepared = await prepareCacheSnapshot({
      kind: 'docker-go',
      key,
      workspace: snapshotWorkspace,
      env,
    })
    if (prepared.result !== 'ready')
      throw new Error(`The snapshot could not prepare: ${prepared.result}`)
    const archivePath = join(stageRoot, 'snapshot.tar')
    run('tar', archive, [
      '--format=pax',
      '-cf',
      archivePath,
      '-C',
      payload,
      '.',
    ], 'Archive the snapshot')
    const archiveBytes = statSync(archivePath).size
    cleanupSnapshotStaging({ kind: 'docker-go', workspace: snapshotWorkspace })
    removeBuilder(writer)
    for (const mode of ['reader', 'exact-hit']) {
      rmSync(active, { recursive: true, force: true })
      mkdirSync(payload, { recursive: true })
      run('tar', archive, ['-xf', archivePath, '-C', payload], `Restore ${mode} archive`)
      const activated = await activateCacheSnapshot({
        kind: 'docker-go',
        key,
        workspace: snapshotWorkspace,
        env,
        matchedKey: key,
        writer: mode === 'exact-hit',
      })
      if (!activated.cacheHit || activated.saveRequired)
        throw new Error(`The ${mode} must reuse its exact snapshot without publishing.`)
      const restored = fingerprint(active)
      if (restored.digest !== exports[1].digest)
        throw new Error(`The ${mode} snapshot contents changed during the archive round trip.`)
      const builder = createBuilder(mode)
      run('docker', docker, importCommand({
        builder,
        source: active,
        executionToken: `${runId}-${mode}-import`,
        platform,
        version: `${versionBase}-b`,
        baseArgs,
      }), `Import ${mode} snapshot`)
      const rebuilt = run('docker', docker, warmBuildCommand({
        builder,
        executionToken: `${runId}-${mode}-build`,
        platform,
        version: `${versionBase}-b`,
        baseArgs,
      }), `Build ${mode} with restored Go cache`)
      const compiled = dependencyCompilations(`${rebuilt.stdout}\n${rebuilt.stderr}`, modules)
      if (compiled.length > 0)
        throw new Error(`The ${mode} rebuilt ${compiled.length} dependencies: ${compiled.slice(0, 8).join(', ')}. Logs: ${logsPath}`)
      readers.push({
        mode,
        dependencyCompilations: compiled.length,
        seconds: rebuilt.seconds,
        files: restored.files,
      })
      removeBuilder(builder)
    }
    result = {
      exports,
      readers,
      coldDependencyCompilations: coldCompilations.length,
      coldWriterSeconds: writerRuns[0].seconds,
      changedWriterSeconds: writerRuns[1].seconds,
      selectedBytes: prepared.report['selected-bytes'],
      archiveBytes,
      logsPath,
    }
  }
  catch (error) {
    failure = error
  }
  finally {
    for (const builder of [...builders]) {
      const cleanupError = removeBuilder(builder)
      failure ??= cleanupError
    }
    try {
      rmSync(stageRoot, { recursive: true, force: true })
      try {
        rmdirSync(parent)
      }
      catch (error) {
        if (error.code !== 'ENOTEMPTY' && error.code !== 'ENOENT' && error.code !== 'EEXIST')
          failure ??= error
      }
    }
    catch (error) {
      failure ??= error
    }
  }
  if (failure)
    throw failure
  return result
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (isMain) {
  const { values } = parseArgs({
    options: {
      variant: { type: 'string', default: 'alpine' },
      platform: { type: 'string' },
    },
  })
  runDockerCacheCheck({
    variant: values.variant,
    platform: values.platform,
    log: message => console.log(message),
  }).then((result) => {
    console.log(`Cold writer: ${result.coldWriterSeconds.toFixed(2)}s; changed writer: ${result.changedWriterSeconds.toFixed(2)}s.`)
    console.log(`Snapshot: ${result.selectedBytes} selected bytes; ${result.archiveBytes} tar bytes; ${result.coldDependencyCompilations} cold dependency compilations.`)
    for (const reader of result.readers) console.log(`${reader.mode}: ${reader.seconds.toFixed(2)}s; ${reader.files} restored files; ${reader.dependencyCompilations} dependency compilations.`)
    console.log(`Docker cache check passed. Logs: ${result.logsPath}`)
  }).catch((error) => {
    console.error(`Docker cache check failed: ${error.message}`)
    process.exitCode = 1
  })
}
