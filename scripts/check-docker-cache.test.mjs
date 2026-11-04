import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { exportCommand, runDockerCacheCheck, warmBuildCommand } from './check-docker-cache.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

function fakeDocker(options = {}) {
  const commands = []
  const failOn = options.failOn ?? null
  const emptyExport = options.emptyExport ?? false
  const identicalExports = options.identicalExports ?? false
  let exportsSeen = 0
  const runner = (args) => {
    commands.push(args)
    if (failOn !== null && args[0] === failOn)
      return { status: 1, stderr: `fixture ${failOn} failure`, stdout: '' }
    if (args[0] === 'buildx' && args[1] === 'build' && args.some(arg => arg.startsWith('type=local,dest='))) {
      exportsSeen++
      const dest = args.find(arg => arg.startsWith('type=local,dest=')).slice('type=local,dest='.length)
      // A real local exporter creates the destination and writes many files
      // under hash directories. An identical second export replays the
      // first content, which the check must reject.
      const contentGeneration = identicalExports ? 1 : exportsSeen
      mkdirSync(dest, { recursive: true })
      if (!emptyExport) {
        for (const name of [`hash-${contentGeneration}-a`, `hash-${contentGeneration}-b`])
          writeFileSync(join(dest, name), 'payload\n')
      }
    }
    return { status: 0, stderr: '', stdout: '' }
  }
  runner.commands = commands
  return runner
}

describe('runDockerCacheCheck', () => {
  it('creates a dedicated builder, exports twice with the no-cache filter, and removes everything', async () => {
    const workspace = scratch.create('docker-cache-check')
    const docker = fakeDocker()
    const baseArgs = ['--build-arg', 'BASE_IMAGE=alpine:fixture']
    const result = await runDockerCacheCheck({
      mode: 'writer',
      docker,
      workspace,
      log: () => {},
      buildArgs: baseArgs,
    })
    expect(result.exports).toHaveLength(2)
    // Every build carries the base-image arguments that the Dockerfile needs.
    for (const build of buildsPlaceholder(docker)) expect(build).toEqual(expect.arrayContaining(baseArgs))
    function buildsPlaceholder() {
      return docker.commands.filter(args => args[0] === 'buildx' && args[1] === 'build')
    }
    const builds = docker.commands.filter(args => args[0] === 'buildx' && args[1] === 'build')
    expect(builds).toHaveLength(2)
    for (const build of builds) {
      // The buildx CLI flag is singular; the build-push-action input that
      // the workflow uses is plural.
      expect(build).toContain('--no-cache-filter')
      expect(build[build.indexOf('--no-cache-filter') + 1]).toBe('go-cache-export')
      expect(build[build.indexOf('--target') + 1]).toBe('go-cache-files')
      expect(build.some(arg => arg.startsWith('type=local,dest='))).toBe(true)
    }
    // The second export carries the changed VERSION value.
    const versions = builds.map(build => build.filter(arg => arg.startsWith('VERSION=')).join(''))
    expect(versions[0]).not.toBe(versions[1])
    expect(versions[0]).toContain('-a')
    expect(versions[1]).toContain('-b')
    // The builder exists only between create and rm.
    const create = docker.commands.findIndex(args => args[0] === 'buildx' && args[1] === 'create')
    const remove = docker.commands.findIndex(args => args[0] === 'buildx' && args[1] === 'rm')
    expect(create).toBeGreaterThan(-1)
    expect(remove).toBeGreaterThan(create)
    expect(remove).toBe(docker.commands.length - 1)
    // The staging directory is removed.
    const leftovers = readdirSync(join(workspace, '.tmp')).filter(name => name.startsWith('docker-cache-check'))
    expect(leftovers).toEqual([])
  })

  it('stops snapshot publication when an export fails, and still removes the builder', async () => {
    const workspace = scratch.create('docker-cache-check')
    const docker = fakeDocker({ failOn: 'buildx' })
    await expect(runDockerCacheCheck({
      mode: 'writer',
      docker,
      workspace,
      log: () => {},
      buildArgs: ['--build-arg', 'BASE_IMAGE=alpine:fixture'],
    })).rejects.toThrow('failed')
    expect(docker.commands.filter(args => args[0] === 'buildx' && args[1] === 'build')).toHaveLength(0)
    expect(docker.commands[docker.commands.length - 1][1]).toBe('rm')
  })

  it('rejects an empty export', async () => {
    const workspace = scratch.create('docker-cache-check')
    const docker = fakeDocker({ emptyExport: true })
    await expect(runDockerCacheCheck({
      mode: 'writer',
      docker,
      workspace,
      log: () => {},
      buildArgs: ['--build-arg', 'BASE_IMAGE=alpine:fixture'],
    })).rejects.toThrow('empty')
    expect(docker.commands[docker.commands.length - 1][1]).toBe('rm')
  })

  it('rejects a second export that replayed the first instead of reading the mount', async () => {
    const workspace = scratch.create('docker-cache-check')
    const docker = fakeDocker({ identicalExports: true })
    await expect(runDockerCacheCheck({
      mode: 'writer',
      docker,
      workspace,
      log: () => {},
      buildArgs: ['--build-arg', 'BASE_IMAGE=alpine:fixture'],
    })).rejects.toThrow('replayed a cached layer')
  })

  it('issues no export command for a reader or an exact-hit writer', async () => {
    for (const mode of ['reader', 'exact-hit']) {
      const workspace = scratch.create('docker-cache-check')
      const docker = fakeDocker()
      await runDockerCacheCheck({
        mode,
        docker,
        workspace,
        log: () => {},
        buildArgs: ['--build-arg', 'BASE_IMAGE=alpine:fixture'],
      })
      const builds = docker.commands.filter(args => args[0] === 'buildx' && args[1] === 'build')
      expect(builds, mode).toHaveLength(1)
      // The warm rebuild carries neither the export target nor the output.
      expect(builds[0]).not.toContain('--no-cache-filter')
      expect(builds[0]).not.toContain('--target')
      expect(builds[0].some(arg => arg.startsWith('type=local'))).toBe(false)
    }
  })

  it('refuses an unknown mode before any docker command', async () => {
    const docker = fakeDocker()
    await expect(runDockerCacheCheck({
      mode: 'sometimes',
      docker,
      log: () => {},
    })).rejects.toThrow('Unknown check mode')
    expect(docker.commands).toEqual([])
  })
})

describe('exportCommand', () => {
  it('reads the production Dockerfile and forces the export stage to rebuild', () => {
    const args = exportCommand({
      builder: 'probe',
      dest: '/tmp/out',
      platform: 'linux/arm64',
      version: 'v',
    })
    expect(args).toContain('docker/Dockerfile')
    expect(args[args.indexOf('--target') + 1]).toBe('go-cache-files')
    expect(args[args.indexOf('--no-cache-filter') + 1]).toBe('go-cache-export')
    expect(args[args.indexOf('--output') + 1]).toBe('type=local,dest=/tmp/out')
    expect(args[args.indexOf('--platform') + 1]).toBe('linux/arm64')
    expect(args.at(-1)).toBe('.')
  })

  it('uses the production stage names from the Dockerfile', () => {
    // A rename in the Dockerfile must update this check.
    const dockerfile = readFileSync(join(import.meta.dirname, '..', 'docker', 'Dockerfile'), 'utf8')
    expect(dockerfile).toContain('AS go-cache-export')
    expect(dockerfile).toContain('AS go-cache-files')
  })

  it('builds a warm rebuild without any export argument', () => {
    const args = warmBuildCommand({ builder: 'probe', platform: null, version: 'v' })
    expect(args).not.toContain('--no-cache-filters')
    expect(args).not.toContain('--target')
    expect(args).not.toContain('--output')
  })
})
