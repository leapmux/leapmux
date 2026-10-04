import type { Page } from '@playwright/test'
import type { ChildProcess } from 'node:child_process'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from './nativeStartupWrapper'
import type { NativeWorker } from './nativeWorker'
import { execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeStartupShellEnvironment, withNativeStartupWorker } from './nativeStartupWorker'
import { stopProcess } from './process'
import { createServerOutput } from './serverOutput'

const calls = vi.hoisted(() => ({
  worker: vi.fn(),
  directory: vi.fn(),
  environment: vi.fn(),
}))
vi.mock('./nativeWorker', () => ({ withNativeWorker: calls.worker }))
vi.mock('./runDirectory', () => ({ createTestDirectory: calls.directory }))
vi.mock('./server', () => ({ hubSpawnEnv: calls.environment }))

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let directory: string
const children: ChildProcess[] = []

beforeEach(() => {
  vi.resetAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-startup-shell-unit-'))
  calls.directory.mockImplementation((prefix: string) => mkdtempSync(join(directory, prefix)))
  calls.environment.mockImplementation((environment: NodeJS.ProcessEnv) => ({ ...environment }))
})
afterEach(async () => {
  const results = await Promise.allSettled(children.splice(0).map(child => stopProcess(child)))
  const failures = results.filter(result => result.status === 'rejected')
  if (failures.length)
    throw new AggregateError(failures.map(result => result.reason), 'The startup Worker fixture cleanup failed.')
  rmSync(directory, { recursive: true, force: true })
})

function workerFixture() {
  const home = join(directory, 'isolated-home')
  mkdirSync(home)
  const context: ManagedNativeScenarioContext = {
    provider: AgentProvider.CODEX,
    workspaceId: 'controlled-startup-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: 'suite-worker', agentEnv: { HOME: home, PATH: process.env.PATH ?? '' } },
    get page(): Page { throw new Error('The startup wrapper unit must not access a browser.') },
    get modelScript(): ModelScript { throw new Error('The startup wrapper unit must not access a model.') },
  }
  const peer: NativeWorker<ManagedNativeScenarioContext['leapmuxServer']> = {
    server: { ...context.leapmuxServer, workerId: 'actual-private-worker', agentEnv: { HOME: home, PATH: process.env.PATH ?? '', PRIVATE_PIN: 'actual-worker' } },
    workerId: 'actual-private-worker',
    dataDir: join(directory, 'actual-worker-data'),
    output: createServerOutput(),
  }
  mkdirSync(peer.dataDir)
  calls.worker.mockImplementation(async (_server: unknown, _options: unknown, use: (worker: typeof peer) => Promise<void>) => use(peer))
  const program = join(directory, 'native-fixture.cjs')
  writeFileSync(program, 'process.exitCode=0')
  const launch: NativeStartupLaunch = { binaryName: 'native-fixture', executable: process.execPath, args: [program], holdWhen: ['runtime'] }
  return { context, peer, launch }
}

describe('withNativeStartupWorker', () => {
  it('passes the same actual Worker object as the third callback argument', async () => {
    const { context, peer, launch } = workerFixture()
    let observedWrapper: NativeStartupWrapper | undefined
    await withNativeStartupWorker(context, launch, {}, async (workerId, wrapper, ...remaining: unknown[]) => {
      observedWrapper = wrapper
      expect(workerId).toBe(peer.workerId)
      expect(remaining).toHaveLength(1)
      expect(remaining[0]).toBe(peer)
      expect(existsSync(peer.dataDir)).toBe(true)
    })
    if (!observedWrapper)
      throw new Error('The startup Worker did not enter its controlled callback.')
    await expect(observedWrapper.entry).rejects.toThrow('closed before a native process entered')
  })

  it('keeps existing two-argument callbacks and exact controlled shell setup', async () => {
    const { context, peer, launch } = workerFixture()
    let callbackCount = 0
    await withNativeStartupWorker(context, launch, {}, async (workerId, wrapper) => {
      callbackCount++
      expect(workerId).toBe(peer.workerId)
      expect(existsSync(wrapper.scriptPath)).toBe(true)
    })
    expect(callbackCount).toBe(1)
    expect(calls.worker).toHaveBeenCalledWith(context.leapmuxServer, expect.objectContaining({ dataDirPrefix: 'native-startup-worker', workerName: 'Native startup test', env: expect.objectContaining({ PATH: expect.any(String), ZDOTDIR: expect.any(String) }) }), expect.any(Function))
  })

  it('forwards selected observation options to the actual startup wrapper', async () => {
    const { context, launch } = workerFixture()
    const workingDir = join(directory, 'actual cwd 漢字')
    mkdirSync(workingDir)
    const environment = { TMPDIR: join(directory, 'actual temporary') }
    const options = { failRuntime: false, observeEnvironment: ['TMPDIR'] }
    await withNativeStartupWorker(context, launch, options, async (_workerId, wrapper) => {
      const child = spawn(process.execPath, [wrapper.scriptPath, 'runtime'], { cwd: workingDir, env: { ...process.env, ...environment }, stdio: ['ignore', 'pipe', 'pipe'] })
      children.push(child)
      const exited = once(child, 'exit')
      expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: { workingDir, environment } })
      await wrapper.release()
      expect(await exited).toEqual([0, null])
    })
  })

  it('disposes the wrapper after the callback fails', async () => {
    const { context, launch } = workerFixture()
    const callbackFailure = new Error('The controlled callback failed.')
    let observedWrapper: NativeStartupWrapper | undefined
    await expect(withNativeStartupWorker(context, launch, {}, async (_workerId, wrapper) => {
      observedWrapper = wrapper
      throw callbackFailure
    })).rejects.toBe(callbackFailure)
    if (!observedWrapper)
      throw new Error('The startup Worker did not enter its failing callback.')
    await expect(observedWrapper.entry).rejects.toThrow('closed before a native process entered')
  })

  it('retains both callback and wrapper cleanup failures', async () => {
    const { context, launch } = workerFixture()
    const callbackFailure = new Error('The controlled callback failed.')
    const cleanupFailure = new Error('The controlled wrapper cleanup failed.')
    await expect(withNativeStartupWorker(context, launch, {}, async (_workerId, wrapper) => {
      const dispose = wrapper.dispose.bind(wrapper)
      vi.spyOn(wrapper, 'dispose').mockImplementation(async () => {
        await dispose()
        throw cleanupFailure
      })
      throw callbackFailure
    })).rejects.toMatchObject({ errors: [callbackFailure, cleanupFailure] })
  })
})

describe('nativeStartupShellEnvironment', () => {
  it('keeps the isolated profile and applies the wrapper prefix after it', () => {
    const home = join(directory, 'private-home')
    mkdirSync(home)
    const original = 'export PRIVATE_NATIVE_PROFILE_MARKER=isolated\n'
    writeFileSync(join(home, '.zlogin'), original)
    writeFileSync(join(home, '.zshrc'), original)
    const wrapper = join(directory, 'a space and \'quote')
    const profiles = join(directory, 'private-shell')
    const environment = { HOME: home, PATH: '/original/native/path' }
    expect(nativeStartupShellEnvironment(profiles, wrapper, environment)).toEqual({ PATH: `${wrapper}${delimiter}/original/native/path`, ZDOTDIR: profiles })
    for (const file of ['.zlogin', '.zshrc']) {
      const source = readFileSync(join(profiles, file), 'utf8')
      expect(source.startsWith(original)).toBe(true)
      expect(source).toContain('export PATH=')
      expect(source.indexOf('PRIVATE_NATIVE_PROFILE_MARKER')).toBeLessThan(source.indexOf('export PATH='))
    }
    expect(environment).toEqual({ HOME: home, PATH: '/original/native/path' })
  })

  it.runIf(existsSync('/bin/zsh'))('finds the controlled executable after native login profiles run', () => {
    const wrapper = join(directory, 'wrapper with spaces')
    mkdirSync(wrapper)
    const binary = join(wrapper, 'native-fixture')
    writeFileSync(binary, '#!/bin/sh\nexit 0\n', { mode: 0o755 })
    const home = join(directory, 'private-home')
    mkdirSync(home)
    const environment = { ...process.env, HOME: home, ZDOTDIR: home }
    const controlled = nativeStartupShellEnvironment(join(directory, 'private-shell'), wrapper, environment)
    const result = execFileSync('/bin/zsh', ['-lic', 'command -v native-fixture'], { env: { ...environment, ...controlled }, encoding: 'utf8' })
    expect(result.trim()).toBe(binary)
  })
})
