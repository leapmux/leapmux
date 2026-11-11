import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, rmSync, watch, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { EXAMPLES_DIR, PROXIES, ROOT } from './reverse-proxy-examples.mjs'
import { DockerSession, isMissingDockerResource, runCommand } from './reverse-proxy-runtime.mjs'
import { ProxyEnvironment, runReverseProxyTests } from './test-reverse-proxies.mjs'

async function withRuntimeDirectory(check) {
  mkdirSync(join(ROOT, '.tmp'), { recursive: true })
  const directory = mkdtempSync(join(ROOT, '.tmp', 'reverse-proxy-runtime-'))
  try {
    return await check(directory)
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

function watchForFile(path) {
  let observer
  const ready = new Promise((resolve, reject) => {
    observer = watch(dirname(path), () => {
      if (existsSync(path))
        resolve()
    })
    observer.on('error', reject)
  })
  return {
    ready,
    close: () => observer?.close(),
  }
}

function dockerFixture(onRun) {
  const containers = new Set()
  const calls = []
  const execute = async (command, args, options = {}) => {
    calls.push({ command, args, signal: options.signal })
    if (args[0] === 'run') {
      const position = args.indexOf('--name')
      const name = position < 0
        ? 'untracked-helper'
        : args[position + 1]
      containers.add(name)
      return onRun(options, args)
    }
    if (args[0] === 'rm') {
      expect(options.signal).toBeUndefined()
      return containers.delete(args.at(-1))
        ? { status: 0, stdout: '', stderr: '' }
        : { status: 1, stdout: '', stderr: `No such container: ${args.at(-1)}` }
    }
    throw new Error(`Unexpected Docker command: ${args.join(' ')}`)
  }
  return { execute, containers, calls }
}

describe('runCommand', () => {
  it('preserves output and a nonzero exit status', async () => {
    const result = await runCommand(process.execPath, ['-e', 'process.stdout.write("output"); process.stderr.write("detail"); process.exit(7)'])
    expect(result).toEqual({ status: 7, stdout: 'output', stderr: 'detail' })
  })

  it('reports a command that cannot start', async () => {
    await withRuntimeDirectory(async (directory) => {
      await expect(runCommand(join(directory, 'missing-command'), [])).rejects.toMatchObject({ code: 'ENOENT' })
    })
  })

  it('limits a command that never exits', async () => {
    const result = await runCommand(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { timeoutMs: 1 })
    expect(result.status).toBe(128)
  })

  it('cancels a running command after its readiness event', async () => {
    await withRuntimeDirectory(async (directory) => {
      const ready = join(directory, 'ready')
      const release = join(directory, 'release')
      const script = join(directory, 'child.mjs')
      writeFileSync(script, `
import { existsSync, watch, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import process from 'node:process'
const [ready, release] = process.argv.slice(2)
const finish = () => {
  if (existsSync(release)) {
    observer.close()
    process.exit(42)
  }
}
const observer = watch(dirname(release), finish)
writeFileSync(ready, 'ready')
finish()
`)
      const observer = watchForFile(ready)
      const controller = new AbortController()
      const command = runCommand(process.execPath, [script, ready, release], { signal: controller.signal })
      try {
        await Promise.race([
          observer.ready,
          command.then(() => { throw new Error('The command ended before its readiness event') }),
        ])
        controller.abort()
        // An implementation that ignores cancellation exits normally with 42.
        writeFileSync(release, '')
        await expect(command).rejects.toMatchObject({ name: 'AbortError' })
      }
      finally {
        writeFileSync(release, '')
        observer.close()
        await command.catch(() => {})
      }
    })
  })

  it('does not launch a command when its signal already ended', async () => {
    await withRuntimeDirectory(async (directory) => {
      const marker = join(directory, 'started')
      const controller = new AbortController()
      controller.abort()
      await expect(runCommand(process.execPath, ['-e', 'require("node:fs").writeFileSync(process.argv[1], "started")', marker], {
        signal: controller.signal,
      })).rejects.toMatchObject({ name: 'AbortError' })
      expect(existsSync(marker)).toBe(false)
    })
  })
})

describe('DockerSession', () => {
  it('prevents new verification commands after cancellation while allowing cleanup', async () => {
    const controller = new AbortController()
    controller.abort()
    const fixture = dockerFixture(async () => ({ status: 0, stdout: '', stderr: '' }))
    const session = new DockerSession(controller.signal, fixture.execute)
    await expect(session.run(['run', 'test-image'])).rejects.toMatchObject({ name: 'AbortError' })
    expect(fixture.calls).toHaveLength(0)
    await session.cleanupRun(['rm', '--force', 'absent'])
    expect(fixture.calls).toHaveLength(1)
  })

  it('removes a helper container after its client times out', async () => {
    const fixture = dockerFixture(async () => ({ status: 128, stdout: 'started', stderr: '' }))
    const session = new DockerSession(new AbortController().signal, fixture.execute)
    const result = await session.runContainer(['--rm', 'test-image'])
    expect(result.status).toBe(128)
    expect(fixture.containers.size).toBe(0)
    const start = fixture.calls.find(call => call.args[0] === 'run')
    expect(start.args).toContain('--name')
  })

  it('removes a helper container after its command rejects', async () => {
    const failure = new Error('client failed after container creation')
    const fixture = dockerFixture(async () => {
      throw failure
    })
    const session = new DockerSession(new AbortController().signal, fixture.execute)
    await expect(session.runContainer(['--rm', 'test-image'])).rejects.toBe(failure)
    expect(fixture.containers.size).toBe(0)
  })

  it('runs helper cleanup after verification is canceled', async () => {
    const controller = new AbortController()
    const fixture = dockerFixture(async () => {
      controller.abort()
      throw controller.signal.reason
    })
    const session = new DockerSession(controller.signal, fixture.execute)
    await expect(session.runContainer(['--rm', 'test-image'])).rejects.toMatchObject({ name: 'AbortError' })
    expect(fixture.containers.size).toBe(0)
    expect(fixture.calls.some(call => call.args[0] === 'rm')).toBe(true)
  })

  it('accepts a helper that Docker already removed', async () => {
    const fixture = dockerFixture(async (_, args) => {
      fixture.containers.delete(args[args.indexOf('--name') + 1])
      return { status: 0, stdout: '', stderr: '' }
    })
    const session = new DockerSession(new AbortController().signal, fixture.execute)
    await session.runContainer(['--rm', 'test-image'])
    expect(await session.cleanup()).toEqual([])
    expect(fixture.calls).toHaveLength(2)
  })

  it('reports a missing dependency as a cleanup failure', async () => {
    const fixture = dockerFixture(async () => ({ status: 0, stdout: '', stderr: '' }))
    const execute = (command, args, options) => args[0] === 'rm'
      ? Promise.resolve({ status: 1, stdout: '', stderr: 'volume plugin not found' })
      : fixture.execute(command, args, options)
    const session = new DockerSession(new AbortController().signal, execute)
    await expect(session.runContainer(['--rm', 'test-image'])).rejects.toThrow('volume plugin not found')
    expect(fixture.containers.size).toBe(1)
    const failures = await session.cleanup()
    expect(failures).toHaveLength(1)
    expect(failures[0].message).toContain('volume plugin not found')
  })

  it('gives concurrent helper containers distinct identities and removes both', async () => {
    let release
    let enter
    const held = new Promise((resolve) => {
      release = resolve
    })
    const entered = new Promise((resolve) => {
      enter = resolve
    })
    let starts = 0
    const fixture = dockerFixture(async () => {
      if (++starts === 2)
        enter()
      await held
      return { status: 128, stdout: '', stderr: '' }
    })
    const session = new DockerSession(new AbortController().signal, fixture.execute)
    const commands = [
      session.runContainer(['--rm', 'first-image']),
      session.runContainer(['--rm', 'second-image']),
    ]
    try {
      await entered
      expect(fixture.containers.size).toBe(2)
      release()
      await Promise.all(commands)
      expect(fixture.containers.size).toBe(0)
    }
    finally {
      release()
      await Promise.allSettled(commands)
    }
  })

  for (const outcome of ['rejects', 'fails', 'succeeds']) {
    it(`reports cleanup failure when its helper command ${outcome}`, async () => {
      const original = new Error('command failed')
      const fixture = dockerFixture(async () => {
        if (outcome === 'rejects')
          throw original
        return {
          status: outcome === 'fails'
            ? 7
            : 0,
          stdout: 'command output',
          stderr: '',
        }
      })
      let removals = 0
      const execute = async (command, args, options) => {
        if (args[0] === 'rm' && ++removals === 1)
          return { status: 1, stdout: '', stderr: 'daemon unavailable' }
        return fixture.execute(command, args, options)
      }
      const session = new DockerSession(new AbortController().signal, execute)
      let failure
      try {
        await session.runContainer(['--rm', 'test-image'])
      }
      catch (error) {
        failure = error
      }
      expect(failure).toBeInstanceOf(Error)
      if (outcome === 'succeeds') {
        expect(failure.message).toContain('daemon unavailable')
      }
      else {
        expect(failure).toBeInstanceOf(AggregateError)
        expect(failure.errors).toHaveLength(2)
        if (outcome === 'rejects')
          expect(failure.errors[0]).toBe(original)
        else
          expect(failure.errors[0].message).toContain('failed (7)')
        expect(failure.errors[1].message).toContain('daemon unavailable')
      }
      expect(fixture.containers.size).toBe(1)
      expect(await session.cleanup()).toEqual([])
      expect(fixture.containers.size).toBe(0)
      expect(removals).toBe(2)
      expect(await session.cleanup()).toEqual([])
      expect(removals).toBe(2)
    })
  }
})

describe('runReverseProxyTests', () => {
  for (const signalName of [undefined, 'SIGINT', 'SIGTERM']) {
    const cause = signalName
      ? `interruption (${signalName})`
      : 'a failure'
    it(`cleans up after ${cause} during certificate setup`, async () => {
      const signals = new EventEmitter()
      const failure = new Error('certificate setup failed')
      let enter
      let release
      const entered = new Promise((resolve) => {
        enter = resolve
      })
      const held = new Promise((_, reject) => {
        release = reject
      })
      let directory
      const fixture = dockerFixture(async ({ signal }, args) => {
        const mount = args[args.indexOf('-v') + 1]
        directory = dirname(mount.slice(0, -':/out'.length))
        const abort = () => release(signal.reason)
        signal.addEventListener('abort', abort, { once: true })
        enter()
        try {
          return await held
        }
        finally {
          signal.removeEventListener('abort', abort)
        }
      })
      const verification = runReverseProxyTests(['test-image', 'nginx'], { execute: fixture.execute, signals })
      try {
        await Promise.race([
          entered,
          verification.then(() => { throw new Error('Verification ended before certificate setup') }),
        ])
        expect(signals.listenerCount(signalName ?? 'SIGINT')).toBe(1)
        if (signalName) {
          signals.emit(signalName)
          await expect(verification).rejects.toMatchObject({ name: 'AbortError' })
        }
        else {
          release(failure)
          await expect(verification).rejects.toBe(failure)
        }
        expect(fixture.containers.size).toBe(0)
        expect(signals.listenerCount('SIGINT')).toBe(0)
        expect(signals.listenerCount('SIGTERM')).toBe(0)
      }
      finally {
        release(new Error('fixture finished'))
        await verification.catch(() => {})
        if (directory)
          rmSync(directory, { recursive: true, force: true })
      }
    })
  }
})

describe('ProxyEnvironment cleanup', () => {
  it('reports an unavailable Docker context for every owned resource', async () => {
    await withRuntimeDirectory(async (directory) => {
      const controller = new AbortController()
      controller.abort()
      const execute = async (_, args, options = {}) => {
        expect(options.signal).toBeUndefined()
        return args[0] === 'logs'
          ? { status: 0, stdout: '', stderr: '' }
          : { status: 1, stdout: '', stderr: 'unable to resolve docker endpoint: context "test" not found' }
      }
      const commands = new DockerSession(controller.signal, execute)
      const environment = new ProxyEnvironment(PROXIES[0], 'test-image', directory, {}, EXAMPLES_DIR, commands)
      environment.containers = ['held-container']
      environment.volumes = ['held-volume']
      const failures = await environment.cleanup()
      expect(failures).toHaveLength(3)
      for (const failure of failures)
        expect(failure.message).toContain('context "test" not found')
    })
  })
})

describe('isMissingDockerResource', () => {
  const name = 'leapmux.test'
  for (const [kind, message] of [
    ['container', `No such container: ${name}`],
    ['volume', `get ${name}: no such volume`],
    ['network', `network ${name} not found`],
  ]) {
    it(`accepts a missing ${kind} diagnostic for the expected resource`, () => {
      for (const prefix of ['', 'Error: ', 'Error response from daemon: '])
        expect(isMissingDockerResource(`${prefix}${message}\n`, kind, name)).toBe(true)
    })

    it(`rejects missing ${kind} diagnostics for another resource`, () => {
      expect(isMissingDockerResource(message.replace(name, 'another-resource'), kind, name)).toBe(false)
      expect(isMissingDockerResource(message.replace(name, 'leapmuxXtest'), kind, name)).toBe(false)
    })
  }

  it('preserves context, dependency, connection, and malformed diagnostics as failures', () => {
    for (const message of [
      'unable to resolve docker endpoint: context "test" not found',
      'volume plugin not found',
      'Cannot connect to the Docker daemon',
      'not found',
      '',
      undefined,
    ]) {
      for (const kind of ['container', 'volume', 'network'])
        expect(isMissingDockerResource(message, kind, name)).toBe(false)
    }
    expect(isMissingDockerResource(`No such container: ${name}`, 'image', name)).toBe(false)
    for (const absentName of ['', undefined, null, 0])
      expect(isMissingDockerResource(`No such container: ${name}`, 'container', absentName)).toBe(false)
  })

  it('matches the original case of a resource name', () => {
    for (const [kind, message] of [
      ['container', 'No such container: leapmux.Test'],
      ['volume', 'get leapmux.Test: no such volume'],
      ['network', 'network leapmux.Test not found'],
    ]) {
      expect(isMissingDockerResource(message, kind, 'leapmux.Test')).toBe(true)
      expect(isMissingDockerResource(message, kind, 'leapmux.test')).toBe(false)
    }
  })
})
