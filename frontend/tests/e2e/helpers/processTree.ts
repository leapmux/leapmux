/**
 * The process table of the machine, for a spec that checks which processes an
 * agent leaves behind when it closes.
 *
 * POSIX hosts use `ps`. Windows uses the native Win32_Process table.
 */
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { windowsJobArguments } from '../../../scripts/windowsCommandJob'

/** One process of the machine: its id, its parent and its command line. */
export interface ProcessRow {
  pid: number
  ppid: number
  command: string
  executable?: string
  rawCommand?: string
  /** Keep Windows FILETIME bytes. A JavaScript number can lose their precision. */
  creationTime?: string
}

/** A whole decimal number, as `ps` prints a process id. */
const PROCESS_ID = /^\d+$/

/**
 * One row of `ps -o pid=,ppid=,command=`: two numbers and the command line after
 * them. One space joins the words of the command, and a test that looks for the
 * name of a program reads them the same way. Null for a row of another shape.
 */
export function parseProcessRow(line: string): ProcessRow | null {
  const [pid, ppid, ...command] = line.trim().split(/\s+/)
  if (!pid || !ppid || !PROCESS_ID.test(pid) || !PROCESS_ID.test(ppid))
    return null
  const processId = Number(pid)
  const parentId = Number(ppid)
  if (!Number.isSafeInteger(processId) || processId <= 0 || !Number.isSafeInteger(parentId) || parentId < 0)
    return null
  const normalized = command.join(' ')
  const raw = /^\s*\d+\s+\d+\s+(\S.*)$/.exec(line)?.[1]?.trimEnd()
  return { pid: processId, ppid: parentId, command: normalized, ...(raw !== undefined && raw !== normalized ? { rawCommand: raw } : {}) }
}

/** Read the native Windows process records without losing an executable path with spaces. */
export function parseWindowsProcessTable(text: string): ProcessRow[] {
  const decoded: unknown = text.trim() === '' ? [] : JSON.parse(text)
  if (decoded === null)
    return []
  const entries = Array.isArray(decoded) ? decoded : [decoded]
  return entries.map((value: unknown) => {
    if (typeof value !== 'object' || value === null || Array.isArray(value)
      || !('ProcessId' in value) || typeof value.ProcessId !== 'number' || !Number.isSafeInteger(value.ProcessId) || value.ProcessId < 0
      || !('ParentProcessId' in value) || typeof value.ParentProcessId !== 'number' || !Number.isSafeInteger(value.ParentProcessId) || value.ParentProcessId < 0) {
      throw new Error('The Windows process table contains an invalid process ID.')
    }
    const command = 'CommandLine' in value ? value.CommandLine : null
    const executable = 'ExecutablePath' in value ? value.ExecutablePath : null
    const creationTime = 'CreationTime' in value ? value.CreationTime : null
    if ((command !== null && typeof command !== 'string') || (executable !== null && typeof executable !== 'string'))
      throw new Error('The Windows process table contains an invalid command or executable.')
    if (creationTime !== null && (typeof creationTime !== 'string' || !/^[1-9]\d{0,18}$/u.test(creationTime) || BigInt(creationTime) > 9_223_372_036_854_775_807n))
      throw new Error('The Windows process table contains an invalid creation identity.')
    return { pid: value.ProcessId, ppid: value.ParentProcessId, command: command ?? executable ?? '', ...(executable ? { executable } : {}), ...(creationTime !== null ? { creationTime } : {}) }
  }).filter(row => row.pid > 0)
}

/** Read native process records through `ps` or the Windows helper. */
export function listProcesses(): ProcessRow[] {
  if (process.platform === 'win32') {
    const scratch = resolve(import.meta.dirname, '../../../../.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'windows-process-table-'))
    let rows: ProcessRow[]
    try {
      const output = execFileSync('powershell.exe', windowsJobArguments('Snapshot'), { encoding: 'utf8', env: { ...process.env, TEMP: directory, TMP: directory }, timeout: 30_000 })
      rows = parseWindowsProcessTable(output)
    }
    catch (error) {
      try {
        rmSync(directory, { recursive: true, force: true })
      }
      catch (cleanupError) {
        throw new AggregateError([error, cleanupError], 'The native process query and its private cleanup failed.')
      }
      throw error
    }
    rmSync(directory, { recursive: true, force: true })
    return rows
  }
  const output = execFileSync('ps', ['-axo', 'pid=,ppid=,command='], { encoding: 'utf8' })
  return output.split('\n').flatMap((line) => {
    const row = parseProcessRow(line)
    return row ? [row] : []
  })
}

/**
 * The process ids of each root and of every descendant of it, in one snapshot.
 *
 * A process that starts after the snapshot, and one that the system already moved
 * to another parent, are not in the result. {@link newProcessesMatching} finds
 * those by their command line.
 */
export function withDescendants(rows: readonly ProcessRow[], roots: readonly number[]): number[] {
  const found = new Set(roots)
  let grew = true
  while (grew) {
    grew = false
    for (const row of rows) {
      if (found.has(row.ppid) && !found.has(row.pid)) {
        found.add(row.pid)
        grew = true
      }
    }
  }
  return [...found]
}

/** The processes that `before` does not hold and whose command line holds `text`. */
export function newProcessesMatching(rows: readonly ProcessRow[], before: ReadonlySet<number>, text: string): ProcessRow[] {
  return rows.filter(row => !before.has(row.pid) && row.command.includes(text))
}

/** Whether a process of this id still exists. */
export function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  }
  catch (error) {
    if (typeof error === 'object' && error !== null && 'code' in error) {
      if (error.code === 'ESRCH')
        return false
      // EPERM means that the process exists and belongs to another user.
      if (error.code === 'EPERM')
        return true
    }
    throw error
  }
}
