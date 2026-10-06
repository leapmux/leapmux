import { existsSync, lstatSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { withCleanup } from './cleanup'
import { assertPrivateNativePath } from './nativeCredentialIsolation'

/**
 * Require the nearest existing entry at or above `path` to resolve inside the private run, and return that entry.
 * A missing path counts through its nearest existing ancestor, because a write creates the missing directories below
 * it. A symbolic link resolves to its target: a link to a target outside the run fails, and a broken link fails
 * because its target does not resolve. With `refuseSymlink`, the nearest existing entry must not be a symbolic link at
 * all, even one whose target lies inside the run.
 */
export function assertPrivateNativeAncestor(path: string, runDir: string, options: { refuseSymlink?: boolean } = {}): string {
  if (!path || !isAbsolute(path) || path.includes('\0'))
    throw new Error('The private native path requires an absolute path.')
  let existing = resolve(path)
  for (;;) {
    const entry = lstatSync(existing, { throwIfNoEntry: false })
    if (entry) {
      if (options.refuseSymlink && entry.isSymbolicLink())
        throw new Error('The private native path must not be a symbolic link, which could point outside the E2E run.')
      assertPrivateNativePath(existing, runDir)
      return existing
    }
    const parent = dirname(existing)
    if (parent === existing)
      throw new Error('The private native path has no existing ancestor.')
    existing = parent
  }
}

/** Apply one private native fixture file and restore its exact bytes after success or failure. */
export async function withNativeConfigurationFile(
  options: { path: string, content: string, runDir: string },
  use: () => Promise<void>,
): Promise<void> {
  // Validate before creating directories. A link at the path or at a parent can point outside the private run, and a
  // write through a broken link would create its target there.
  assertPrivateNativeAncestor(resolve(options.path), options.runDir)
  mkdirSync(dirname(options.path), { recursive: true })
  assertPrivateNativePath(dirname(options.path), options.runDir)
  const existing = existsSync(options.path)
  if (existing)
    assertPrivateNativePath(options.path, options.runDir)
  const original = existing ? readFileSync(options.path) : undefined
  writeFileSync(options.path, options.content, { mode: 0o600 })
  await withCleanup(use, async () => {
    if (original === undefined)
      rmSync(options.path, { force: true })
    else
      writeFileSync(options.path, original)
  })
}
