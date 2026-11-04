// Integration check for the Docker Go cache export. It builds the export
// stages of docker/Dockerfile against a dedicated Buildx builder, proves
// that the export reads the current cache mount content, and removes the
// builder and its staging data afterwards. It never pushes an image and it
// never writes a shared cache.
//
// `task test-docker-cache` runs this check. The scenarios:
// - writer: a cold build populates the mount, the export stages it, a build
//   with a changed VERSION value mutates the mount, and the second export
//   must differ from the first; the snapshot preparation then validates the
//   exported tree.
// - reader and exact-hit: warm rebuilds that must issue no export command,
//   because neither case publishes a replacement.
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readdirSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { prepareCacheSnapshot } from './cache-snapshot.mjs'

const DOCKER_FILE = 'docker/Dockerfile'
const EXPORT_TARGET = 'go-cache-files'
const NO_CACHE_FILTER = 'go-cache-export'

// The base-image and toolchain build arguments that every docker/Dockerfile
// build needs, read from the environment that the Task target fills from
// versions.env. BASE_IMAGE composes from the variant's image exactly as
// setup-docker-build does; VERSION is per export and stays separate.
function baseBuildArgs(env = process.env, variant = 'alpine') {
  const baseImage = variant === 'ubuntu'
    ? env.UBUNTU_IMAGE
    : env.ALPINE_IMAGE
  const composed = {
    BASE_IMAGE: baseImage,
    BUF_VERSION: env.BUF_VERSION,
    TASK_VERSION: env.TASK_VERSION,
    S6_OVERLAY_VERSION: env.S6_OVERLAY_VERSION,
    BUN_VERSION: env.BUN_VERSION,
    NODE_VERSION: env.NODE_VERSION,
    GOLANG_VERSION: env.GOLANG_VERSION,
  }
  const missing = Object.entries(composed).filter(([, value]) => !value).map(([name]) => name)
  if (missing.length > 0)
    throw new Error(`The Docker cache check needs the build arguments that the task target fills from versions.env; missing: ${missing.join(', ')}.`)
  return Object.entries(composed).flatMap(([name, value]) => ['--build-arg', `${name}=${value}`])
}

function realDocker(args, { timeout = 600000 } = {}) {
  const result = spawnSync('docker', args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, timeout })
  return {
    status: result.status,
    stderr: result.stderr ?? result.error?.message ?? '',
    stdout: result.stdout ?? '',
  }
}

/** The buildx build arguments of one export of the Go cache mount. */
export function exportCommand({ builder, dest, platform, version, dockerFile = DOCKER_FILE, baseArgs = [] }) {
  return [
    'buildx',
    'build',
    '--builder',
    builder,
    '--file',
    dockerFile,
    '--target',
    EXPORT_TARGET,
    '--no-cache-filter',
    NO_CACHE_FILTER,
    '--output',
    `type=local,dest=${dest}`,
    ...(platform
      ? ['--platform', platform]
      : []),
    ...baseArgs,
    '--build-arg',
    `VERSION=${version}`,
    '.',
  ]
}

/** The buildx build arguments of one warm rebuild that must not export. */
export function warmBuildCommand({ builder, platform, version, dockerFile = DOCKER_FILE, baseArgs = [] }) {
  return [
    'buildx',
    'build',
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
    '.',
  ]
}

function fingerprint(directory) {
  let hash = createHash('sha256')
  let files = 0
  const stack = [directory]
  while (stack.length > 0) {
    const current = stack.pop()
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name)
      if (entry.isDirectory()) {
        stack.push(path)
        continue
      }
      files++
      hash = hash.update(entry.name)
    }
  }
  return {
    digest: hash.digest('hex'),
    files,
  }
}

function run(docker, args, what, log) {
  log(`docker ${args.slice(0, 4).join(' ')}...`)
  const result = docker(args)
  if (result.status !== 0)
    throw new Error(`${what} failed: ${result.stderr.slice(-1500).trim()}`)
  return result
}

/**
 * Run the Docker Go cache export check. Returns the measurements of the
 * two exports. The dedicated builder and the staging directory are removed
 * on every exit path.
 */
export async function runDockerCacheCheck({
  mode = 'writer',
  docker = realDocker,
  workspace = process.cwd(),
  platform = null,
  variant = 'alpine',
  versionBase = `cache-check-${process.pid}`,
  log = () => {},
  buildArgs = null,
} = {}) {
  if (mode !== 'writer' && mode !== 'reader' && mode !== 'exact-hit')
    throw new Error(`Unknown check mode ${mode}; use writer, reader, or exact-hit.`)
  if (variant !== 'alpine' && variant !== 'ubuntu')
    throw new Error(`Unknown image variant ${variant}; use alpine or ubuntu.`)
  const builder = `leapmux-cache-check-${process.pid}`
  const stageRoot = join(workspace, '.tmp', 'docker-cache-check', String(process.pid))
  const exports = []
  try {
    mkdirSync(stageRoot, { recursive: true })
    const baseArgs = buildArgs ?? baseBuildArgs(process.env, variant)
    if (mode === 'writer') {
      run(docker, ['buildx', 'create', '--name', builder], 'builder creation', log)
      const firstDest = join(stageRoot, 'export-1')
      const secondDest = join(stageRoot, 'export-2')
      run(docker, exportCommand({
        builder,
        dest: firstDest,
        platform,
        version: `${versionBase}-a`,
        baseArgs,
      }), 'the first export', log)
      if (!existsSync(firstDest) || readdirSync(firstDest).length === 0)
        throw new Error('The first export is empty; the export stage read nothing from the mount.')
      const first = fingerprint(firstDest)
      run(docker, exportCommand({
        builder,
        dest: secondDest,
        platform,
        version: `${versionBase}-b`,
        baseArgs,
      }), 'the second export', log)
      if (!existsSync(secondDest) || readdirSync(secondDest).length === 0)
        throw new Error('The second export is empty; the export stage read nothing from the mount.')
      const second = fingerprint(secondDest)
      if (second.digest === first.digest)
        throw new Error('The second export equals the first; the export stage replayed a cached layer instead of reading the changed mount.')
      exports.push(first, second)

      // The production preparation must accept the exported tree as a
      // docker-go snapshot payload.
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
      const { activeRootsFor, partitionPrefix } = await import('./cache-policy.mjs')
      // The export destination of the build action is the active root that
      // preparation reads; mirror that here.
      const activeRoot = activeRootsFor('docker-go', env, workspace)[0].active
      rmSync(activeRoot, { recursive: true, force: true })
      mkdirSync(activeRoot, { recursive: true })
      // The export wrote to firstDest; stage it at the active root.
      const { cpSync } = await import('node:fs')
      cpSync(firstDest, activeRoot, { recursive: true })
      const key = `${partitionPrefix('docker-go', env)}${'c'.repeat(64)}-2030-W01`
      const prepared = await prepareCacheSnapshot({
        kind: 'docker-go',
        key,
        workspace,
        env,
      })
      if (prepared.result !== 'ready')
        throw new Error(`The exported Go cache did not prepare as a docker-go snapshot: ${prepared.result}`)
      log(`Prepared the docker-go snapshot: ${prepared.report['selected-files']} files, ${prepared.report['selected-bytes']} bytes.`)
      // The save action's always() cleanup removes the staging payload; the
      // check mirrors it so no staging survives the run.
      const { cleanupSnapshotStaging } = await import('./cache-snapshot.mjs')
      cleanupSnapshotStaging({ kind: 'docker-go', workspace })
    }
    else {
      // Reader and exact-hit modes rebuild warm and must issue no export.
      run(docker, ['buildx', 'create', '--name', builder], 'builder creation', log)
      run(docker, warmBuildCommand({
        builder,
        platform,
        version: `${versionBase}-warm`,
        baseArgs,
      }), 'the warm rebuild', log)
    }
    return { exports }
  }
  finally {
    // Cleanup must never mask the failure that brought the run here: a
    // builder that the platform already forgot is not an error.
    const removed = docker(['buildx', 'rm', '--force', builder])
    if (removed.status !== 0)
      log(`Note: the builder removal reported: ${removed.stderr.trim().split('\n').at(-1) ?? ''}`)
    rmSync(stageRoot, { recursive: true, force: true })
    // Leave no empty per-run parent behind; a concurrent run's directory
    // makes the removal fail, which is fine.
    try {
      rmSync(dirname(stageRoot), { recursive: true })
    }
    catch {}
  }
}

function parseArgs(argv) {
  const args = { mode: 'writer', variant: 'alpine' }
  for (let index = 0; index < argv.length; index++) {
    if (argv[index] === '--mode') {
      args.mode = argv[index + 1]
      index++
      continue
    }
    if (argv[index] === '--variant') {
      args.variant = argv[index + 1]
      index++
      continue
    }
    if (argv[index] === '--platform') {
      args.platform = argv[index + 1]
      index++
    }
  }
  return args
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)

if (isMain) {
  const { mode, platform, variant } = parseArgs(process.argv.slice(2))
  runDockerCacheCheck({
    mode,
    platform,
    variant,
    log: message => console.log(message),
  }).then((result) => {
    for (const [index, export_] of result.exports.entries())
      console.log(`Export ${index + 1}: ${export_.files} files, fingerprint ${export_.digest.slice(0, 12)}.`)
    console.log('Docker cache check passed.')
  }).catch((error) => {
    console.error(`Docker cache check failed: ${error.message}`)
    process.exitCode = 1
  })
}
