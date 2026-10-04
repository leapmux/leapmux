import { lstatSync } from 'node:fs'
import { dirname, isAbsolute } from 'node:path'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'

/** Check the resolved parent and reject a link at the path before a fixture writes it. */
export function assertPiConfigurationPath(path: string, runDirectory: string): void {
  if (!path || !isAbsolute(path) || path.includes('\0'))
    throw new Error('The private Pi configuration requires an absolute path.')
  let existing = path
  for (;;) {
    const entry = lstatSync(existing, { throwIfNoEntry: false })
    if (entry) {
      if (entry.isSymbolicLink())
        throw new Error('The private Pi configuration path must not use a symbolic link outside the E2E run.')
      assertPrivateNativePath(existing, runDirectory)
      return
    }
    const parent = dirname(existing)
    if (parent === existing)
      throw new Error('The private Pi configuration path has no existing parent.')
    existing = parent
  }
}
