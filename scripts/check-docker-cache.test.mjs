import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { dependencyCompilations, exportCommand, importCommand, runDockerCacheCheck, warmBuildCommand } from './check-docker-cache.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
function target(args) {
  const index = args.indexOf('--target')
  return index === -1
    ? null
    : args[index + 1]
}
function fakeDocker(options = {}) {
  const commands = []
  const imports = []
  const importedBuilders = new Set()
  let exportsSeen = 0
  const runner = (args) => {
    commands.push(args)
    if (args.includes('--no-cache-filter') && target(args) === 'builder')
      importedBuilders.delete(args[args.indexOf('--builder') + 1])
    if ((options.failOn && args[0] === options.failOn) || (options.failOnTarget && target(args) === options.failOnTarget))
      return { status: 1, stderr: 'fixture failure', stdout: '' }
    let stdout = ''
    if (target(args) === 'go-cache-files') {
      exportsSeen++
      const dest = args.find(arg => arg.startsWith('type=local,dest=')).slice('type=local,dest='.length)
      mkdirSync(dest, { recursive: true })
      if (!options.emptyExport) {
        const generation = options.identicalExports ? 1 : exportsSeen
        for (const name of [`hash-${generation}-a`, `hash-${generation}-b`]) writeFileSync(join(dest, name), 'compiled dependency\n')
      }
      stdout = '/usr/local/go/pkg/tool/linux_arm64/compile -o object -p example.com/dependency source.go\n'
    }
    if (target(args) === 'go-cache-import') {
      const source = args[args.indexOf('--build-context') + 1].slice('go-cache-input='.length)
      const files = readdirSync(source).sort()
      imports.push({
        builder: args[args.indexOf('--builder') + 1],
        files,
        contents: files.map(file => readFileSync(join(source, file), 'utf8')),
      })
      importedBuilders.add(args[args.indexOf('--builder') + 1])
    }
    if (target(args) === 'builder' && (options.rebuildDependency || !importedBuilders.has(args[args.indexOf('--builder') + 1])))
      stdout = '/usr/local/go/pkg/tool/linux_arm64/compile -o object -p example.com/dependency source.go\n'
    return { status: 0, stderr: '', stdout }
  }
  runner.commands = commands
  runner.imports = imports
  return runner
}

describe('runDockerCacheCheck', () => {
  it('preserves staging data owned by another check when a build fails', async () => {
    const workspace = scratch.create('docker-cache-concurrent')
    const sibling = join(workspace, '.tmp', 'docker-cache-check', 'another-check')
    mkdirSync(sibling, { recursive: true })
    writeFileSync(join(sibling, 'owned'), 'another check')
    await expect(runDockerCacheCheck({
      workspace,
      docker: fakeDocker({ failOn: 'buildx' }),
      buildArgs: [],
    })).rejects.toThrow('failed')
    expect(existsSync(join(sibling, 'owned'))).toBe(true)
  })

  it('imports actual restored snapshot files into fresh readers before checking reuse', async () => {
    const workspace = scratch.create('docker-cache-round-trip')
    const docker = fakeDocker()
    const baseArgs = ['--build-arg', 'BASE_IMAGE=alpine:fixture']
    const result = await runDockerCacheCheck({ workspace, docker, buildArgs: baseArgs })
    expect(result.exports).toHaveLength(2)
    expect(result.exports[0].digest).not.toBe(result.exports[1].digest)
    expect(result.coldDependencyCompilations).toBe(1)
    expect(result.archiveBytes).toBeGreaterThan(result.selectedBytes)
    expect(docker.imports).toHaveLength(2)
    expect(new Set(docker.imports.map(entry => entry.builder)).size).toBe(2)
    for (const entry of docker.imports) {
      expect(entry.files).toEqual(['hash-2-a', 'hash-2-b'])
      expect(entry.contents).toEqual(['compiled dependency\n', 'compiled dependency\n'])
    }
    expect(result.readers.map(entry => entry.mode)).toEqual(['reader', 'exact-hit'])
    for (const reader of result.readers) expect(reader.dependencyCompilations).toBe(0)
    const builds = docker.commands.filter(args => args[1] === 'build')
    expect(builds).toHaveLength(6)
    for (const build of builds) expect(build).toEqual(expect.arrayContaining(baseArgs))
    expect(builds.filter(args => target(args) === 'go-cache-files')).toHaveLength(2)
    expect(builds.filter(args => target(args) === 'builder')).toHaveLength(2)
    const created = docker.commands.filter(args => args[1] === 'create').map(args => args[args.indexOf('--name') + 1])
    const removed = docker.commands.filter(args => args[1] === 'rm').map(args => args.at(-1))
    expect(removed.sort()).toEqual(created.sort())
    expect(existsSync(join(workspace, '.tmp', 'docker-cache-check'))).toBe(false)
    expect(existsSync(result.logsPath)).toBe(true)
  })

  it('stops publication after a failed export and removes its own builder', async () => {
    const workspace = scratch.create('docker-cache-export-failure')
    const docker = fakeDocker({ failOnTarget: 'go-cache-files' })
    await expect(runDockerCacheCheck({ workspace, docker, buildArgs: [] })).rejects.toThrow('Export writer a failed')
    expect(docker.imports).toEqual([])
    expect(docker.commands.at(-1)[1]).toBe('rm')
  })

  it.each([
    [
      { emptyExport: true },
      'empty',
    ],
    [
      { identicalExports: true },
      'replayed',
    ],
    [
      { rebuildDependency: true },
      'rebuilt 1 dependencies',
    ],
  ])('rejects invalid cache evidence and removes the builders', async (options, error) => {
    const docker = fakeDocker(options)
    await expect(runDockerCacheCheck({
      workspace: scratch.create('docker-cache-invalid'),
      docker,
      buildArgs: [],
    })).rejects.toThrow(error)
    const created = docker.commands.filter(args => args[1] === 'create').map(args => args[args.indexOf('--name') + 1])
    const removed = docker.commands.filter(args => args[1] === 'rm').map(args => args.at(-1))
    expect(removed.sort()).toEqual(created.sort())
  })

  it('preserves the original error when archive creation fails', async () => {
    const docker = fakeDocker()
    await expect(runDockerCacheCheck({
      workspace: scratch.create('docker-cache-archive-failure'),
      docker,
      buildArgs: [],
      archive: () => ({ status: 1, stderr: 'archive fixture failure' }),
    })).rejects.toThrow('archive fixture failure')
    expect(docker.imports).toEqual([])
    expect(docker.commands.at(-1)[1]).toBe('rm')
  })

  it('refuses an unsupported variant before creating any builder', async () => {
    const docker = fakeDocker()
    await expect(runDockerCacheCheck({ variant: 'unknown', docker, buildArgs: [] })).rejects.toThrow('Unknown image variant')
    expect(docker.commands).toEqual([])
  })
})

describe('Docker cache commands', () => {
  it('reruns stage commands without requesting a cache-mount reset', () => {
    const options = {
      builder: 'fixture',
      version: 'v',
      dest: '/output',
      source: '/restored',
      executionToken: 'unique-run',
    }
    for (const command of [
      exportCommand(options),
      importCommand(options),
      warmBuildCommand(options),
    ]) {
      expect(command).not.toContain('--no-cache-filter')
      expect(command.some(arg => /^GO_CACHE_\w+_TOKEN=unique-run$/.test(arg))).toBe(true)
    }
  })
  it('uses the production import and export stages with their current cache mounts', () => {
    const options = {
      builder: 'fixture',
      platform: 'linux/arm64',
      version: 'v',
      dest: '/output',
      source: '/restored',
      executionToken: 'fixture-token',
    }
    const exported = exportCommand(options)
    const imported = importCommand(options)
    expect(target(exported)).toBe('go-cache-files')
    expect(exported).toContain('GO_CACHE_EXPORT_TOKEN=fixture-token')
    expect(target(imported)).toBe('go-cache-import')
    expect(imported).toContain('go-cache-input=/restored')
    expect(imported).toContain('GO_CACHE_IMPORT_TOKEN=fixture-token')
    const reader = warmBuildCommand(options)
    expect(target(reader)).toBe('builder')
    expect(reader).toContain('GO_CACHE_BUILD_TOKEN=fixture-token')
    expect(reader.some(arg => arg.startsWith('type=local'))).toBe(false)
    for (const args of [imported, exported, reader]) expect(args).toContain('GOFLAGS=-x')
    const dockerfile = readFileSync(join(import.meta.dirname, '..', 'docker', 'Dockerfile'), 'utf8')
    for (const stage of ['go-cache-import', 'go-cache-export', 'go-cache-files']) expect(dockerfile).toContain(`AS ${stage}`)
  })
})

describe('dependencyCompilations', () => {
  it('counts compiler commands while excluding workspace packages and linking', () => {
    const text = [
      '/go/pkg/tool/linux_arm64/compile -o object -p example.com/dependency file.go',
      '/go/pkg/tool/linux_arm64/compile -o object -p example.com/app/sub file.go',
      '/go/pkg/tool/linux_arm64/compile -o object -p main file.go',
      '/go/pkg/tool/linux_arm64/link -o app object',
    ].join('\n')
    expect(dependencyCompilations(text, ['example.com/app'])).toEqual(['example.com/dependency'])
  })
})
