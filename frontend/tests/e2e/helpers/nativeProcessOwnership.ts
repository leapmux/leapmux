import type { ProcessRow } from './processTree'
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
    const normalize = (value: string) => process.platform === 'win32' ? value.toLowerCase() : value
    const expected = normalize(workerExecutable)
    const command = normalize(row.rawCommand ?? row.command)
    const isWorker = row.executable
      ? normalize(row.executable) === expected
      : [expected, `"${expected}"`, `'${expected}'`].some(prefix => command === prefix || command.startsWith(`${prefix} `) || command.startsWith(`${prefix}\t`))
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
