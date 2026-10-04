import type { ProcessRow } from '../helpers/processTree'
import { posix, win32 } from 'node:path'
import { withDescendants } from '../helpers/processTree'

interface KiroRunProcessOwner {
  workerPid: number
  beforePids: ReadonlySet<number>
  dataDir: string
}

function privateKasPrefix(dataDir: string): { prefix: string, windows: boolean } {
  const windows = /^[a-z]:[\\/]|^\\\\/i.test(dataDir)
  const paths = windows ? win32 : posix
  if (!dataDir.trim() || dataDir.includes('\0') || !paths.isAbsolute(dataDir))
    throw new Error('The Kiro private data directory must be an absolute directory.')
  const directory = paths.normalize(dataDir)
  if (directory === paths.parse(directory).root)
    throw new Error('The Kiro private data directory must not be a filesystem root.')
  const normalized = windows ? directory.replaceAll('\\', '/').toLowerCase() : directory
  return { prefix: `${normalized.replace(/\/+$/, '')}/kas/`, windows }
}

function hasKasArgument(row: ProcessRow, native: ReturnType<typeof privateKasPrefix>): boolean {
  const raw = row.rawCommand ?? row.command
  const command = native.windows ? raw.replaceAll('\\', '/').toLowerCase() : raw
  let position = command.indexOf(native.prefix)
  while (position !== -1) {
    const previous = command[position - 1]
    const beforeQuote = command[position - 2]
    if (position === 0 || (previous !== undefined && /\s/.test(previous))
      || ((previous === '"' || previous === '\'') && (position === 1 || (beforeQuote !== undefined && /\s/.test(beforeQuote))))) {
      return true
    }
    position = command.indexOf(native.prefix, position + 1)
  }
  return false
}

/** Select engines from Kiro's extracted bundle under the run's private data directory. */
export function kiroEngineProcesses(rows: readonly ProcessRow[], dataDir: string): ProcessRow[] {
  const native = privateKasPrefix(dataDir)
  return rows.filter(row => hasKasArgument(row, native))
}

/** Select new relays below the exact Worker and private engines that lost their parent. */
export function kiroRunProcesses(rows: readonly ProcessRow[], owner: KiroRunProcessOwner): ProcessRow[] {
  if (!Number.isSafeInteger(owner.workerPid) || owner.workerPid <= 0)
    throw new Error('The Kiro Worker PID must be a positive integer.')
  const engines = new Set(kiroEngineProcesses(rows, owner.dataDir).map(row => row.pid))
  const descendants = new Set(withDescendants(rows, [owner.workerPid]))
  const relay = /(?:^|[/\\])kiro-cli-chat(?:\.exe)?(?=["'\s]|$)/i
  return rows.filter(row => !owner.beforePids.has(row.pid)
    && (engines.has(row.pid) || (descendants.has(row.pid) && relay.test(row.rawCommand ?? row.command))))
}
