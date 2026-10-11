import type { ChildProcess } from 'node:child_process'
import type { GatedOutput } from './outputGate'
import { spawn } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createOutputGate, runWithGatedOutput } from './outputGate'
import { quotePosixShellArgument } from './shellArguments'

const scratchRoot = resolve(process.cwd(), '../.tmp')
const DEADLINE_MS = 30_000
/** The limit of one shell test. It holds several waits of `DEADLINE_MS`, so it is longer than one wait. */
const SHELL_TEST_TIMEOUT_MS = 90_000
let directory: string
const children: ChildProcess[] = []

beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'output-gate-unit-'))
})
afterEach(() => {
  for (const child of children.splice(0))
    child.kill('SIGKILL')
  rmSync(directory, { recursive: true, force: true })
})

interface ShellRun {
  child: ChildProcess
  stdout: () => string
  stderr: () => string
  exit: Promise<number | null>
}

/** Start `<shell> <flags> -c <command>` and collect its streams. The afterEach hook kills a run that a test leaves behind. */
function startShell({ path, flags }: ShellUnderTest, command: string, env: NodeJS.ProcessEnv = process.env): ShellRun {
  const child = spawn(path, [...flags, '-c', command], { cwd: directory, env, stdio: ['ignore', 'pipe', 'pipe'] })
  children.push(child)
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', chunk => stdout += String(chunk))
  child.stderr.on('data', chunk => stderr += String(chunk))
  const exit = new Promise<number | null>(resolveExit => child.on('close', code => resolveExit(code)))
  return { child, stdout: () => stdout, stderr: () => stderr, exit }
}

/**
 * A `sleep` that returns at once and counts its calls.
 *
 * The hold loop calls `sleep` once for each check of the release file. A test that
 * waits for several calls knows that the shell still waits, with no timer to size.
 */
function countingSleepEnvironment(): { env: NodeJS.ProcessEnv, polls: () => number } {
  const bin = join(directory, 'shim-bin')
  const log = join(directory, 'sleep-calls')
  mkdirSync(bin)
  writeFileSync(log, '')
  const shim = join(bin, 'sleep')
  writeFileSync(shim, `#!/bin/sh\necho poll >> ${quotePosixShellArgument(log)}\n`)
  chmodSync(shim, 0o755)
  return {
    env: { ...process.env, PATH: `${bin}:${process.env.PATH ?? ''}` },
    polls: () => readFileSync(log, 'utf8').split('\n').filter(Boolean).length,
  }
}

interface ShellUnderTest {
  path: string
  /**
   * Flags that keep the shell from reading the startup files of the user.
   * A `~/.zshenv` can reorder PATH, and the test needs its `sleep` first on PATH.
   */
  flags: string[]
}

const SHELLS: ShellUnderTest[] = [
  { path: '/bin/sh', flags: [] },
  { path: '/bin/bash', flags: [] },
  { path: '/bin/zsh', flags: ['-f'] },
  { path: '/bin/dash', flags: [] },
]

describe('createOutputGate', () => {
  it('refuses a relative directory', () => {
    expect(() => createOutputGate('relative/gate')).toThrow('absolute')
  })

  it('refuses a directory that does not exist', () => {
    expect(() => createOutputGate(join(directory, 'absent'))).toThrow('directory')
  })

  it('refuses a path that is a file', () => {
    const file = join(directory, 'file')
    writeFileSync(file, '')
    expect(() => createOutputGate(file)).toThrow('directory')
  })

  it('gives each gate its own release file inside the directory', () => {
    const first = createOutputGate(directory)
    const second = createOutputGate(directory)
    expect(first.releasePath).not.toBe(second.releasePath)
    expect(first.releasePath.startsWith(directory)).toBe(true)
    first.release()
    expect(existsSync(first.releasePath)).toBe(true)
    expect(existsSync(second.releasePath)).toBe(false)
  })

  it('creates no release file before release', () => {
    const gate = createOutputGate(directory)
    expect(existsSync(gate.releasePath)).toBe(false)
  })

  it('creates the release file once and accepts a second release', () => {
    const gate = createOutputGate(directory)
    gate.release()
    gate.release()
    expect(existsSync(gate.releasePath)).toBe(true)
  })
})

describe('OutputGate hold', () => {
  it('refuses an empty command', () => {
    const gate = createOutputGate(directory)
    expect(() => gate.hold('')).toThrow('command')
    expect(() => gate.hold('  \n')).toThrow('command')
  })

  it('keeps the original command text and quotes the release path', () => {
    const nested = join(directory, 'path with $(touch expanded) ; & \' `-gate')
    mkdirSync(nested)
    const gate = createOutputGate(nested)
    const command = 'printf \'OUT%s\\n\' 42'
    const held = gate.hold(command)
    expect(held).toContain(quotePosixShellArgument(gate.releasePath))
    expect(held.endsWith(`; ${command}`)).toBe(true)
  })

  it.runIf(existsSync('/bin/sh'))('holds every command that it wrapped until one release', async () => {
    const { env, polls } = countingSleepEnvironment()
    const shell: ShellUnderTest = { path: '/bin/sh', flags: [] }
    const gate = createOutputGate(directory)
    const first = startShell(shell, gate.hold('printf \'ONE%s\\n\' 1'), env)
    const second = startShell(shell, gate.hold('printf \'TWO%s\\n\' 2'), env)
    await vi.waitFor(() => expect([first.stdout(), second.stdout()]).toEqual(['ONE1\n', 'TWO2\n']), { timeout: DEADLINE_MS, interval: 5 })
    // Both shells check the one release file, so the calls of `sleep` come from both.
    await vi.waitFor(() => expect(polls()).toBeGreaterThanOrEqual(6), { timeout: DEADLINE_MS, interval: 5 })
    expect([first.child.exitCode, second.child.exitCode]).toEqual([null, null])
    gate.release()
    expect(await Promise.all([first.exit, second.exit])).toEqual([0, 0])
  }, SHELL_TEST_TIMEOUT_MS)

  describe.each(SHELLS)('in $path', (shell) => {
    const run = (title: string, body: () => Promise<void>) => it.runIf(existsSync(shell.path))(title, body, SHELL_TEST_TIMEOUT_MS)

    run('keeps a fast command alive after its output until release, then keeps its output', async () => {
      const { env, polls } = countingSleepEnvironment()
      const gate = createOutputGate(directory)
      const shellRun = startShell(shell, gate.hold('printf \'OUT%s\\n\' 42'), env)
      await vi.waitFor(() => expect(shellRun.stdout()).toBe('OUT42\n'), { timeout: DEADLINE_MS, interval: 5 })
      // The shell checks the release file again and again, so it still runs.
      const seen = polls()
      await vi.waitFor(() => expect(polls()).toBeGreaterThanOrEqual(seen + 3), { timeout: DEADLINE_MS, interval: 5 })
      expect(shellRun.child.exitCode).toBeNull()
      gate.release()
      expect(await shellRun.exit).toBe(0)
      expect(shellRun.stdout()).toBe('OUT42\n')
      expect(shellRun.stderr()).toBe('')
    })

    run('keeps the status and the standard error of a command that exits with a failure', async () => {
      const { env, polls } = countingSleepEnvironment()
      const gate = createOutputGate(directory)
      const shellRun = startShell(shell, gate.hold('printf \'ERR%s\\n\' 77 >&2; exit 7'), env)
      await vi.waitFor(() => expect(shellRun.stderr()).toBe('ERR77\n'), { timeout: DEADLINE_MS, interval: 5 })
      await vi.waitFor(() => expect(polls()).toBeGreaterThanOrEqual(3), { timeout: DEADLINE_MS, interval: 5 })
      expect(shellRun.child.exitCode).toBeNull()
      gate.release()
      expect(await shellRun.exit).toBe(7)
      expect(shellRun.stderr()).toBe('ERR77\n')
      expect(shellRun.stdout()).toBe('')
    })

    run('keeps the status of a command whose last step fails', async () => {
      const gate = createOutputGate(directory)
      gate.release()
      const shellRun = startShell(shell, gate.hold('printf \'OUT%s\\n\' 42; false'))
      expect(await shellRun.exit).toBe(1)
      expect(shellRun.stdout()).toBe('OUT42\n')
    })

    run('ends a command at once when the release file exists before the command ends', async () => {
      const gate = createOutputGate(directory)
      gate.release()
      const shellRun = startShell(shell, gate.hold('printf \'OUT%s\\n\' 42'))
      expect(await shellRun.exit).toBe(0)
      expect(shellRun.stdout()).toBe('OUT42\n')
    })

    run('does not run a shell metacharacter of the release path', async () => {
      const nested = join(directory, 'path with $(touch expanded-marker) ; & \' `-gate')
      mkdirSync(nested)
      const gate = createOutputGate(nested)
      gate.release()
      const shellRun = startShell(shell, gate.hold('printf \'OUT%s\\n\' 42'))
      expect(await shellRun.exit).toBe(0)
      expect(shellRun.stdout()).toBe('OUT42\n')
      expect(existsSync(join(directory, 'expanded-marker'))).toBe(false)
    })
  })
})

describe('runWithGatedOutput', () => {
  /** A promise that a test settles from outside. */
  function deferred<T = void>() {
    let resolveValue!: (value: T) => void
    let rejectValue!: (error: unknown) => void
    const promise = new Promise<T>((accept, refuse) => {
      resolveValue = accept
      rejectValue = refuse
    })
    return { promise, resolve: resolveValue, reject: rejectValue }
  }

  /** A gate that records the order of its release and lets a test await it. */
  function fakeGated(shown: () => Promise<void>) {
    const released = deferred()
    const release = vi.fn(() => released.resolve())
    const gated: GatedOutput = { gate: { releasePath: '/unused', hold: command => command, release }, shown }
    return { gated, release, released: released.promise }
  }

  it('runs the operation alone when no gate exists', async () => {
    const run = vi.fn(async () => 'result')
    await expect(runWithGatedOutput(undefined, run)).resolves.toBe('result')
    expect(run).toHaveBeenCalledOnce()
  })

  it('releases the gate only after the output shows, and the operation waits for that release', async () => {
    const shown = deferred()
    const { gated, release, released } = fakeGated(() => shown.promise)
    const run = vi.fn(async () => {
      await released
      return 'finished'
    })
    const outcome = runWithGatedOutput(gated, run)
    // The operation starts at once, because it can need an approval click while the output shows.
    expect(run).toHaveBeenCalledOnce()
    await Promise.resolve()
    expect(release).not.toHaveBeenCalled()
    shown.resolve()
    await expect(outcome).resolves.toBe('finished')
    expect(release).toHaveBeenCalled()
  })

  it('releases the gate and rethrows the failure when the output never shows', async () => {
    const failure = new Error('the output did not show')
    const { gated, release } = fakeGated(() => Promise.reject(failure))
    const pending = deferred()
    await expect(runWithGatedOutput(gated, () => pending.promise)).rejects.toBe(failure)
    expect(release).toHaveBeenCalled()
    pending.resolve()
  })

  it('releases the gate and rethrows the failure when the operation fails first', async () => {
    const failure = new Error('the operation failed')
    const shown = deferred()
    const { gated, release } = fakeGated(() => shown.promise)
    await expect(runWithGatedOutput(gated, () => Promise.reject(failure))).rejects.toBe(failure)
    expect(release).toHaveBeenCalled()
    shown.resolve()
  })

  it('reports both failures when the operation and the release fail', async () => {
    const failure = new Error('the operation failed')
    const releaseFailure = new Error('the release failed')
    const gated: GatedOutput = {
      gate: { releasePath: '/unused', hold: command => command, release: () => { throw releaseFailure } },
      shown: () => new Promise<void>(() => {}),
    }
    const error = await runWithGatedOutput(gated, () => Promise.reject(failure)).then(() => null, (caught: unknown) => caught)
    expect((error as AggregateError).errors).toEqual([failure, releaseFailure])
  })

  it('releases a real gate so that a held shell command ends', async () => {
    const gate = createOutputGate(directory)
    const shell: ShellUnderTest = { path: '/bin/sh', flags: [] }
    if (!existsSync(shell.path))
      return
    const shellRun = startShell(shell, gate.hold('printf \'OUT%s\\n\' 42'))
    const gated: GatedOutput = {
      gate,
      shown: () => vi.waitFor(() => expect(shellRun.stdout()).toBe('OUT42\n'), { timeout: DEADLINE_MS, interval: 5 }).then(() => {}),
    }
    await expect(runWithGatedOutput(gated, () => shellRun.exit)).resolves.toBe(0)
    expect(existsSync(gate.releasePath)).toBe(true)
  })
})
