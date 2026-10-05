import { randomUUID } from 'node:crypto'
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, join } from 'node:path'
import { isObject } from '../src/lib/jsonPick'

export type StateFileOperations = Pick<typeof import('node:fs'), 'mkdirSync' | 'writeFileSync' | 'renameSync' | 'rmSync'>
const fileOperations: StateFileOperations = { mkdirSync, writeFileSync, renameSync, rmSync }

/** Accept only an absolute destination, so a changed working directory cannot move the file. */
export function absoluteDestination(value: unknown, description: string): string {
  if (typeof value !== 'string' || !isAbsolute(value) || value.includes('\0'))
    throw new Error(`The ${description} must be an absolute path without NUL characters.`)
  return value
}

/**
 * Replace a state file only after its complete content reaches a private draft file.
 * A reader sees the preceding content or the new content, never a partial write.
 */
export function writeFileAtomically(path: string, content: string, io: StateFileOperations = fileOperations): void {
  const destination = absoluteDestination(path, 'state file destination')
  const directory = dirname(destination)
  io.mkdirSync(directory, { recursive: true })
  const draft = join(directory, `.leapmux-${randomUUID()}.writing`)
  try {
    io.writeFileSync(draft, content, { encoding: 'utf8', flag: 'wx', mode: 0o600 })
    io.renameSync(draft, destination)
  }
  catch (error) {
    try {
      io.rmSync(draft, { force: true })
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The state file write and its draft cleanup failed.')
    }
    throw error
  }
}

/** Read a state file. Return undefined only when the file does not exist, and throw every other read error. */
export function readOptionalStateFile(path: string): string | undefined {
  try {
    return readFileSync(path, 'utf8')
  }
  catch (error) {
    if (isObject(error) && error.code === 'ENOENT')
      return undefined
    throw error
  }
}
