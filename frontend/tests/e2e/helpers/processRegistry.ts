import type { ChildProcess, SpawnOptions } from 'node:child_process'
import { spawn } from 'node:child_process'
import { existsSync, lstatSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { finishCleanup } from './cleanup'
import { getGlobalState } from './server'

/** Register each child before returning its handle to a fixture. */
export function spawnTestProcess(command: string, args: string[], options: SpawnOptions): ChildProcess {
  const runDir = getGlobalState().tmpDir
  const child = spawn(command, args, options)
  try {
    trackProcess(runDir, child)
  }
  catch (error) {
    // The caller cannot own a child when registration throws before returning its handle.
    try {
      child.kill('SIGKILL')
    }
    catch (signalError) {
      throw new AggregateError([error, signalError], 'Process registration and termination failed')
    }
    throw error
  }
  return child
}

/** Keep one record per live child in the run that owns it. */
export function trackProcess(runDir: string, child: ChildProcess): void {
  if (child.pid === undefined || child.exitCode !== null || child.signalCode !== null)
    return
  const directory = join(runDir, 'processes')
  mkdirSync(directory, { recursive: true })
  const file = join(directory, String(child.pid))
  writeFileSync(file, '')
  child.once('exit', () => rmSync(file, { force: true }))
}

function signalProcess(pid: number, signal: NodeJS.Signals | 0): boolean {
  try {
    process.kill(pid, signal)
    return true
  }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ESRCH')
      return false
    throw error
  }
}

async function stopRecordedProcess(pid: number): Promise<void> {
  if (!signalProcess(pid, 'SIGTERM'))
    return
  // This fallback runs after a worker crash, when no ChildProcess handle remains.
  // Normal fixture teardown waits on exit events through stopProcess instead.
  for (const signal of ['SIGTERM', 'SIGKILL'] as const) {
    if (signal === 'SIGKILL' && !signalProcess(pid, signal))
      return
    const deadline = Date.now() + 5000
    while (signalProcess(pid, 0)) {
      if (Date.now() >= deadline)
        break
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    if (!signalProcess(pid, 0))
      return
  }
  throw new Error(`Test process ${pid} did not exit after SIGKILL`)
}

/** Stop only children that the current run still owns after fixture teardown. */
export async function stopTrackedProcesses(runDir: string): Promise<void> {
  const directory = join(runDir, 'processes')
  if (!existsSync(directory))
    return
  await finishCleanup(readdirSync(directory).map(async (file) => {
    const pid = Number(file)
    const path = join(directory, file)
    if (!/^[1-9]\d*$/.test(file) || !Number.isSafeInteger(pid) || pid > 2_147_483_647 || !lstatSync(path).isFile())
      throw new Error(`Invalid test process record: ${file}`)
    await stopRecordedProcess(pid)
    rmSync(path, { force: true })
  }))
}
