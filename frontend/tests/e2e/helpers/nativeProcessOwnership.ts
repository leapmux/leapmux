import type { ProcessRow } from './processTree'
import { isAbsolute } from 'node:path'
import process from 'node:process'
import { withDescendants } from './processTree'

export interface NativeProcessOwnership {
  workerPid: number
  rootPid: number
  ownedPids: number[]
}

/** Follow one held native tool to the Worker that owns its provider. */
export function resolveNativeProcessOwnership(rows: readonly ProcessRow[], toolPid: number, workerExecutable: string): NativeProcessOwnership {
  if (!Number.isSafeInteger(toolPid) || toolPid <= 0)
    throw new Error('The native tool PID must be a positive integer.')
  if (!workerExecutable)
    throw new Error('The native ownership resolver requires the Worker executable.')
  const byId = new Map(rows.map(row => [row.pid, row]))
  const owned: number[] = []
  const visited = new Set<number>()
  let pid = toolPid
  while (pid > 0) {
    if (visited.has(pid))
      throw new Error('The native process parents contain a cycle before the Worker boundary.')
    visited.add(pid)
    const row = byId.get(pid)
    if (!row)
      throw new Error('The native process snapshot cannot resolve the Worker boundary.')
    const isWorker = row.executable
      ? sameExecutablePath(row.executable, workerExecutable)
      : commandStartsWithExecutable(row.rawCommand ?? row.command, workerExecutable)
    if (isWorker) {
      const rootPid = owned.at(-1)
      if (rootPid === undefined)
        throw new Error('The held native tool PID identifies the shared Worker.')
      return { workerPid: pid, rootPid, ownedPids: withDescendants(rows, [rootPid]) }
    }
    owned.push(pid)
    pid = row.ppid
  }
  throw new Error('The native process parents end before the Worker boundary.')
}

/** Windows compares an executable path without case. */
function normalizeExecutable(value: string): string {
  return process.platform === 'win32' ? value.toLowerCase() : value
}

/** Whether two executable paths name the same file, as the host compares paths. */
export function sameExecutablePath(actual: string, expected: string): boolean {
  return normalizeExecutable(actual) === normalizeExecutable(expected)
}

/**
 * Whether the command line `command` starts with `executable` as its whole first word: bare, in double quotes, or in
 * single quotes. A longer path that only starts with the same text does not match.
 */
export function commandStartsWithExecutable(command: string, executable: string): boolean {
  const actual = normalizeExecutable(command)
  return [executable, `"${executable}"`, `'${executable}'`].some((prefix) => {
    const expected = normalizeExecutable(prefix)
    return actual === expected || actual.startsWith(`${expected} `) || actual.startsWith(`${expected}\t`)
  })
}

/** One word of a command line, with its offsets in the command line and whether a quote holds part of it. */
interface CommandWord {
  text: string
  start: number
  end: number
  quoted: boolean
}

/**
 * Split a command line into its words.
 *
 * Windows reports the command line with the quoting of the process that created it, so a word in double or single
 * quotes keeps its spaces, and a backslash before a double quote inside double quotes is a literal quote. `ps` reports
 * the argument vector joined with single spaces and no quoting, so there each run of whitespace ends a word.
 */
function commandWords(command: string): CommandWord[] {
  const words: CommandWord[] = []
  let index = 0
  while (index < command.length) {
    while (index < command.length && /\s/.test(command[index]!))
      index++
    if (index >= command.length)
      break
    const start = index
    let text = ''
    let quoted = false
    while (index < command.length && !/\s/.test(command[index]!)) {
      const quote = command[index]
      if (quote !== '"' && quote !== '\'') {
        text += quote
        index++
        continue
      }
      quoted = true
      index++
      for (;;) {
        if (index >= command.length)
          throw new Error(`The command line has an unbalanced ${quote} quote: ${command}`)
        const character = command[index]!
        if (character === quote) {
          index++
          break
        }
        if (quote === '"' && character === '\\' && command[index + 1] === '"') {
          text += '"'
          index += 2
          continue
        }
        text += character
        index++
      }
    }
    words.push({ text, start, end: index, quoted })
  }
  return words
}

/** The spellings of the data-directory flag of the Worker. The flag package of Go accepts one or two hyphens. */
const DATA_DIRECTORY_FLAGS = ['--data-dir', '-data-dir'] as const

/**
 * Read the data directory from the command line of a Worker, or of the `dev` and `hub` processes that hold one.
 *
 * The flag can take its value in the next word (`--data-dir /path`) or after `=` (`--data-dir=/path`), and other
 * arguments can follow it in any order. A `ps` command line has no quoting, so an unquoted path with a space spans
 * several words: the value then runs to the next word that starts with a hyphen. Such an unquoted path therefore
 * cannot hold a word that starts with a hyphen.
 *
 * The command line must hold exactly one data-directory argument, and its value must be an absolute path.
 */
export function workerDataDirectory(command: string): string {
  const words = commandWords(command)
  const values: string[] = []
  for (let index = 0; index < words.length; index++) {
    const word = words[index]!
    const flag = DATA_DIRECTORY_FLAGS.find(spelling => word.text === spelling || word.text.startsWith(`${spelling}=`))
    if (!flag)
      continue
    let first: CommandWord
    let inline: string | undefined
    if (word.text === flag) {
      const next = words[index + 1]
      if (!next)
        throw new Error(`The Worker command line has no value after its ${flag} argument.`)
      first = next
      index++
    }
    else {
      first = word
      inline = word.text.slice(flag.length + 1)
    }
    let last = first
    if (!first.quoted) {
      while (index + 1 < words.length && !words[index + 1]!.quoted && !words[index + 1]!.text.startsWith('-')) {
        index++
        last = words[index]!
      }
    }
    if (last === first) {
      values.push(inline ?? first.text)
      continue
    }
    // Keep the exact whitespace of the path between its words.
    const joined = command.slice(first.start, last.end)
    values.push(inline === undefined ? joined : joined.slice(flag.length + 1))
  }
  if (values.length !== 1)
    throw new Error(`The Worker command line must hold one data-directory argument, not ${values.length}: ${command}`)
  const path = values[0]!
  if (!isAbsolute(path) || path.includes('\n'))
    throw new Error(`The Worker data directory must be an absolute path on one line: ${JSON.stringify(path)}`)
  return path
}
