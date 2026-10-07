import type { SpawnOptions } from 'node:child_process'
import { ChildProcess, execFileSync, spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join, resolve, sep } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { ANCESTOR_INSTRUCTION_FILES, ANCESTOR_INSTRUCTION_SENTINEL } from '../tests/e2e/helpers/ancestorInstructions'
import { copyRunBinary, LEAPMUX_BINARY_NAME, runBinaryPath } from '../tests/e2e/helpers/runBinary'
import { RUN_ROOT_ENV } from '../tests/e2e/helpers/runRoot'
import { runE2E, runRootParent } from './run-e2e'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  const spawn = vi.fn()
  return { ...actual, spawn, default: { ...actual, spawn } }
})
vi.mock('./resolve-task-bin', () => ({ resolveTaskBin: () => 'task' }))
vi.mock('../tests/e2e/helpers/runBinary', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../tests/e2e/helpers/runBinary')>()
  return { ...actual, copyRunBinary: vi.fn(actual.copyRunBinary) }
})

const calls: { command: string, args: string[], env: NodeJS.ProcessEnv }[] = []
const runDirs = new Set<string>()
const releaseHeldCommands = new Set<() => Promise<void>>()
/** The report that the mocked discovery run writes. A test replaces it to select other tests. */
let discoveredReport: () => unknown
/** The report that the mocked merge writes. A test replaces it to report other results. */
let mergedReport: () => unknown
/** A project root of this test's own, so no test reads or writes the real build output. */
let projectRoot: string
/** The file that `task build-backend` writes in `projectRoot`. */
let buildOutput: string

it('keeps actual short runtime roots separate from readable test-result shard paths', async () => {
  const observed: { runtime: string, binary: string, artifact: string }[] = []
  processes([0, 0], (env) => {
    if (!env.LEAPMUX_E2E_NONCE_PATH)
      return
    const runtime = dirname(env.LEAPMUX_E2E_NONCE_PATH)
    const artifact = env.LEAPMUX_E2E_OUTPUT_FILE_DIR ?? ''
    observed.push({ runtime, binary: runBinaryPath(runtime), artifact })
  })
  expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
  const shards = observed.filter(value => value.artifact.includes('shard-'))
  expect(shards).toHaveLength(2)
  for (const [index, shard] of shards.entries()) {
    expect(basename(shard.runtime)).toBe(String(index + 1))
    expect(basename(dirname(shard.runtime))).toMatch(/^leapmux-e2e-[A-Za-z0-9]{6}$/)
    expect(shard.runtime.startsWith(join(projectRoot, '.tmp') + sep)).toBe(true)
    expect(basename(shard.artifact)).toBe(`shard-${index + 1}`)
    expect(basename(dirname(shard.artifact))).toMatch(/^e2e-[A-Za-z0-9]{6}$/)
    expect(dirname(shard.binary)).toBe(shard.runtime)
    expect(existsSync(shard.runtime)).toBe(false)
  }
  expect(new Set(shards.map(shard => shard.runtime)).size).toBe(2)
})

it('writes each sentinel instruction file into the run root before a shard starts', async () => {
  const sentinels: string[][] = []
  processes([0, 0], (env) => {
    if (!env.LEAPMUX_E2E_NONCE_PATH || !env.LEAPMUX_E2E_OUTPUT_FILE_DIR?.includes('shard-'))
      return
    const runRoot = dirname(dirname(env.LEAPMUX_E2E_NONCE_PATH))
    sentinels.push(ANCESTOR_INSTRUCTION_FILES.map(file => file.path).filter(path => readFileSync(join(runRoot, path), 'utf8').includes(ANCESTOR_INSTRUCTION_SENTINEL)))
  })
  expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
  const paths = ANCESTOR_INSTRUCTION_FILES.map(file => file.path)
  expect(sentinels).toEqual([paths, paths])
})

it('states the run root that holds each shard directory, and none to the build or the discovery run', async () => {
  vi.stubEnv(RUN_ROOT_ENV, '/stale/run-root/of/a/caller')
  const stated: { runtime: string, runRoot: string | undefined }[] = []
  processes([0, 0], (env) => {
    if (env.LEAPMUX_E2E_NONCE_PATH && env.LEAPMUX_E2E_OUTPUT_FILE_DIR?.includes('shard-'))
      stated.push({ runtime: dirname(env.LEAPMUX_E2E_NONCE_PATH), runRoot: env[RUN_ROOT_ENV] })
  })
  expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
  expect(stated).toHaveLength(2)
  for (const { runtime, runRoot } of stated)
    expect(runRoot).toBe(dirname(runtime))
  for (const call of calls.filter(call => !call.env.LEAPMUX_E2E_NONCE_PATH))
    expect(call.env[RUN_ROOT_ENV], `${call.command} ${call.args.join(' ')}`).toBeUndefined()
})

it('states the run root of a serial run, which is its own private directory', async () => {
  processes()
  expect(await runE2E(['--workers=1'], projectRoot)).toBe(0)
  const child = calls.find(call => call.command === 'node' && call.args.includes('test') && !call.args.includes('--list'))
  if (!child?.env.LEAPMUX_E2E_NONCE_PATH)
    throw new Error('The serial Playwright fixture has no owned runtime.')
  expect(child.env[RUN_ROOT_ENV]).toBe(dirname(child.env.LEAPMUX_E2E_NONCE_PATH))
})

describe('runRootParent', () => {
  it.each(['darwin', 'linux'] as const)('takes the real path of /tmp on %s, which is short and under no home', (platform) => {
    expect(runRootParent({}, platform)).toBe(realpathSync('/tmp'))
  })

  it('takes the real path of the temporary directory on Windows', () => {
    expect(runRootParent({}, 'win32')).toBe(realpathSync(tmpdir()))
  })

  it('takes the parent that LEAPMUX_E2E_RUN_PARENT states, as an absolute path', () => {
    expect(runRootParent({ LEAPMUX_E2E_RUN_PARENT: 'relative/runs' }, 'darwin')).toBe(resolve('relative/runs'))
  })

  it('ignores an empty LEAPMUX_E2E_RUN_PARENT', () => {
    expect(runRootParent({ LEAPMUX_E2E_RUN_PARENT: '' }, 'linux')).toBe(realpathSync('/tmp'))
  })
})

it('keeps serial runtime roots short and preserves their full tool output run labels', async () => {
  processes()
  expect(await runE2E(['--workers=1'], projectRoot)).toBe(0)
  const child = calls.find(call => call.command === 'node' && call.args.includes('test') && !call.args.includes('--list'))
  if (!child?.env.LEAPMUX_E2E_NONCE_PATH || !child.env.LEAPMUX_E2E_OUTPUT_FILE_DIR)
    throw new Error('The serial Playwright fixture has no owned runtime or full tool output directory.')
  const runtime = dirname(child.env.LEAPMUX_E2E_NONCE_PATH)
  expect(basename(runtime)).toMatch(/^leapmux-e2e-[A-Za-z0-9]{6}$/)
  expect(basename(child.env.LEAPMUX_E2E_OUTPUT_FILE_DIR)).toMatch(/^e2e-[A-Za-z0-9]{6}$/)
  expect(runtime.startsWith(join(projectRoot, '.tmp') + sep)).toBe(true)
  expect(existsSync(runtime)).toBe(false)
})

beforeEach(() => {
  calls.length = 0
  discoveredReport = selectedReport
  mergedReport = selectedReport
  // Mocked command PIDs must never select a real process or process group.
  vi.spyOn(process, 'kill').mockImplementation(() => {
    throw Object.assign(new Error('The mocked process does not exist.'), { code: 'ESRCH' })
  })
  vi.stubEnv('LEAPMUX_DEV', '')
  vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', '')
  vi.stubEnv('LEAPMUX_E2E_NONCE', '')
  const scratch = resolve(import.meta.dirname, '../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  projectRoot = mkdtempSync(join(scratch, 'run-e2e-test-'))
  // The run roots of a test stay in its own project root, not in the shared /tmp of the machine.
  vi.stubEnv('LEAPMUX_E2E_RUN_PARENT', join(projectRoot, '.tmp'))
  buildOutput = join(projectRoot, LEAPMUX_BINARY_NAME)
  writeFileSync(buildOutput, 'first build')
})

afterEach(async () => {
  await Promise.all([...releaseHeldCommands].map(release => release()))
  releaseHeldCommands.clear()
  for (const dir of runDirs)
    rmSync(dir, { recursive: true, force: true })
  runDirs.clear()
  rmSync(projectRoot, { recursive: true, force: true })
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

function selectedReport() {
  return {
    config: { projects: [{ name: 'mock-chromium', testDir: join(projectRoot, 'frontend/tests/e2e') }] },
    suites: ['first.spec.ts', 'second.spec.ts'].map(file => ({
      title: file,
      file,
      specs: [{
        id: file,
        title: 'a selected test',
        file,
        line: 1,
        column: 1,
        tests: [{ projectId: 'mock-chromium', projectName: 'mock-chromium', results: [{ status: 'passed', duration: 125 }] }],
      }],
    })),
    errors: [],
  }
}

function processes(exitCodes = [0, 0], inspect?: (env: NodeJS.ProcessEnv) => void) {
  vi.mocked(spawn).mockImplementation(((command: string, args: string[], options: SpawnOptions) => {
    const env = { ...options.env }
    calls.push({ command, args, env })
    if (args.includes('--list') && env.PLAYWRIGHT_JSON_OUTPUT_FILE) {
      writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE, JSON.stringify(discoveredReport()))
    }
    if (args.includes('--reporter=list,blob,json') && env.PLAYWRIGHT_BLOB_OUTPUT_DIR && env.LEAPMUX_E2E_OUTPUT_FILE_DIR) {
      const shard = args.find(argument => argument.startsWith('--shard='))?.replaceAll('/', '-') ?? basename(env.LEAPMUX_E2E_OUTPUT_FILE_DIR)
      mkdirSync(env.PLAYWRIGHT_BLOB_OUTPUT_DIR, { recursive: true })
      writeFileSync(join(env.PLAYWRIGHT_BLOB_OUTPUT_DIR, `report-${shard}.zip`), 'Playwright blob fixture')
    }
    const noncePath = env.LEAPMUX_E2E_NONCE_PATH
    if (noncePath && command !== 'task') {
      runDirs.add(dirname(noncePath))
      expect(readFileSync(noncePath, 'utf8')).toBe(env.LEAPMUX_E2E_NONCE)
      inspect?.(env)
    }
    if (args.includes('merge-reports') && env.PLAYWRIGHT_JSON_OUTPUT_FILE) {
      mkdirSync(dirname(env.PLAYWRIGHT_JSON_OUTPUT_FILE), { recursive: true })
      writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE, JSON.stringify(mergedReport()))
    }
    const child = new EventEmitter()
    queueMicrotask(() => {
      const code = exitCodes.shift() ?? 0
      child.emit('exit', code, null)
      child.emit('close', code, null)
    })
    return child as ChildProcess
  }) as typeof spawn)
}

function partialLaunchFailure(options: { stopFailure?: Error, signal?: boolean, trackedRecord?: boolean, inheritedPipe?: boolean } = {}) {
  processes()
  const completed = vi.mocked(spawn).getMockImplementation()
  const copy = vi.mocked(copyRunBinary).getMockImplementation()
  if (!completed || !copy)
    throw new Error('The controlled launcher fixture is absent.')
  const state = { stopped: false, directory: '', trackedRecord: '', setupFailure: new Error('The second shard copy failed.') }
  const exited = deferred<void>()
  let child: ChildProcess | undefined
  vi.mocked(copyRunBinary).mockImplementationOnce(copy).mockImplementationOnce(copy).mockImplementationOnce(() => {
    if (options.signal)
      process.emit('SIGTERM')
    throw state.setupFailure
  })
  vi.mocked(spawn).mockImplementation((command, args, spawnOptions) => {
    if (!args?.some(argument => argument.startsWith('--shard=')))
      return completed(command, args as string[], spawnOptions as SpawnOptions)
    const nonce = spawnOptions?.env?.LEAPMUX_E2E_NONCE_PATH
    if (!nonce)
      throw new Error('The controlled shard has no private nonce.')
    state.directory = dirname(nonce)
    runDirs.add(state.directory)
    if (options.trackedRecord) {
      const directory = join(state.directory, 'processes')
      mkdirSync(directory)
      state.trackedRecord = join(directory, '123')
      writeFileSync(state.trackedRecord, '')
    }
    const started = Object.assign(new ChildProcess(), {
      pid: 1234,
      exitCode: null,
      signalCode: null,
      kill: vi.fn(() => {
        expect(existsSync(state.directory)).toBe(true)
        if (options.stopFailure)
          throw options.stopFailure
        queueMicrotask(() => {
          state.stopped = true
          Object.defineProperty(started, 'exitCode', { value: 0, configurable: true })
          started.emit('exit', null, 'SIGTERM')
          exited.resolve()
          if (!options.inheritedPipe)
            started.emit('close', null, 'SIGTERM')
        })
        return true
      }),
    })
    child = started
    return started
  })
  return {
    state,
    exited: exited.promise,
    finish: () => {
      if (child) {
        Object.defineProperty(child, 'exitCode', { value: 0, configurable: true })
        child.emit('exit', 0, null)
        child.emit('close', 0, null)
      }
    },
  }
}

function heldSerialCommand(options: { stopFailure?: Error, inheritedPipe?: boolean }) {
  processes()
  const completed = vi.mocked(spawn).getMockImplementation()
  if (!completed)
    throw new Error('The controlled completed command fixture is absent.')
  const launched = deferred<void>()
  const stopAttempted = deferred<void>()
  const state = { directory: '', trackedRecord: '' }
  let child: ChildProcess | undefined
  vi.mocked(spawn).mockImplementation((command, args, spawnOptions) => {
    if (!args?.includes('test'))
      return completed(command, args as string[], spawnOptions as SpawnOptions)
    const nonce = spawnOptions?.env?.LEAPMUX_E2E_NONCE_PATH
    if (!nonce)
      throw new Error('The controlled held command has no private nonce.')
    state.directory = dirname(nonce)
    runDirs.add(state.directory)
    mkdirSync(join(state.directory, 'processes'))
    state.trackedRecord = join(state.directory, 'processes', '123')
    writeFileSync(state.trackedRecord, '')
    const started = Object.assign(new ChildProcess(), {
      pid: 1234,
      exitCode: null,
      signalCode: null,
      kill: vi.fn(() => {
        stopAttempted.resolve()
        if (options.stopFailure)
          throw options.stopFailure
        Object.defineProperty(started, 'exitCode', { value: 0, configurable: true })
        started.emit('exit', null, 'SIGTERM')
        if (!options.inheritedPipe)
          started.emit('close', null, 'SIGTERM')
        return true
      }),
    })
    child = started
    launched.resolve()
    return started
  })
  const finish = () => {
    if (child) {
      Object.defineProperty(child, 'exitCode', { value: 0, configurable: true })
      child.emit('exit', 0, null)
      child.emit('close', 0, null)
    }
  }
  const completion = runE2E(['--workers=1'], projectRoot).then(code => code, (error: unknown) => error)
  releaseHeldCommands.add(async () => {
    finish()
    await completion
  })
  return { state, launched: launched.promise, stopAttempted: stopAttempted.promise, completion, finish }
}

function failureLeaves(error: unknown): unknown[] {
  return error instanceof AggregateError ? error.errors.flatMap(failureLeaves) : [error]
}

describe('end-to-end launcher', () => {
  it('stops an earlier shard before removing its directory after a later shard setup fails', async () => {
    const fixture = partialLaunchFailure()
    await expect(runE2E(['--workers=2'], projectRoot)).rejects.toBe(fixture.state.setupFailure)
    expect(fixture.state.stopped).toBe(true)
    expect(existsSync(fixture.state.directory)).toBe(false)
  })

  it('cleans independent tracked process records after a command refuses to stop', async () => {
    const stopFailure = new Error('The controlled shard refuses its stop signal.')
    const fixture = partialLaunchFailure({ stopFailure, trackedRecord: true })
    try {
      const result: unknown = await runE2E(['--workers=2'], projectRoot).then(() => null, (error: unknown) => error)
      expect(failureLeaves(result)).toContain(fixture.state.setupFailure)
      expect(failureLeaves(result)).toContain(stopFailure)
      expect(process.kill).toHaveBeenCalledWith(123, 'SIGTERM')
      expect(existsSync(fixture.state.trackedRecord)).toBe(false)
      expect(existsSync(fixture.state.directory)).toBe(true)
    }
    finally {
      fixture.finish()
    }
  })

  it('closes a recorded inherited pipe before waiting for the stopped command close event', async () => {
    const fixture = partialLaunchFailure({ trackedRecord: true, inheritedPipe: true })
    vi.mocked(process.kill).mockImplementation((pid, signal) => {
      if (pid === 123 && signal === 'SIGTERM')
        fixture.finish()
      throw Object.assign(new Error('The mocked process does not exist.'), { code: 'ESRCH' })
    })
    const completion = runE2E(['--workers=2'], projectRoot).then(() => null, (error: unknown) => error)
    releaseHeldCommands.add(async () => {
      fixture.finish()
      await completion
    })
    await fixture.exited
    expect(await completion).toBe(fixture.state.setupFailure)
    expect(process.kill).toHaveBeenCalledWith(123, 'SIGTERM')
    expect(existsSync(fixture.state.directory)).toBe(false)
  }, 30_000)

  it('preserves the setup failure and signal stop failure exactly once', async () => {
    const stopFailure = new Error('The controlled signal stop failed.')
    const fixture = partialLaunchFailure({ stopFailure, signal: true })
    try {
      const result: unknown = await runE2E(['--workers=2'], projectRoot).then(() => null, (error: unknown) => error)
      expect(failureLeaves(result)).toEqual([fixture.state.setupFailure, stopFailure])
      expect(existsSync(fixture.state.directory)).toBe(true)
    }
    finally {
      fixture.finish()
    }
  })

  it('clears inherited internal full tool output state in serial children and keeps public destinations', async () => {
    vi.stubEnv('LEAPMUX_E2E_OUTPUT_FILE_DIR', '/older-run/artifacts')
    vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', '/older-run/nonce')
    vi.stubEnv('LEAPMUX_E2E_NONCE', 'older-nonce')
    vi.stubEnv('E2E_STATE_PATH', '/older-run/state.json')
    vi.stubEnv('PLAYWRIGHT_JSON_OUTPUT_FILE', 'public-report.json')
    processes()
    expect(await runE2E(['--workers=1', '--output=public-output'], projectRoot)).toBe(0)
    const child = calls.find(call => call.args.includes('test'))
    if (!child)
      throw new Error('The controlled serial child is absent.')
    const outputFileDir = child.env.LEAPMUX_E2E_OUTPUT_FILE_DIR
    if (!outputFileDir)
      throw new Error('The serial child lacks its private full tool output directory.')
    expect(dirname(outputFileDir)).toBe(join(projectRoot, 'frontend/public-output/runs'))
    expect(child.env.E2E_STATE_PATH).toBeUndefined()
    expect(child.env.LEAPMUX_E2E_NONCE_PATH).not.toBe('/older-run/nonce')
    expect(child.env.LEAPMUX_E2E_NONCE).not.toBe('older-nonce')
    expect(child.env.PLAYWRIGHT_JSON_OUTPUT_FILE).toBe('public-report.json')
    expect(child.args).toContain(`--output=${join(outputFileDir, 'test-results')}`)
    expect(child.args).not.toContain('--output=public-output')
    expect(child.env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE).toBe(join(projectRoot, 'frontend/public-output/.last-run.json'))
    expect(existsSync(join(outputFileDir, 'console.log'))).toBe(true)
  })

  it.each(['', 'caller-last-run.json'])('preserves Playwright\'s last-failed environment precedence: %j', async (destination) => {
    vi.stubEnv('PLAYWRIGHT_LAST_RUN_OUTPUT_FILE', destination)
    const state = join(projectRoot, 'frontend', destination || join('public-output', '.last-run.json'))
    mkdirSync(dirname(state), { recursive: true })
    writeFileSync(state, JSON.stringify({ status: 'failed', failedTests: ['test-id'] }))
    processes()
    expect(await runE2E(['--workers=1', '--last-failed', '--output=public-output'], projectRoot)).toBe(0)
    const child = calls.find(call => call.args.includes('test'))
    if (!child)
      throw new Error('The controlled serial child is absent.')
    expect(child.env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE)
      .toBe(destination || join(projectRoot, 'frontend/public-output/.last-run.json'))
    expect(child.args).toContain('--last-failed')
  })

  it('returns a failed signal stop without waiting for a held serial command to close', async () => {
    const stopFailure = new Error('The controlled held serial command refuses to stop.')
    const fixture = heldSerialCommand({ stopFailure })
    await fixture.launched
    process.emit('SIGTERM')
    await fixture.stopAttempted
    const result = await fixture.completion
    expect(failureLeaves(result)).toContain(stopFailure)
    expect(existsSync(fixture.state.trackedRecord)).toBe(false)
    expect(existsSync(fixture.state.directory)).toBe(true)
  }, 30_000)

  it('cleans recorded inherited pipes after a successful serial signal stop', async () => {
    const fixture = heldSerialCommand({ inheritedPipe: true })
    vi.mocked(process.kill).mockImplementation((pid, signal) => {
      if (pid === 123 && signal === 'SIGTERM')
        fixture.finish()
      throw Object.assign(new Error('The mocked process does not exist.'), { code: 'ESRCH' })
    })
    await fixture.launched
    process.emit('SIGTERM')
    await fixture.stopAttempted
    expect(await fixture.completion).toBe(143)
    expect(process.kill).toHaveBeenCalledWith(123, 'SIGTERM')
    expect(existsSync(fixture.state.trackedRecord)).toBe(false)
    expect(existsSync(fixture.state.directory)).toBe(false)
  }, 30_000)

  it('removes signal handlers even when child termination fails', async () => {
    const listeners = { interrupt: process.listenerCount('SIGINT'), terminate: process.listenerCount('SIGTERM') }
    const originalInterrupts = process.listeners('SIGINT')
    const originalTerminates = process.listeners('SIGTERM')
    const failure = new Error('The child refuses the stop signal.')
    vi.mocked(process.kill).mockImplementation((pid, signal) => {
      if (pid === -1234 && signal === 'SIGTERM')
        throw failure
      throw Object.assign(new Error('The mocked process does not exist.'), { code: 'ESRCH' })
    })
    const child = Object.assign(new ChildProcess(), {
      pid: 1234,
      exitCode: null,
      signalCode: null,
      kill: vi.fn(() => {
        throw failure
      }),
    })
    vi.mocked(spawn).mockImplementation(() => {
      queueMicrotask(() => {
        process.emit('SIGTERM')
        child.emit('exit', 1, null)
        child.emit('close', 1, null)
      })
      return child
    })
    try {
      await expect(runE2E(['--workers=1'], projectRoot)).rejects.toThrow('Test cleanup failed')
      expect(process.listenerCount('SIGINT')).toBe(listeners.interrupt)
      expect(process.listenerCount('SIGTERM')).toBe(listeners.terminate)
    }
    finally {
      for (const listener of process.listeners('SIGINT')) {
        if (!originalInterrupts.includes(listener))
          process.off('SIGINT', listener)
      }
      for (const listener of process.listeners('SIGTERM')) {
        if (!originalTerminates.includes(listener))
          process.off('SIGTERM', listener)
      }
    }
  })

  it('builds once and starts separate single-worker shards for the public worker count', async () => {
    processes()
    expect(await runE2E(['--workers=2', 'first.spec.ts', 'second.spec.ts'], projectRoot)).toBe(0)
    expect(calls.filter(call => call.command === 'task')).toHaveLength(1)
    const children = calls.filter(call => call.args.includes('test') && !call.args.includes('--list'))
    expect(children).toHaveLength(2)
    expect(children.map(call => call.args.find(argument => argument.startsWith('--shard=')))).toEqual(['--shard=1/2', '--shard=2/2'])
    expect(children.every(call => call.args.includes('--workers=1') && !call.args.includes('--workers=2'))).toBe(true)
    expect(new Set(children.map(call => call.env.LEAPMUX_E2E_NONCE_PATH)).size).toBe(2)
    expect(new Set(children.map(call => call.env.LEAPMUX_E2E_OUTPUT_FILE_DIR)).size).toBe(2)
  })

  function shardChildren() {
    return calls.filter(call => call.args.includes('--reporter=list,blob,json'))
  }

  function lastRunStateFile() {
    return join(projectRoot, 'frontend/test-results/.last-run.json')
  }

  function combinedReportFile() {
    return join(projectRoot, 'frontend/test-results/.last-run-report.json')
  }

  function durationHistoryFile() {
    return join(projectRoot, 'frontend/test-results/.file-durations.json')
  }

  function writeLastRunState(state: unknown, path = lastRunStateFile()) {
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, JSON.stringify(state))
  }

  function writeDurationHistory(files: Record<string, { durationMs: number, cases: number }>) {
    const path = durationHistoryFile()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, JSON.stringify({ version: 1, files }))
  }

  function combinedReport(failedFiles: readonly string[], files: readonly string[] = ['first.spec.ts', 'second.spec.ts']) {
    return {
      errors: [],
      suites: files.map(file => ({
        title: file,
        file,
        specs: [{
          id: file,
          title: 'a selected test',
          file,
          line: 1,
          column: 1,
          tests: [{
            projectId: 'mock-chromium',
            projectName: 'mock-chromium',
            ...(failedFiles.includes(file)
              ? { status: 'unexpected', expectedStatus: 'passed', results: [{ status: 'failed', duration: 5 }] }
              : { status: 'expected', expectedStatus: 'passed', results: [{ status: 'passed', duration: 5 }] }),
          }],
        }],
      })),
    }
  }

  function writeCombinedReport(failedFiles: readonly string[], files?: readonly string[]) {
    const path = combinedReportFile()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, JSON.stringify(combinedReport(failedFiles, files)))
  }

  /** Set the modification time of a file to this many seconds in the past. */
  function ageFile(path: string, seconds: number) {
    const time = (Date.now() - seconds * 1000) / 1000
    utimesSync(path, time, time)
  }

  /** Collect what the launcher writes to its standard streams, so a test reads each message. */
  function captureOutput() {
    const stdout: string[] = []
    const stderr: string[] = []
    const collect = (chunks: string[]) => ((chunk: string | Uint8Array) => {
      chunks.push(String(chunk))
      return true
    }) as typeof process.stdout.write
    vi.spyOn(process.stdout, 'write').mockImplementation(collect(stdout))
    vi.spyOn(process.stderr, 'write').mockImplementation(collect(stderr))
    return { stdout: () => stdout.join(''), stderr: () => stderr.join('') }
  }

  function emptyReport() {
    return { config: selectedReport().config, suites: [], errors: [] }
  }

  it('shares the last-failed state with every parallel shard through private copies', async () => {
    const content = JSON.stringify({ status: 'failed', failedTests: ['test-id'] })
    writeLastRunState(JSON.parse(content))
    processes()
    expect(await runE2E(['--workers=2', '--last-failed'], projectRoot)).toBe(0)
    const discovery = calls.find(call => call.args.includes('--list'))
    if (!discovery)
      throw new Error('The controlled discovery run is absent.')
    const discoveryState = discovery.args.find(argument => argument.startsWith('--last-failed-file='))?.slice('--last-failed-file='.length)
    if (!discoveryState)
      throw new Error('The discovery run did not read a last-failed snapshot.')
    expect(discovery.args).toContain('--last-failed')
    expect(readFileSync(discoveryState, 'utf8')).toBe(content)
    const children = shardChildren()
    expect(children).toHaveLength(2)
    for (const child of children) {
      expect(child.args).toContain('--last-failed')
      const shardState = child.args.find(argument => argument.startsWith('--last-failed-file='))?.slice('--last-failed-file='.length)
      if (!shardState)
        throw new Error('The shard did not read a private last-failed copy.')
      expect(shardState).not.toBe(discoveryState)
      expect(shardState).toContain(join('test-results', 'runs'))
      expect(readFileSync(shardState, 'utf8')).toBe(content)
    }
    // No child wrote the caller's state: only the merge replaces it.
    expect(readFileSync(join(projectRoot, 'frontend/test-results/.last-run.json'), 'utf8')).toBe(content)
  })

  it.each([
    { args: ['--last-failed'], code: 1 },
    { args: ['--last-failed', '--pass-with-no-tests'], code: 0 },
  ])('settles a parallel last-failed run without failed tests before any build starts: %j', async ({ args, code }) => {
    writeLastRunState({ status: 'passed', failedTests: [] })
    const before = readFileSync(lastRunStateFile(), 'utf8')
    processes()
    expect(await runE2E(['--workers=2', ...args], projectRoot)).toBe(code)
    expect(calls).toEqual([])
    expect(readFileSync(lastRunStateFile(), 'utf8')).toBe(before)
  })

  it.each([
    { args: ['--last-failed'], code: 1, stream: 'stderr', message: 'No selected E2E test matches a failed test in', hint: 'Add --pass-with-no-tests to accept an empty selection.' },
    { args: ['--last-failed', '--pass-with-no-tests'], code: 0, stream: 'stdout', message: 'No selected E2E test matches a failed test in', hint: '' },
    { args: ['--grep', 'no match'], code: 1, stream: 'stderr', message: 'No selected E2E test files exist.', hint: 'Add --pass-with-no-tests to accept an empty selection.' },
    { args: ['--grep', 'no match', '--pass-with-no-tests'], code: 0, stream: 'stdout', message: 'No selected E2E test files exist.', hint: '' },
  ] as const)('keeps the saved failed tests when discovery selects no test file: %j', async ({ args, code, stream, message, hint }) => {
    const saved = { status: 'failed', failedTests: ['first-id', 'second-id'] }
    writeLastRunState(saved)
    const before = readFileSync(lastRunStateFile(), 'utf8')
    discoveredReport = emptyReport
    processes()
    const output = captureOutput()
    expect(await runE2E(['--workers=2', ...args], projectRoot)).toBe(code)
    expect(output[stream]()).toContain(message)
    expect(output[stream]()).toContain(hint)
    // Only the discovery run executes: no shard starts, and no merge replaces the state.
    expect(calls.filter(call => call.command === 'node').map(call => call.args.includes('--list'))).toEqual([true])
    expect(readFileSync(lastRunStateFile(), 'utf8')).toBe(before)
  })

  it('refuses a parallel last-failed run without a last-run state before any build starts', async () => {
    processes()
    await expect(runE2E(['--workers=2', '--last-failed'], projectRoot)).rejects.toThrow('that file does not exist')
    expect(calls).toEqual([])
  })

  it('assigns the selected files to balanced shards from the recorded duration history', async () => {
    writeDurationHistory({ 'first.spec.ts': { durationMs: 10, cases: 1 }, 'second.spec.ts': { durationMs: 5000, cases: 1 } })
    processes()
    expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
    const children = shardChildren()
    expect(children).toHaveLength(2)
    expect(children.every(call => !call.args.some(argument => argument.startsWith('--shard=')))).toBe(true)
    const lists = children.map((call) => {
      const list = call.args.find(argument => argument.startsWith('--test-list='))?.slice('--test-list='.length)
      if (!list)
        throw new Error('The balanced shard did not receive its exact test list.')
      return readFileSync(list, 'utf8')
    })
    expect(lists).toEqual(['second.spec.ts\n', 'first.spec.ts\n'])
    // The merged run replaces the estimates of the measured files for the next plan.
    const history: unknown = JSON.parse(readFileSync(join(projectRoot, 'frontend/test-results/.file-durations.json'), 'utf8'))
    expect(history).toEqual({ version: 1, files: { 'first.spec.ts': { durationMs: 125, cases: 1 }, 'second.spec.ts': { durationMs: 125, cases: 1 } } })
  })

  it('keeps Playwright\'s own shard split when the caller turns balancing off', async () => {
    writeDurationHistory({ 'first.spec.ts': { durationMs: 10, cases: 1 }, 'second.spec.ts': { durationMs: 5000, cases: 1 } })
    processes()
    expect(await runE2E(['--workers=2', '--balance=off'], projectRoot)).toBe(0)
    const children = shardChildren()
    expect(children).toHaveLength(2)
    expect(children.map(call => call.args.find(argument => argument.startsWith('--shard=')))).toEqual(['--shard=1/2', '--shard=2/2'])
    expect(children.every(call => !call.args.some(argument => argument.startsWith('--test-list=')))).toBe(true)
  })

  it('reruns the complete failed files of the last combined report', async () => {
    writeCombinedReport(['second.spec.ts'])
    processes()
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
    const discovery = calls.find(call => call.args.includes('--list'))
    if (!discovery)
      throw new Error('The controlled discovery run is absent.')
    const list = discovery.args.find(argument => argument.startsWith('--test-list='))?.slice('--test-list='.length)
    if (!list)
      throw new Error('The discovery run did not receive the failed-file list.')
    expect(readFileSync(list, 'utf8')).toBe('second.spec.ts\n')
    const children = shardChildren()
    expect(children).toHaveLength(2)
    // The selection is the whole file. A last-failed selection would run the failed cases only.
    for (const child of [discovery, ...children]) {
      expect(child.args).not.toContain('--last-failed')
      expect(child.args.some(argument => argument.startsWith('--last-failed-file='))).toBe(false)
    }
    for (const child of children) {
      expect(child.args).toContain(`--test-list=${list}`)
      expect(child.args.some(argument => argument.startsWith('--shard='))).toBe(true)
    }
  })

  it('accepts a combined report without failed files before any build starts', async () => {
    writeCombinedReport([])
    processes()
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
    expect(calls).toEqual([])
  })

  it('refuses a failed-file rerun without a combined report before any build starts', async () => {
    processes()
    await expect(runE2E(['--workers=2', '--failed-files'], projectRoot)).rejects.toThrow('that file does not exist')
    expect(calls).toEqual([])
  })

  it('refuses the last combined report after a later run replaced the last-run state', async () => {
    writeCombinedReport(['second.spec.ts'])
    writeLastRunState({ status: 'passed', failedTests: [] })
    ageFile(combinedReportFile(), 60)
    processes()
    await expect(runE2E(['--workers=2', '--failed-files'], projectRoot)).rejects.toThrow('older than the last-run state')
    expect(calls).toEqual([])
  })

  it('compares the report with the last-run state that --last-failed-file selects', async () => {
    writeCombinedReport(['second.spec.ts'])
    const custom = join(projectRoot, 'frontend', 'custom-state.json')
    writeLastRunState({ status: 'passed', failedTests: [] }, custom)
    ageFile(combinedReportFile(), 60)
    processes()
    await expect(runE2E(['--workers=2', '--failed-files', '--last-failed-file=custom-state.json'], projectRoot)).rejects.toThrow(`older than the last-run state at ${custom}`)
    // The default state file is absent, so it cannot show a later run.
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
  })

  it('reruns the failed files when the combined report is not older than the last-run state', async () => {
    writeCombinedReport(['second.spec.ts'])
    writeLastRunState({ status: 'failed', failedTests: ['second-id'] })
    ageFile(lastRunStateFile(), 60)
    processes()
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
    expect(calls.some(call => call.args.includes('--list'))).toBe(true)
  })

  it('reads an explicit report although a later run replaced the last-run state', async () => {
    writeCombinedReport(['second.spec.ts'])
    writeLastRunState({ status: 'passed', failedTests: [] })
    ageFile(combinedReportFile(), 60)
    processes()
    expect(await runE2E(['--workers=2', '--failed-files-from=test-results/.last-run-report.json'], projectRoot)).toBe(0)
    expect(calls.some(call => call.args.includes('--list'))).toBe(true)
  })

  it('writes the combined report after the merge replaced the state, so the next --failed-files run accepts it', async () => {
    mergedReport = () => {
      // Playwright's merge reporter replaces the last-run state before the launcher saves the report.
      writeLastRunState({ status: 'failed', failedTests: ['second-id'] })
      return combinedReport(['second.spec.ts'])
    }
    processes()
    expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
    expect(JSON.parse(readFileSync(combinedReportFile(), 'utf8'))).toEqual(combinedReport(['second.spec.ts']))
    calls.length = 0
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
    const list = calls.find(call => call.args.includes('--list'))?.args.find(argument => argument.startsWith('--test-list='))?.slice('--test-list='.length)
    if (!list)
      throw new Error('The second run did not receive the failed-file list.')
    expect(readFileSync(list, 'utf8')).toBe('second.spec.ts\n')
  })

  it('keeps the preceding combined report when the merged report misses a selected case', async () => {
    writeCombinedReport(['first.spec.ts'])
    const preceding = readFileSync(combinedReportFile(), 'utf8')
    mergedReport = () => ({ ...selectedReport(), suites: selectedReport().suites.slice(0, 1) })
    processes()
    await expect(runE2E(['--workers=2'], projectRoot)).rejects.toThrow('does not contain every selected test case exactly once')
    expect(readFileSync(combinedReportFile(), 'utf8')).toBe(preceding)
  })

  it('warns about a failed file that selects no test now and reruns the files that remain', async () => {
    writeCombinedReport(['first.spec.ts', 'third.spec.ts'], ['first.spec.ts', 'second.spec.ts', 'third.spec.ts'])
    processes()
    const output = captureOutput()
    expect(await runE2E(['--workers=2', '--failed-files'], projectRoot)).toBe(0)
    expect(output.stderr()).toContain(`Warning: these failed files from ${combinedReportFile()} select no test now: third.spec.ts`)
    expect(output.stdout()).toContain('E2E --failed-files: 2 files from')
    const list = calls.find(call => call.args.includes('--list'))?.args.find(argument => argument.startsWith('--test-list='))?.slice('--test-list='.length)
    if (!list)
      throw new Error('The discovery run did not receive the failed-file list.')
    expect(readFileSync(list, 'utf8')).toBe('first.spec.ts\nthird.spec.ts\n')
  })

  it('runs the failed files of the last combined report in one serial process', async () => {
    writeCombinedReport(['second.spec.ts'])
    processes()
    const output = captureOutput()
    expect(await runE2E(['--workers=1', '--failed-files'], projectRoot)).toBe(0)
    expect(output.stdout()).toContain(`E2E --failed-files: 1 file from ${combinedReportFile()}`)
    // A serial run starts no discovery run and no shard: one Playwright process runs the complete files.
    const children = calls.filter(call => call.command === 'node')
    expect(children).toHaveLength(1)
    const [child] = children
    const list = child!.args.find(argument => argument.startsWith('--test-list='))?.slice('--test-list='.length)
    if (!list)
      throw new Error('The serial run did not receive the failed-file list.')
    expect(readFileSync(list, 'utf8')).toBe('second.spec.ts\n')
    expect(child!.args).not.toContain('--list')
    expect(child!.args).not.toContain('--last-failed')
  })

  it('saves the duration of the files that another run recorded while this run went on', async () => {
    writeDurationHistory({ 'first.spec.ts': { durationMs: 10, cases: 1 } })
    mergedReport = () => {
      // A second launcher run that shares this output root ends first and saves its own measurement.
      writeDurationHistory({ 'first.spec.ts': { durationMs: 10, cases: 1 }, 'other.spec.ts': { durationMs: 700, cases: 2 } })
      return selectedReport()
    }
    processes()
    expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
    const history: unknown = JSON.parse(readFileSync(durationHistoryFile(), 'utf8'))
    expect(history).toEqual({ version: 1, files: { 'first.spec.ts': { durationMs: 125, cases: 1 }, 'other.spec.ts': { durationMs: 700, cases: 2 }, 'second.spec.ts': { durationMs: 125, cases: 1 } } })
  })

  it('warns and keeps the result of the run when the duration history cannot be saved', async () => {
    // A directory at the history path makes the atomic replacement fail on every platform.
    mkdirSync(durationHistoryFile(), { recursive: true })
    processes()
    const output = captureOutput()
    expect(await runE2E(['--workers=2'], projectRoot)).toBe(0)
    expect(output.stderr()).toContain(`Warning: the E2E duration history at ${durationHistoryFile()} was not saved.`)
    expect(existsSync(combinedReportFile())).toBe(true)
  })

  it('retains separate reports for two runs with the same explicit output directory', async () => {
    processes()
    const args = ['--workers=2', '--output=retained-output']
    expect(await runE2E(args, projectRoot)).toBe(0)
    const first = calls.find(call => call.args.includes('merge-reports'))!.env.PLAYWRIGHT_JSON_OUTPUT_FILE!
    calls.length = 0
    expect(await runE2E(args, projectRoot)).toBe(0)
    const second = calls.find(call => call.args.includes('merge-reports'))!.env.PLAYWRIGHT_JSON_OUTPUT_FILE!
    expect(first).not.toBe(second)
    expect(readFileSync(first, 'utf8')).toBe(readFileSync(second, 'utf8'))
  })

  it('rejects fully parallel shared fixtures before any build starts', async () => {
    processes()
    await expect(runE2E(['--fully-parallel'], projectRoot)).rejects.toThrow('serial tests inside each isolated shard')
    expect(calls).toEqual([])
  })

  it('rejects an invalid public worker count before any build starts', async () => {
    processes()
    await expect(runE2E(['--workers=0'], projectRoot)).rejects.toThrow('worker count')
    expect(calls).toEqual([])
  })

  it('uses the cached backend build and passes development mode to both processes', async () => {
    processes()
    expect(await runE2E(['--workers=1', '--grep', 'a pattern with spaces'], projectRoot)).toBe(0)
    expect(calls[0]?.args).toEqual(['build-backend'])
    expect(calls).toHaveLength(2)
    expect(calls.every(call => call.env.LEAPMUX_DEV === '1')).toBe(true)
    expect(calls[1]?.args.slice(-2)).toEqual(['--grep', 'a pattern with spaces'])
    expect(process.env.LEAPMUX_DEV).toBe('')
  })

  it.each([0, 3])('removes its nonce directory after Playwright exits with %i', async (exitCode) => {
    processes([0, exitCode])
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(exitCode)
    expect(runDirs.size).toBe(1)
    for (const dir of runDirs)
      expect(existsSync(dir)).toBe(false)
  })

  it('does not start Playwright after the build fails', async () => {
    processes([7])
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(7)
    expect(calls).toHaveLength(1)
    expect(runDirs.size).toBe(0)
  })

  it('runs Playwright with a private copy of the binary that the build wrote', async () => {
    rmSync(buildOutput)
    processes([0, 0], (env) => {
      expect(readFileSync(runBinaryPath(dirname(env.LEAPMUX_E2E_NONCE_PATH!)), 'utf8')).toBe('fresh build')
    })
    // Take the copy after the build, not before it: the build writes the binary.
    const harness = vi.mocked(spawn).getMockImplementation()!
    vi.mocked(spawn).mockImplementationOnce((command, args, options) => {
      writeFileSync(buildOutput, 'fresh build')
      return harness(command, args as string[], options as SpawnOptions)
    })
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(0)
    expect(calls).toHaveLength(2)
  })

  it('keeps the run on its own binary when another pipeline rebuilds the root binary', async () => {
    let copy = ''
    processes([0, 0], (env) => {
      copy = runBinaryPath(dirname(env.LEAPMUX_E2E_NONCE_PATH!))
      // Another task pipeline rebuilds the binary while the run goes on.
      writeFileSync(buildOutput, 'second build')
      expect(readFileSync(copy, 'utf8')).toBe('first build')
    })
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(0)
    expect(copy).not.toBe('')
    // The copy belongs to the run directory, so the cleanup removes it with the run.
    expect(existsSync(copy)).toBe(false)
  })

  it('does not start Playwright when the build left no binary', async () => {
    rmSync(buildOutput)
    processes()
    await expect(runE2E(['--workers=1'], projectRoot)).rejects.toThrow(expect.objectContaining({ code: 'ENOENT' }))
    expect(calls).toHaveLength(1)
    // The launcher created a run directory before the copy failed, and removed it.
    expect(readdirSync(join(projectRoot, '.tmp'))).toEqual([])
  })

  it('stops recorded children when Playwright exits without global teardown', async () => {
    let record = ''
    processes([0, 9], (env) => {
      const directory = join(dirname(env.LEAPMUX_E2E_NONCE_PATH!), 'processes')
      mkdirSync(directory)
      record = join(directory, '123')
      writeFileSync(record, '')
    })
    let alive = true
    const kill = vi.spyOn(process, 'kill').mockImplementation((pid, signal) => {
      expect(pid).toBe(123)
      expect(existsSync(record)).toBe(true)
      if (!alive)
        throw Object.assign(new Error('No process'), { code: 'ESRCH' })
      if (signal === 'SIGTERM')
        alive = false
      return true
    })
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(9)
    expect(kill).toHaveBeenCalledWith(123, 'SIGTERM')
    expect(existsSync(record)).toBe(false)
  })

  it('retains process records when child termination fails', async () => {
    let record = ''
    processes([0, 9], (env) => {
      const directory = join(dirname(env.LEAPMUX_E2E_NONCE_PATH!), 'processes')
      mkdirSync(directory)
      record = join(directory, '123')
      writeFileSync(record, '')
    })
    vi.spyOn(process, 'kill').mockImplementation(() => {
      throw Object.assign(new Error('Permission denied'), { code: 'EPERM' })
    })
    await expect(runE2E(['--workers=1'], projectRoot)).rejects.toThrow('Test cleanup failed')
    expect(existsSync(record)).toBe(true)
  })

  it('keeps private working directories separate from the project repository', async () => {
    vi.stubEnv('GIT_DIR', 'inherited-repository')
    processes([0, 0], (env) => {
      expect(env.GIT_DIR).toBeUndefined()
      const directory = join(dirname(env.LEAPMUX_E2E_NONCE_PATH!), 'working-directory')
      mkdirSync(directory)
      // Git exits with status 128 when it finds no repository. A failure for another reason, such as an absent git executable, does not give that status.
      expect(() => execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: directory, env, stdio: 'ignore' })).toThrow(expect.objectContaining({ status: 128 }))
      execFileSync('git', ['-c', 'init.templateDir=', 'init', '--quiet'], { cwd: directory, env, stdio: 'ignore' })
      for (const [key, value] of [['core.fsmonitor', 'false'], ['gc.auto', '0'], ['maintenance.auto', 'false']])
        execFileSync('git', ['config', key!, value!], { cwd: directory, env, stdio: 'ignore' })
      const output = execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: directory, env, encoding: 'utf8' })
      expect(resolve(output.trim())).toBe(directory)
    })
    expect(await runE2E(['--workers=1'], projectRoot)).toBe(0)
  })

  it('removes the run directory when Playwright fails to start', async () => {
    processes()
    const build = vi.mocked(spawn).getMockImplementation()!
    const error = new Error('Cannot start Playwright')
    vi.mocked(spawn).mockImplementationOnce(build).mockImplementationOnce((_command, _args, options) => {
      const noncePath = options!.env!.LEAPMUX_E2E_NONCE_PATH!
      runDirs.add(dirname(noncePath))
      const child = new EventEmitter()
      queueMicrotask(() => child.emit('error', error))
      return child as ChildProcess
    })
    await expect(runE2E(['--workers=1'], projectRoot)).rejects.toBe(error)
    expect(runDirs.size).toBe(1)
    for (const dir of runDirs)
      expect(existsSync(dir)).toBe(false)
  })
})
