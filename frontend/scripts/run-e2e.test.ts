import type { ChildProcess, SpawnOptions } from 'node:child_process'
import { execFileSync, spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LEAPMUX_BINARY_NAME, runBinaryPath } from '../tests/e2e/helpers/runBinary'
import { runCommand, runE2E } from './run-e2e'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  const spawn = vi.fn()
  return { ...actual, spawn, default: { ...actual, spawn } }
})
vi.mock('./resolve-task-bin', () => ({ resolveTaskBin: () => 'task' }))

const calls: { command: string, args: string[], env: NodeJS.ProcessEnv }[] = []
const runDirs = new Set<string>()
/** A project root of this test's own, so no test reads or writes the real build output. */
let projectRoot: string
/** The file that `task build-backend` writes in `projectRoot`. */
let buildOutput: string

beforeEach(() => {
  calls.length = 0
  vi.stubEnv('LEAPMUX_DEV', '')
  vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', '')
  vi.stubEnv('LEAPMUX_E2E_NONCE', '')
  const scratch = resolve(import.meta.dirname, '../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  projectRoot = mkdtempSync(join(scratch, 'run-e2e-test-'))
  buildOutput = join(projectRoot, LEAPMUX_BINARY_NAME)
  writeFileSync(buildOutput, 'first build')
})

afterEach(() => {
  for (const dir of runDirs)
    rmSync(dir, { recursive: true, force: true })
  runDirs.clear()
  rmSync(projectRoot, { recursive: true, force: true })
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

function processes(exitCodes = [0, 0], inspect?: (env: NodeJS.ProcessEnv) => void) {
  vi.mocked(spawn).mockImplementation(((command: string, args: string[], options: SpawnOptions) => {
    const env = { ...options.env }
    calls.push({ command, args, env })
    const noncePath = env.LEAPMUX_E2E_NONCE_PATH
    if (noncePath) {
      runDirs.add(dirname(noncePath))
      expect(readFileSync(noncePath, 'utf8')).toBe(env.LEAPMUX_E2E_NONCE)
      inspect?.(env)
    }
    const child = new EventEmitter()
    queueMicrotask(() => child.emit('exit', exitCodes.shift() ?? 0, null))
    return child as ChildProcess
  }) as typeof spawn)
}

describe('end-to-end launcher', () => {
  it('uses the cached backend build and passes development mode to both processes', async () => {
    processes()
    expect(await runE2E(['--grep', 'a pattern with spaces'], projectRoot)).toBe(0)
    expect(calls[0]?.args).toEqual(['build-backend'])
    expect(calls).toHaveLength(2)
    expect(calls.every(call => call.env.LEAPMUX_DEV === '1')).toBe(true)
    expect(calls[1]?.args.slice(-2)).toEqual(['--grep', 'a pattern with spaces'])
    expect(process.env.LEAPMUX_DEV).toBe('')
  })

  it.each([0, 3])('removes its nonce directory after Playwright exits with %i', async (exitCode) => {
    processes([0, exitCode])
    expect(await runE2E([], projectRoot)).toBe(exitCode)
    expect(runDirs.size).toBe(1)
    for (const dir of runDirs)
      expect(existsSync(dir)).toBe(false)
  })

  it('does not start Playwright after the build fails', async () => {
    processes([7])
    expect(await runE2E([], projectRoot)).toBe(7)
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
    expect(await runE2E([], projectRoot)).toBe(0)
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
    expect(await runE2E([], projectRoot)).toBe(0)
    expect(copy).not.toBe('')
    // The copy belongs to the run directory, so the cleanup removes it with the run.
    expect(existsSync(copy)).toBe(false)
  })

  it('does not start Playwright when the build left no binary', async () => {
    rmSync(buildOutput)
    processes()
    await expect(runE2E([], projectRoot)).rejects.toThrow(expect.objectContaining({ code: 'ENOENT' }))
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
    expect(await runE2E([], projectRoot)).toBe(9)
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
    await expect(runE2E([], projectRoot)).rejects.toThrow('Test cleanup failed')
    expect(existsSync(record)).toBe(true)
  })

  it('keeps private working directories separate from the project repository', async () => {
    vi.stubEnv('GIT_DIR', 'inherited-repository')
    processes([0, 0], (env) => {
      expect(env.GIT_DIR).toBeUndefined()
      const directory = join(dirname(env.LEAPMUX_E2E_NONCE_PATH!), 'working-directory')
      mkdirSync(directory)
      expect(() => execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: directory, env, stdio: 'ignore' })).toThrow()
      execFileSync('git', ['-c', 'init.templateDir=', 'init', '--quiet'], { cwd: directory, env, stdio: 'ignore' })
      const output = execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: directory, env, encoding: 'utf8' })
      expect(resolve(output.trim())).toBe(directory)
    })
    expect(await runE2E([], projectRoot)).toBe(0)
  })

  it('rejects a process startup error', async () => {
    const child = new EventEmitter()
    // Consume the event in the harness so a missing launcher handler cannot crash Vitest.
    child.on('error', () => {})
    vi.mocked(spawn).mockReturnValue(child as ChildProcess)
    const error = new Error('executable not found')
    let outcome: unknown
    void runCommand('missing', []).then(value => outcome = value, reason => outcome = reason)
    child.emit('error', error)
    await vi.waitFor(() => expect(outcome).toBe(error), { timeout: 25, interval: 1 })
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
    await expect(runE2E([], projectRoot)).rejects.toBe(error)
    expect(runDirs.size).toBe(1)
    for (const dir of runDirs)
      expect(existsSync(dir)).toBe(false)
  })

  it('reports failure when a child exits through a signal', async () => {
    const child = new EventEmitter()
    vi.mocked(spawn).mockReturnValue(child as ChildProcess)
    const result = runCommand('fixture-child', [])
    child.emit('exit', null, 'SIGTERM')
    expect(await result).toBe(1)
  })
})
