/**
 * The process table of the machine, for a spec that checks which processes an
 * agent leaves behind when it closes.
 *
 * The table comes from `ps`, which has no Windows form. A spec that reads it skips
 * on Windows.
 */
import { execFileSync } from 'node:child_process'
import process from 'node:process'

/** One process of the machine: its id, its parent and its command line. */
export interface ProcessRow {
  pid: number
  ppid: number
  command: string
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
  return { pid: Number(pid), ppid: Number(ppid), command: command.join(' ') }
}

/** Every process of the machine, read with `ps`. */
export function listProcesses(): ProcessRow[] {
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
    // EPERM: the process exists, and it belongs to another user.
    return (error as NodeJS.ErrnoException).code === 'EPERM'
  }
}
