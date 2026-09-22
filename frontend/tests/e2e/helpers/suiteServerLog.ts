import { readFileSync, statSync } from 'node:fs'

/** Return the current byte offset in the shared server log. */
export function markSuiteServerLog(path: string): number {
  return statSync(path).size
}

/** Read the shared server log from a prior byte offset. */
export function readSuiteServerLog(path: string, from: number): string {
  if (!Number.isSafeInteger(from) || from < 0)
    throw new RangeError('The server log mark must be a non-negative safe integer')
  const bytes = readFileSync(path)
  return bytes.subarray(Math.min(from, bytes.byteLength)).toString('utf8')
}
