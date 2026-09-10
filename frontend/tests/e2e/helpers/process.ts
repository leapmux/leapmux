import type { ChildProcess } from 'node:child_process'
import { finishCleanup } from './cleanup'

/** Stop a process and wait for its exit. Escalate after the graceful shutdown deadline. */
export function stopProcess(proc: ChildProcess, killAfterMs = 5000): Promise<void> {
  if (!Number.isFinite(killAfterMs) || killAfterMs <= 0 || killAfterMs > 2_147_483_647)
    return Promise.reject(new RangeError('The shutdown delay must fit a positive Node timer delay'))
  if (proc.exitCode !== null || proc.signalCode !== null || proc.pid === undefined)
    return Promise.resolve()

  return new Promise<void>((resolve, reject) => {
    let finished = false
    let timer: ReturnType<typeof setTimeout>
    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(timer)
      proc.off('exit', onExit)
      proc.off('error', finish)
      if (error)
        reject(error)
      else
        resolve()
    }
    function onExit() {
      finish()
    }
    const signal = (value: NodeJS.Signals) => {
      try {
        proc.kill(value)
      }
      catch (error) {
        finish(error instanceof Error ? error : new Error(String(error)))
      }
    }
    proc.once('exit', onExit)
    proc.once('error', finish)
    timer = setTimeout(() => {
      signal('SIGKILL')
      if (!finished)
        timer = setTimeout(() => finish(new Error(`Test process ${proc.pid} did not exit after SIGKILL`)), killAfterMs)
    }, killAfterMs)
    signal('SIGTERM')
  })
}

/** Stop every process concurrently. Wait for every result before reporting failures. */
export async function stopProcesses(procs: ChildProcess[], killAfterMs = 5000): Promise<void> {
  await finishCleanup(procs.map(proc => stopProcess(proc, killAfterMs)))
}
