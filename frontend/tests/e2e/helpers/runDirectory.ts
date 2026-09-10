import { mkdtempSync } from 'node:fs'
import { basename, join } from 'node:path'
import { getGlobalState } from './server'

/** Create a private directory that the launcher removes after the test run. */
export function createTestDirectory(prefix: string): string {
  if (!prefix || prefix === '.' || prefix === '..' || basename(prefix) !== prefix || prefix.includes('\\'))
    throw new Error('The test directory prefix must be one filename component')
  return mkdtempSync(join(getGlobalState().tmpDir, prefix))
}
