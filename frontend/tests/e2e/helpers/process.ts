import type { ChildProcess } from 'node:child_process'

/**
 * Terminate `proc` with SIGTERM and wait for it to actually exit, escalating
 * to SIGKILL if it has not gone within `killAfterMs`.
 *
 * This replaces the "SIGTERM, sleep a flat second, SIGKILL" shape the suite
 * used wherever it tore down a spawned server. That shape is wrong in both
 * directions: a clean shutdown finishes well inside the second, so the rest of
 * it is dead wall time paid by every Playwright worker and every dev server;
 * and a wedged one needs longer than a second, so the SIGKILL landed while the
 * process was still draining. Waiting on the exit event gets both cases right
 * and costs nothing in the common one.
 */
export function stopProcess(proc: ChildProcess, killAfterMs = 5000): Promise<void> {
  try {
    proc.kill('SIGTERM')
  }
  catch { /* already dead */ }

  return new Promise<void>((resolve) => {
    if (proc.exitCode !== null || proc.signalCode !== null) {
      resolve()
      return
    }
    const escalate = setTimeout(() => {
      try {
        proc.kill('SIGKILL')
      }
      catch { /* already dead */ }
    }, killAfterMs)
    // Give SIGKILL the same grace again, then give up waiting.
    const abandon = setTimeout(() => {
      console.warn(`[e2e] pid ${proc.pid} did not exit after SIGKILL; abandoning it`)
      resolve()
    }, killAfterMs * 2)
    proc.once('exit', () => {
      clearTimeout(escalate)
      clearTimeout(abandon)
      resolve()
    })
  })
}

/** Terminate every process concurrently and wait for all of them to exit. */
export function stopProcesses(procs: ChildProcess[], killAfterMs = 5000): Promise<void[]> {
  return Promise.all(procs.map(p => stopProcess(p, killAfterMs)))
}
