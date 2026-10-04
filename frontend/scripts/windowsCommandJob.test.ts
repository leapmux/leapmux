import type { SpawnOptions } from 'node:child_process'
import { execFile, spawn } from 'node:child_process'
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { isObject } from '../src/lib/jsonPick'
import { parseWindowsJobState, spawnWindowsCommandJob, windowsCommandEnvironment, windowsJobArguments } from './windowsCommandJob'

const control = vi.hoisted(() => ({ errors: [] as Error[], effect: (_args: string[]) => {}, directories: new Set<string>() }))

vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>()
  return { ...actual, rmSync: vi.fn(actual.rmSync) }
})

vi.mock('node:process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:process')>()
  return { ...actual, default: { ...actual, cwd: () => 'C:\\private', env: { ...actual.env, SystemRoot: 'C:\\Windows' } } }
})

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return {
    ...actual,
    spawn: vi.fn(),
    execFile: vi.fn((...args: unknown[]) => {
      const vector = args[1]
      const callback = args.at(-1)
      if (!Array.isArray(vector) || !vector.every(value => typeof value === 'string') || typeof callback !== 'function')
        throw new Error('The controlled Windows command has an invalid shape.')
      const error = control.errors.shift()
      if (!error)
        control.effect(vector)
      queueMicrotask(() => callback(error ?? null, '', ''))
      return new actual.ChildProcess()
    }),
  }
})

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  control.errors.length = 0
  control.effect = () => {}
})

afterEach(async () => {
  const actual = await vi.importActual<typeof import('node:fs')>('node:fs')
  for (const directory of control.directories)
    actual.rmSync(directory, { recursive: true, force: true })
  control.directories.clear()
  vi.mocked(rmSync).mockReset().mockImplementation(actual.rmSync)
  vi.clearAllTimers()
  vi.useRealTimers()
})

function startedJob(options: SpawnOptions = { cwd: 'C:\\private', stdio: 'inherit' }) {
  const stub = createProcessStub()
  vi.mocked(spawn).mockReturnValue(stub.proc)
  const resource = spawnWindowsCommandJob('task', ['build-backend'], options, 50)
  const args = vi.mocked(spawn).mock.calls.at(-1)?.[1]
  if (!args)
    throw new Error('The controlled job has no launcher arguments.')
  const payloadPath = args[args.indexOf('-PayloadPath') + 1]
  if (!payloadPath)
    throw new Error('The controlled job has no payload path.')
  control.directories.add(dirname(payloadPath))
  const payload: unknown = JSON.parse(readFileSync(payloadPath, 'utf8'))
  if (!isObject(payload) || typeof payload.statePath !== 'string')
    throw new Error('The controlled job payload has no state path.')
  return { ...stub, resource, payloadPath, payload, statePath: payload.statePath, directory: dirname(payloadPath) }
}

function writeState(job: ReturnType<typeof startedJob>) {
  writeFileSync(job.statePath, JSON.stringify({ version: 1, ownerPid: 123, rootPid: 456, complete: true, members: [{ pid: 456, creationTime: '134000000000000001' }] }))
}

function mode(args: string[]): string | undefined {
  return args[args.indexOf('-Mode') + 1]
}

describe('windowsCommandEnvironment', () => {
  it('keeps empty values and the first case-insensitive key without mutating the source', () => {
    const source = { Path: 'second', PATH: 'first', EMPTY: '', OMIT: undefined }
    expect(windowsCommandEnvironment(source)).toEqual({ EMPTY: '', PATH: 'first' })
    expect(source).toEqual({ Path: 'second', PATH: 'first', EMPTY: '', OMIT: undefined })
  })

  it('omits a first undefined key without selecting a later duplicate', () => {
    expect(windowsCommandEnvironment({ PATH: undefined, Path: 'later' })).toEqual({})
  })

  it('keeps an environment key that matches an object prototype property', () => {
    const source = Object.create(null)
    Object.defineProperty(source, '__proto__', { value: 'literal', enumerable: true })
    expect(Object.entries(windowsCommandEnvironment(source))).toEqual([['__proto__', 'literal']])
  })

  it.each([{ '': 'value' }, { 'A\0B': 'value' }, { A: 'a\0b' }])('rejects a malformed environment entry: %j', (source) => {
    expect(() => windowsCommandEnvironment(source)).toThrow('Windows command')
  })
})

describe('parseWindowsJobState', () => {
  const state = { version: 1, ownerPid: 123, rootPid: 456, complete: true, members: [{ pid: 456, creationTime: '134000000000000001' }] }

  it('preserves the full creation identity without numeric coercion', () => {
    expect(parseWindowsJobState(state, 123)).toEqual(state)
  })

  it.each([
    null,
    {},
    { ...state, ownerPid: 789 },
    { ...state, rootPid: 0 },
    { ...state, complete: 1 },
    { ...state, members: [] },
    { ...state, members: [{ pid: 456, creationTime: 134 }] },
    { ...state, members: [{ pid: 456, creationTime: '0' }] },
    { ...state, members: [{ pid: 456, creationTime: '9223372036854775808' }] },
    { ...state, members: [{ pid: 1, creationTime: '134000000000000001' }] },
    { ...state, members: [{ pid: 123, creationTime: '134000000000000001' }] },
    { ...state, members: [...state.members, ...state.members] },
  ])('rejects an incomplete or foreign job state: %j', (value) => {
    expect(() => parseWindowsJobState(value, 123)).toThrow('Windows job state')
  })
})

describe('spawnWindowsCommandJob', () => {
  it('preserves stdin and environment while a private launcher owns the exact child handle', async () => {
    const environment = { PRIVATE: 'marker', EMPTY: '' }
    const job = startedJob({ cwd: 'C:\\private', stdio: 'inherit', env: environment })
    try {
      expect(job.resource.child).toBe(job.proc)
      expect(job.payload).toMatchObject({ version: 1, command: 'task', args: ['build-backend'], argv0: 'task', verbatimArguments: false, cwd: 'C:\\private', environment })
      expect(job.payload.stopEventName).toMatch(/^Local\\LeapMuxE2EJob-/u)
      expect(vi.mocked(spawn).mock.calls[0]?.[2]).toMatchObject({ stdio: 'inherit', env: environment, shell: false, detached: false })
    }
    finally {
      job.emitter.exitCode = 0
      await job.resource.stop()
    }
    expect(existsSync(job.directory)).toBe(false)
  })

  it('signals its private event and verifies only its captured job state', async () => {
    const job = startedJob()
    writeState(job)
    control.effect = (args) => {
      if (mode(args) === 'Stop') {
        job.emitter.exitCode = 0
        job.emitter.emit('exit', 0, null)
      }
    }
    await job.resource.stop()
    expect(execFile).toHaveBeenCalledTimes(2)
    expect(vi.mocked(execFile).mock.calls.map(call => mode(Array.isArray(call[1]) ? call[1] : []))).toEqual(['Stop', 'Verify'])
    expect(job.emitter.kill).not.toHaveBeenCalled()
    expect(existsSync(job.directory)).toBe(false)
  })

  it('uses one shutdown promise for concurrent callers', async () => {
    const job = startedJob()
    control.effect = () => {
      job.emitter.exitCode = 0
      job.emitter.emit('exit', 0, null)
    }
    const first = job.resource.stop()
    const second = job.resource.stop()
    expect(second).toBe(first)
    await first
    expect(execFile).toHaveBeenCalledTimes(1)
  })

  it('verifies descendants of a root that completed before cleanup', async () => {
    const job = startedJob()
    writeState(job)
    job.emitter.exitCode = 0
    await job.resource.stop()
    expect(execFile).toHaveBeenCalledExactlyOnceWith(expect.any(String), windowsJobArguments('Verify', job.payloadPath), expect.any(Object), expect.any(Function))
    expect(job.emitter.kill).not.toHaveBeenCalled()
  })

  it('stops an owner that does not finish after its event receives a signal', async () => {
    const job = startedJob()
    job.emitter.kill.mockImplementation(() => {
      queueMicrotask(() => {
        job.emitter.signalCode = 'SIGTERM'
        job.emitter.emit('exit', null, 'SIGTERM')
      })
      return true
    })
    const stopped = job.resource.stop()
    await vi.advanceTimersByTimeAsync(49)
    expect(job.emitter.kill).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    await stopped
    expect(job.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('preserves a control failure and closes the exact owner handle before cleanup', async () => {
    const job = startedJob()
    const error = new Error('The private event transport failed.')
    control.errors.push(error)
    job.emitter.kill.mockImplementation(() => {
      queueMicrotask(() => {
        job.emitter.signalCode = 'SIGTERM'
        job.emitter.emit('exit', null, 'SIGTERM')
      })
      return true
    })
    await expect(job.resource.stop()).rejects.toBe(error)
    expect(job.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(existsSync(job.directory)).toBe(false)
  })

  it('preserves control and wrapped-handle failures when forced cleanup also fails', async () => {
    const job = startedJob()
    const controlError = new Error('The private event transport failed.')
    const stopError = new Error('The wrapped owner handle did not stop.')
    control.errors.push(controlError)
    job.emitter.kill.mockImplementation(() => {
      throw stopError
    })
    const result = job.resource.stop().then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(50)
    const failure = await result
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The Windows cleanup did not preserve both failures.')
    const original = failure.errors[0]
    expect(original).toBeInstanceOf(AggregateError)
    expect(original).toMatchObject({ errors: [controlError, stopError] })
    expect(failure.errors[1]).toBe(stopError)
    expect(existsSync(job.directory)).toBe(true)
  })

  it('stops a startup owner when its private event does not yet exist', async () => {
    const job = startedJob()
    control.errors.push(Object.assign(new Error('The event does not exist.'), { code: 2 }))
    job.emitter.kill.mockImplementation(() => {
      queueMicrotask(() => {
        job.emitter.signalCode = 'SIGTERM'
        job.emitter.emit('exit', null, 'SIGTERM')
      })
      return true
    })
    await job.resource.stop()
    expect(job.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(existsSync(job.directory)).toBe(false)
  })

  it('preserves a failed native creation-identity verification', async () => {
    const job = startedJob()
    writeState(job)
    job.emitter.exitCode = 0
    const error = new Error('A captured process did not exit.')
    control.errors.push(error)
    await expect(job.resource.stop()).rejects.toBe(error)
    expect(existsSync(job.directory)).toBe(true)
  })

  it('rejects foreign state before the verification helper can receive it', async () => {
    const job = startedJob()
    job.emitter.exitCode = 0
    writeFileSync(job.statePath, JSON.stringify({ version: 1, ownerPid: 900, rootPid: 456, complete: true, members: [{ pid: 456, creationTime: '134000000000000001' }] }))
    await expect(job.resource.stop()).rejects.toThrow('owning launcher')
    expect(execFile).not.toHaveBeenCalled()
    expect(existsSync(job.directory)).toBe(true)
  })

  it('preserves a foreign-state failure and a failed owner cleanup', async () => {
    const job = startedJob()
    const stopError = new Error('The wrapped owner handle did not stop.')
    job.emitter.kill.mockImplementation(() => {
      throw stopError
    })
    writeFileSync(job.statePath, JSON.stringify({ version: 1, ownerPid: 900, rootPid: 456, complete: true, members: [{ pid: 456, creationTime: '134000000000000001' }] }))
    const result = job.resource.stop().then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(50)
    const failure = await result
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The Windows cleanup did not preserve the ownership and shutdown failures.')
    expect(failure.errors[0]).toMatchObject({ errors: [stopError, { message: expect.stringContaining('owning launcher') }] })
    expect(failure.errors[1]).toBe(stopError)
    expect(vi.mocked(execFile).mock.calls.map(call => mode(Array.isArray(call[1]) ? call[1] : []))).toEqual(['Stop'])
    expect(existsSync(job.directory)).toBe(true)
  })

  it('rejects shell execution before a private process starts', () => {
    expect(() => spawnWindowsCommandJob('task', [], { shell: true }, 50)).toThrow('direct executable')
    expect(spawn).not.toHaveBeenCalled()
  })

  it('retains the payload and state when a held owner cannot stop', async () => {
    const job = startedJob()
    writeState(job)
    const stopError = new Error('The held owner refused termination.')
    job.emitter.kill.mockImplementation(() => {
      throw stopError
    })
    const result = job.resource.stop().then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(50)
    expect(await result).toBeInstanceOf(AggregateError)
    expect(job.emitter.exitCode).toBeNull()
    expect(existsSync(job.payloadPath)).toBe(true)
    expect(existsSync(job.statePath)).toBe(true)
  })

  it('preserves a spawn failure and a private-directory cleanup failure', () => {
    const spawnError = new Error('The private owner could not start.')
    const cleanupError = new Error('The private directory could not be removed.')
    vi.mocked(spawn).mockImplementationOnce(() => {
      throw spawnError
    })
    vi.mocked(rmSync).mockImplementationOnce((directory) => {
      if (typeof directory === 'string')
        control.directories.add(directory)
      throw cleanupError
    })
    let failure: unknown
    try {
      spawnWindowsCommandJob('task', [], { cwd: 'C:\\private' }, 50)
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    expect(failure).toMatchObject({ errors: [spawnError, cleanupError] })
  })

  it('keeps its control executable, environment, and cwd after the caller changes their sources', async () => {
    const environment = { PRIVATE: 'original', PATH: 'C:\\original-bin' }
    const options = { cwd: 'C:\\original-project', env: environment, stdio: 'inherit' as const }
    const job = startedJob(options)
    writeState(job)
    const originalExecutable = vi.mocked(spawn).mock.calls[0]?.[0]
    const systemRoot = process.env.SystemRoot
    options.cwd = 'C:\\changed-project'
    environment.PRIVATE = 'changed'
    environment.PATH = 'C:\\changed-bin'
    process.env.SystemRoot = 'C:\\changed-system'
    control.effect = (args) => {
      if (mode(args) === 'Stop') {
        job.emitter.exitCode = 0
        job.emitter.emit('exit', 0, null)
      }
    }
    try {
      await job.resource.stop()
      for (const call of vi.mocked(execFile).mock.calls) {
        expect(call[0]).toBe(originalExecutable)
        expect(call[2]).toMatchObject({ cwd: 'C:\\original-project', env: { PRIVATE: 'original', PATH: 'C:\\original-bin' } })
      }
      expect(execFile).toHaveBeenCalledTimes(2)
    }
    finally {
      if (systemRoot === undefined)
        delete process.env.SystemRoot
      else
        process.env.SystemRoot = systemRoot
    }
  })
})
