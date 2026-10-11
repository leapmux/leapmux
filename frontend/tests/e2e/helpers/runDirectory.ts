import { mkdtempSync } from 'node:fs'
import { basename, join } from 'node:path'
import { getGlobalState } from './server'

/**
 * Return true when `value` is one file-name component: a nonempty name that is not `.` or `..`, and that holds no
 * path separator and no NUL. Both separators count on every platform, so a name that passes on POSIX also stays one
 * component on Windows. Node refuses a path that holds a NUL, so the check refuses the name before a file operation does.
 */
export function isFileNameComponent(value: string): boolean {
  return value !== '' && value !== '.' && value !== '..' && basename(value) === value
    && !value.includes('/') && !value.includes('\\') && !value.includes('\0')
}

/** Create a private directory that the launcher removes after the test run. */
export function createTestDirectory(prefix: string): string {
  if (!isFileNameComponent(prefix))
    throw new Error('The test directory prefix must be one filename component')
  return mkdtempSync(join(getGlobalState().tmpDir, prefix))
}
