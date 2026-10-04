import { ChildProcess, spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { PassThrough } from 'node:stream'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { runCommand } from './e2eCommand'
import { spawnCommandProcess } from './e2eCommandProcess'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return { ...actual, spawn: vi.fn() }
})
vi.mock('./e2eCommandProcess', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./e2eCommandProcess')>()
  return { ...actual, spawnCommandProcess: vi.fn(actual.spawnCommandProcess) }
})

let projectRoot: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  projectRoot = mkdtempSync(join(scratch, 'e2e-command-test-'))
})

afterEach(() => {
  vi.restoreAllMocks()
  rmSync(projectRoot, { recursive: true, force: true })
})

describe('runCommand', () => {
  it('preserves captured tree ownership when the caller changes its options before close', async () => {
    const child = new ChildProcess()
    const stop = vi.fn(async () => {})
    vi.mocked(spawnCommandProcess).mockReturnValueOnce({ child, stop })
    const ownership = { ownTree: true }
    const result = runCommand('controlled', [], {}, { ownership })
    ownership.ownTree = false
    child.emit('exit', 0, null)
    child.emit('close', 0, null)
    expect(await result).toBe(0)
    expect(stop).toHaveBeenCalledOnce()
  })

  it('reports a child output stream failure instead of a successful exit', async () => {
    const stdout = new PassThrough()
    const failure = new Error('The child output stream failed.')
    stdout.on('error', () => {})
    const child = Object.assign(new ChildProcess(), {
      stdout,
      kill: vi.fn(() => true),
    })
    vi.mocked(spawn).mockReturnValue(child)
    const result = runCommand('controlled', [], {}, { logPath: join(projectRoot, 'console.log') })
    stdout.emit('error', failure)
    child.emit('exit', 0, null)
    child.emit('close', 0, null)
    await expect(result).rejects.toBe(failure)
  })

  it('reports a final output write failure without throwing from the child close handler', async () => {
    const stdout = new PassThrough()
    const child = Object.assign(new ChildProcess(), { stdout })
    vi.mocked(spawn).mockReturnValue(child)
    const failure = new Error('The parent output stream failed.')
    const result = runCommand('controlled', [], {}, { logPath: join(projectRoot, 'console.log') })
    stdout.end('a final line without a newline')
    const write = vi.spyOn(process.stdout, 'write').mockImplementationOnce(() => {
      throw failure
    })
    let closeFailure: unknown
    try {
      child.emit('exit', 0, null)
      child.emit('close', 0, null)
    }
    catch (error) {
      closeFailure = error
    }
    finally {
      write.mockRestore()
    }
    expect(closeFailure).toBeUndefined()
    await expect(result).rejects.toBe(failure)
  })

  it('retains complete split UTF-8 output and waits for stream close after process exit', async () => {
    const stdout = new PassThrough()
    const stderr = new PassThrough()
    const child = Object.assign(new ChildProcess(), { stdout, stderr })
    vi.mocked(spawn).mockReturnValue(child)
    const logPath = join(projectRoot, 'console.log')
    let settled = false
    const result = runCommand('controlled', [], {}, { logPath, label: 'shard' })
    void result.then(() => {
      settled = true
    })
    const bytes = new TextEncoder().encode('before 😀 after\n')
    stdout.write(bytes.slice(0, 9))
    child.emit('exit', 0, null)
    await Promise.resolve()
    expect(settled).toBe(false)
    stdout.end(bytes.slice(9))
    stderr.end('an error without a newline')
    child.emit('close', 0, null)
    expect(await result).toBe(0)
    expect(readFileSync(logPath, 'utf8')).toBe('before 😀 after\nan error without a newline')
  })

  it('stops a child and closes its log when process observation throws', async () => {
    const failure = new Error('The process observer failed.')
    const child = Object.assign(new ChildProcess(), {
      pid: 1234,
      exitCode: null,
      signalCode: null,
      kill: vi.fn(() => {
        queueMicrotask(() => {
          child.emit('exit', null, 'SIGTERM')
          child.emit('close', null, 'SIGTERM')
        })
        return true
      }),
    })
    vi.mocked(spawn).mockReturnValue(child)
    const logPath = join(projectRoot, 'console.log')
    await expect(runCommand('controlled', [], {}, {
      logPath,
      observe: () => {
        throw failure
      },
    })).rejects.toBe(failure)
    expect(child.kill).toHaveBeenCalledWith('SIGTERM')
    rmSync(logPath)
    expect(existsSync(logPath)).toBe(false)
  })

  it('rejects a process startup error', async () => {
    const child = new ChildProcess()
    // Consume the event in the harness so a missing launcher handler cannot crash Vitest.
    child.on('error', () => {})
    vi.mocked(spawn).mockReturnValue(child)
    const error = new Error('executable not found')
    const result = runCommand('missing', [])
    child.emit('error', error)
    await expect(result).rejects.toBe(error)
  })

  it('reports failure when a child exits through a signal', async () => {
    const child = new ChildProcess()
    vi.mocked(spawn).mockReturnValue(child)
    const result = runCommand('fixture-child', [])
    child.emit('exit', null, 'SIGTERM')
    child.emit('close', null, 'SIGTERM')
    expect(await result).toBe(1)
  })
})
