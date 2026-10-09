import { existsSync, realpathSync } from 'node:fs'
import { isAbsolute, relative, sep } from 'node:path'

/**
 * Validate a native path against the directory that owns its test run.
 * Both paths must exist. The file system resolves each path through its symbolic links.
 */
export function assertPrivateNativePath(path: string, runDir: string): void {
  if (!path || !runDir)
    throw new Error('A private native path and run directory must be nonempty.')
  if (!existsSync(runDir))
    throw new Error(`The E2E run directory ${runDir} does not exist.`)
  if (!existsSync(path))
    throw new Error(`The private native path ${path} does not exist.`)
  // The JavaScript resolver normalizes parent segments before symbolic links.
  // The native resolver preserves the file system order for a link followed by `..`.
  const relativePath = relative(realpathSync.native(runDir), realpathSync.native(path))
  if (isAbsolute(relativePath) || relativePath === '..' || relativePath.startsWith(`..${sep}`))
    throw new Error('The private native path resolves outside the E2E run.')
}
