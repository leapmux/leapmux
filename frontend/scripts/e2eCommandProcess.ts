import type { ChildProcess, SpawnOptions } from 'node:child_process'
import { spawn } from 'node:child_process'
import process from 'node:process'
import { stopProcess, validateProcessShutdownDelay } from '../tests/e2e/helpers/process'
import { spawnWindowsCommandJob } from './windowsCommandJob'

export interface CommandProcess {
  readonly child: ChildProcess
  stop: () => Promise<void>
}

export interface CommandProcessOwnership {
  ownTree?: boolean
  shutdownDelayMs?: number
}

const PROCESS_CHECK_INTERVAL_MS = 25

function isProcessError(error: unknown, code: string): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && error.code === code
}

function signalOwnedProcess(pid: number, signal: NodeJS.Signals | 0): boolean {
  try {
    process.kill(pid, signal)
    return true
  }
  catch (error) {
    if (isProcessError(error, 'ESRCH'))
      return false
    throw error
  }
}

function requireOwnedPid(pid: number): void {
  if (!Number.isSafeInteger(pid) || pid <= 1 || pid > 2_147_483_647 || pid === process.pid)
    throw new RangeError('The owned command PID must identify a private child process.')
}

/** Wait for the root and its owned descendants. A root exit alone does not end tree ownership. */
function stopOwnedProcessGroup(child: ChildProcess, delay: number): Promise<void> {
  const pid = child.pid
  if (pid === undefined)
    return Promise.resolve()
  const groupId = pid
  try {
    requireOwnedPid(pid)
  }
  catch (error) {
    return Promise.reject(error)
  }
  return new Promise((accept, reject) => {
    let ended = child.exitCode !== null || child.signalCode !== null
    let finished = false
    let force = false
    let signaling = false
    let pendingExitError: unknown
    let deadline = performance.now() + delay
    let timer: ReturnType<typeof setTimeout> | undefined
    const finish = (error?: unknown) => {
      if (finished)
        return
      finished = true
      if (timer !== undefined)
        clearTimeout(timer)
      child.off('exit', onExit)
      child.off('error', onError)
      if (error === undefined)
        accept()
      else
        reject(error)
    }
    const terminate = (forced: boolean) => {
      signaling = true
      void Promise.resolve().then(() => signalOwnedProcess(-groupId, forced ? 'SIGKILL' : 'SIGTERM')).then((sent) => {
        signaling = false
        check(sent)
      }, (error) => {
        signaling = false
        handleSignalError(error)
      })
    }
    function handleSignalError(error: unknown, afterSuccessfulTermination = false) {
      if (finished)
        return
      if (!ended && isProcessError(error, 'EPERM') && (afterSuccessfulTermination || child.stdout?.readableEnded)) {
        // The group probe can return EPERM after termination before native exit.
        // Wait for that exact event before signaling surviving descendants again.
        pendingExitError ??= error
        if (timer !== undefined)
          clearTimeout(timer)
        timer = setTimeout(finish, Math.max(0, deadline - performance.now()), pendingExitError)
        return
      }
      finish(error)
    }
    function check(afterSuccessfulTermination = false) {
      if (finished || signaling)
        return
      if (timer !== undefined) {
        clearTimeout(timer)
        timer = undefined
      }
      try {
        if (!signalOwnedProcess(-groupId, 0) && ended) {
          finish()
          return
        }
        if (performance.now() >= deadline) {
          if (force) {
            finish(new Error(`The owned process group ${groupId} did not exit after forced termination.`))
            return
          }
          force = true
          deadline = performance.now() + delay
          terminate(true)
          return
        }
        timer = setTimeout(check, Math.min(PROCESS_CHECK_INTERVAL_MS, Math.max(1, deadline - performance.now())), afterSuccessfulTermination)
      }
      catch (error) {
        handleSignalError(error, afterSuccessfulTermination)
      }
    }
    function onExit() {
      ended = true
      if (pendingExitError !== undefined) {
        pendingExitError = undefined
        if (timer !== undefined) {
          clearTimeout(timer)
          timer = undefined
        }
        terminate(force)
        return
      }
      check()
    }
    function onError(error: Error) {
      finish(error)
    }
    child.once('exit', onExit)
    child.once('error', onError)
    terminate(false)
  })
}

/** Spawn one command and keep its exact handle. Only tree ownership creates a private POSIX process group. */
export function spawnCommandProcess(
  command: string,
  args: string[],
  spawnOptions: SpawnOptions = {},
  ownership: CommandProcessOwnership = {},
): CommandProcess {
  const delay = ownership.shutdownDelayMs ?? 5000
  validateProcessShutdownDelay(delay)
  const ownTree = ownership.ownTree === true
  if (ownTree && process.platform === 'win32')
    return spawnWindowsCommandJob(command, args, spawnOptions, delay)
  const child = spawn(command, args, ownTree ? { ...spawnOptions, detached: true } : spawnOptions)
  let stopping: Promise<void> | undefined
  return {
    child,
    stop: () => {
      stopping ??= ownTree ? stopOwnedProcessGroup(child, delay) : stopProcess(child, delay)
      return stopping
    },
  }
}
