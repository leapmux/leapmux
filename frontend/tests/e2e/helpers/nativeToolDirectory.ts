import { lstatSync, mkdtempSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'

/** Create a private directory with literal shell metacharacters. Keep each scenario's original file basename. */
export function createNativeToolDirectory(workingDir: string): string {
  if (!workingDir || !isAbsolute(workingDir) || workingDir.includes('\0'))
    throw new Error('The native tool directory requires an absolute private working directory.')
  if (!lstatSync(workingDir).isDirectory())
    throw new Error('The native tool directory requires a real directory without a symlink.')
  return mkdtempSync(join(workingDir, 'native path $(touch command-expanded-marker) ; & \' `-'))
}
