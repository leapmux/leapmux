import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { spawnTestProcess, stopTrackedProcesses, trackProcess } from './processRegistry'

let root: string

vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: root }) }))
vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  const spawn = vi.fn()
  return { ...actual, spawn, default: { ...actual, spawn } }
})

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  root = mkdtempSync(join(scratch, 'process-registry-'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  rmSync(root, { recursive: true, force: true })
})

function missingProcess(): never {
  throw Object.assign(new Error('No such process'), { code: 'ESRCH' })
}

function record(pid: string, directory = root) {
  mkdirSync(join(directory, 'processes'), { recursive: true })
  const file = join(directory, 'processes', pid)
  writeFileSync(file, '')
  return file
}

describe('test process registry', () => {
  it('keeps a record only while the child is alive', () => {
    const { emitter, proc } = createProcessStub()
    trackProcess(root, proc)
    const file = join(root, 'processes', '123')
    expect(existsSync(file)).toBe(true)
    emitter.emit('exit', 0, null)
    expect(existsSync(file)).toBe(false)
  })

  it('does not record a process that failed to start or already exited', () => {
    trackProcess(root, createProcessStub({ pid: undefined }).proc)
    trackProcess(root, createProcessStub({ exitCode: 0 }).proc)
    expect(existsSync(join(root, 'processes'))).toBe(false)
  })

  it('stops only processes recorded under this run', async () => {
    const own = record('123')
    const other = record('456', join(root, 'other-run'))
    let alive = true
    const kill = vi.spyOn(process, 'kill').mockImplementation((pid, signal) => {
      expect(pid).toBe(123)
      if (!alive)
        return missingProcess()
      if (signal === 'SIGTERM')
        alive = false
      return true
    })
    await stopTrackedProcesses(root)
    expect(kill).toHaveBeenCalledWith(123, 'SIGTERM')
    expect(existsSync(own)).toBe(false)
    expect(existsSync(other)).toBe(true)
  })

  it('removes records for processes that already exited', async () => {
    const file = record('123')
    vi.spyOn(process, 'kill').mockImplementation(missingProcess)
    await stopTrackedProcesses(root)
    expect(existsSync(file)).toBe(false)
  })

  it('cleans valid records even when another record is invalid', async () => {
    const valid = record('123')
    record('0')
    const kill = vi.spyOn(process, 'kill').mockImplementation(missingProcess)
    await expect(stopTrackedProcesses(root)).rejects.toBeInstanceOf(AggregateError)
    expect(existsSync(valid)).toBe(false)
    expect(kill.mock.calls.every(([pid]) => pid === 123)).toBe(true)
  })

  it('waits for graceful shutdown before escalating', async () => {
    vi.useFakeTimers()
    record('123')
    let alive = true
    const kill = vi.spyOn(process, 'kill').mockImplementation((_pid, signal) => {
      if (!alive)
        return missingProcess()
      if (signal === 'SIGKILL')
        alive = false
      return true
    })
    const stopped = stopTrackedProcesses(root)
    await vi.advanceTimersByTimeAsync(4999)
    expect(kill).not.toHaveBeenCalledWith(123, 'SIGKILL')
    await vi.advanceTimersByTimeAsync(1)
    await stopped
    expect(kill).toHaveBeenCalledWith(123, 'SIGKILL')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reports a process that still exists after escalation', async () => {
    vi.useFakeTimers()
    record('123')
    vi.spyOn(process, 'kill').mockReturnValue(true)
    const result = stopTrackedProcesses(root).then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(await result).toBeInstanceOf(AggregateError)
    expect(existsSync(join(root, 'processes', '123'))).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops a new child if its registry record cannot be written', () => {
    writeFileSync(join(root, 'processes'), 'not a directory')
    const { emitter, proc } = createProcessStub()
    vi.mocked(spawn).mockReturnValue(proc)
    expect(() => spawnTestProcess('fixture-child', [], {})).toThrow()
    expect(emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGKILL')
  })

  it('reports both registration and termination failures', () => {
    writeFileSync(join(root, 'processes'), 'not a directory')
    const { emitter, proc } = createProcessStub()
    const signalError = new Error('Cannot terminate the child')
    emitter.kill.mockImplementation(() => {
      throw signalError
    })
    vi.mocked(spawn).mockReturnValue(proc)
    let result: unknown
    try {
      spawnTestProcess('fixture-child', [], {})
    }
    catch (error) {
      result = error
    }
    expect(result).toBeInstanceOf(AggregateError)
    const errors = (result as AggregateError).errors
    expect(errors).toHaveLength(2)
    expect(errors[0]).toMatchObject({ code: 'EEXIST' })
    expect(errors[1]).toBe(signalError)
  })
})
