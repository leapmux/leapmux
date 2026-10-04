import type { ChildProcess, SpawnOptions } from 'node:child_process'
import { execFile, spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve, win32 } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { isObject } from '../src/lib/jsonPick'
import { withCleanup } from '../tests/e2e/helpers/cleanup'
import { stopProcess, validateProcessShutdownDelay } from '../tests/e2e/helpers/process'

interface JobMember {
  pid: number
  creationTime: string
}

export interface WindowsJobState {
  version: 1
  ownerPid: number
  rootPid: number
  complete: boolean
  members: JobMember[]
}

export interface WindowsJobPayload {
  version: 1
  command: string
  args: string[]
  argv0: string
  verbatimArguments: boolean
  cwd: string
  environment: Record<string, string>
  statePath: string
  stopEventName: string
  shutdownDelayMs: number
  searchCurrentDirectory: boolean
}

function requireText(value: unknown, field: string, empty = false): asserts value is string {
  if (typeof value !== 'string' || value.includes('\0'))
    throw new Error(`The Windows command ${field} must be a string without NUL.`)
  if (!empty && value.length === 0)
    throw new Error(`The Windows command ${field} must not be empty.`)
}

/** Keep the first key under Node's Windows key ordering. Keep empty values and omit undefined values. */
export function windowsCommandEnvironment(env: NodeJS.ProcessEnv): Record<string, string> {
  const result: Record<string, string> = {}
  const seen = new Set<string>()
  for (const key of Object.keys(env).sort()) {
    requireText(key, 'environment key')
    const folded = key.toLowerCase()
    if (seen.has(folded))
      continue
    seen.add(folded)
    const value = env[key]
    if (value === undefined)
      continue
    requireText(value, 'environment value', true)
    Object.defineProperty(result, key, { value, enumerable: true, configurable: true, writable: true })
  }
  return result
}

function isWindowsProcessId(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 1 && value <= 4_294_967_295
}

export function parseWindowsJobState(value: unknown, ownerPid: number): WindowsJobState {
  if (!isObject(value) || value.version !== 1 || value.ownerPid !== ownerPid || !isWindowsProcessId(value.ownerPid) || !isWindowsProcessId(value.rootPid) || typeof value.complete !== 'boolean' || !Array.isArray(value.members))
    throw new Error('The Windows job state does not identify its owning launcher.')
  const pids = new Set<number>()
  const members = value.members.map((member: unknown) => {
    if (!isObject(member) || !isWindowsProcessId(member.pid) || member.pid === ownerPid || pids.has(member.pid) || typeof member.creationTime !== 'string' || !/^[1-9]\d{0,18}$/u.test(member.creationTime) || BigInt(member.creationTime) > 9_223_372_036_854_775_807n)
      throw new Error('The Windows job state contains an invalid process identity.')
    pids.add(member.pid)
    return { pid: member.pid, creationTime: member.creationTime }
  })
  if (!pids.has(value.rootPid))
    throw new Error('The Windows job state contains no root process identity.')
  return { version: 1, ownerPid: value.ownerPid, rootPid: value.rootPid, complete: value.complete, members }
}

function powerShellPath(): string {
  const systemRoot = Object.entries(process.env).find(([key]) => key.toUpperCase() === 'SYSTEMROOT')?.[1]
  if (!systemRoot)
    throw new Error('The Windows system directory is unavailable.')
  return win32.join(systemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe')
}

export function windowsJobArguments(mode: 'Run' | 'Stop' | 'Verify' | 'Snapshot', payloadPath?: string): string[] {
  const scriptPath = resolve(import.meta.dirname, 'windows-command-job.ps1')
  return ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', scriptPath, '-Mode', mode, ...(payloadPath ? ['-PayloadPath', payloadPath] : [])]
}

interface JobControl {
  executable: string
  cwd: string
  environment: NodeJS.ProcessEnv
}

function runControl(mode: 'Stop' | 'Verify', payloadPath: string, control: JobControl, delay: number): Promise<void> {
  return new Promise((accept, reject) => {
    execFile(control.executable, windowsJobArguments(mode, payloadPath), { cwd: control.cwd, env: control.environment, windowsHide: true, timeout: delay }, (error) => {
      if (error)
        reject(error)
      else
        accept()
    })
  })
}

function waitForOwnerExit(child: ChildProcess, delay: number): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null || child.pid === undefined)
    return Promise.resolve()
  return new Promise((accept, reject) => {
    let finished = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const finish = (error?: unknown) => {
      if (finished)
        return
      finished = true
      if (timer !== undefined)
        clearTimeout(timer)
      child.off('exit', exited)
      child.off('error', failed)
      if (error === undefined)
        accept()
      else
        reject(error)
    }
    function exited() {
      finish()
    }
    function failed(error: Error) {
      finish(error)
    }
    child.once('exit', exited)
    child.once('error', failed)
    timer = setTimeout(() => {
      void stopProcess(child, delay).then(() => finish(), finish)
    }, delay)
  })
}

/** The job owns every descendant. The wrapped process handle owns the launcher. */
export function spawnWindowsCommandJob(command: string, args: string[], options: SpawnOptions, delay: number) {
  validateProcessShutdownDelay(delay)
  requireText(command, 'executable')
  if (options.shell)
    throw new Error('The Windows job launcher requires a direct executable.')
  const argumentsValue = [options.argv0 ?? command, ...args]
  for (const argument of argumentsValue)
    requireText(argument, 'argument', true)
  const cwd = typeof options.cwd === 'string' ? win32.resolve(options.cwd) : options.cwd ? fileURLToPath(options.cwd) : win32.resolve(process.cwd())
  const environment = windowsCommandEnvironment(options.env ?? process.env)
  const controlEnvironment = { ...process.env }
  for (const [key, value] of Object.entries(options.env ?? {})) {
    if (value !== undefined)
      controlEnvironment[key] = value
  }
  const control: JobControl = { executable: powerShellPath(), cwd, environment: controlEnvironment }
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'windows-command-job-'))
  const payloadPath = join(directory, 'payload.json')
  const statePath = join(directory, 'state.json')
  const payload: WindowsJobPayload = {
    version: 1,
    command,
    args: [...args],
    argv0: options.argv0 ?? command,
    verbatimArguments: options.windowsVerbatimArguments === true,
    cwd,
    environment,
    statePath,
    stopEventName: `Local\\LeapMuxE2EJob-${crypto.randomUUID()}`,
    shutdownDelayMs: Math.max(1, Math.ceil(delay)),
    searchCurrentDirectory: !Object.keys(process.env).some(key => key.toUpperCase() === 'NODEFAULTCURRENTDIRECTORYINEXEPATH'),
  }
  let child: ChildProcess
  try {
    writeFileSync(payloadPath, JSON.stringify(payload), { mode: 0o600, flag: 'wx' })
    child = spawn(control.executable, windowsJobArguments('Run', payloadPath), { ...options, argv0: undefined, shell: false, windowsVerbatimArguments: false, detached: false })
  }
  catch (error) {
    try {
      rmSync(directory, { recursive: true, force: true })
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The Windows job start and its private file cleanup failed.')
    }
    throw error
  }
  let stopping: Promise<void> | undefined
  let membersVerified = false
  return {
    child,
    stop: () => {
      stopping ??= withCleanup(async () => {
        if (child.pid === undefined) {
          membersVerified = true
          return
        }
        const errors: unknown[] = []
        const recordFailure = (error: unknown) => {
          if (!errors.includes(error))
            errors.push(error)
        }
        if (child.exitCode === null && child.signalCode === null) {
          try {
            await runControl('Stop', payloadPath, control, delay)
          }
          catch (error) {
            const absentEvent = isObject(error) && error.code === 2
            if (!absentEvent)
              recordFailure(error)
            try {
              await stopProcess(child, delay)
            }
            catch (stopError) {
              recordFailure(stopError)
            }
          }
          try {
            await waitForOwnerExit(child, delay)
          }
          catch (error) {
            recordFailure(error)
          }
        }
        if (existsSync(statePath)) {
          try {
            parseWindowsJobState(JSON.parse(readFileSync(statePath, 'utf8')), child.pid)
            await runControl('Verify', payloadPath, control, delay)
            membersVerified = true
          }
          catch (error) {
            recordFailure(error)
          }
        }
        else {
          membersVerified = true
        }
        if (errors.length === 1)
          throw errors[0]
        if (errors.length > 1)
          throw new AggregateError(errors, 'The Windows job shutdown and verification failed.')
      }, async () => {
        await stopProcess(child, delay)
        if (membersVerified)
          rmSync(directory, { recursive: true, force: true })
      })
      return stopping
    },
  }
}
