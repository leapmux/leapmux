import { existsSync, lstatSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute } from 'node:path'
import { withCleanup } from './cleanup'
import { assertPrivateNativePath } from './nativePrivatePath'

/**
 * Require the nearest existing entry at or above `path` to resolve inside the private run. Return that entry.
 * A missing path uses its nearest existing ancestor. A write creates its missing directories below that ancestor.
 * The file system resolves symbolic links and parent segments before the guard compares paths.
 * A link outside the run fails. A broken link fails because its target does not resolve.
 * With `refuseSymlink`, the nearest existing entry must not be a symbolic link, even when its target stays inside.
 */
export function assertPrivateNativeAncestor(path: string, runDir: string, options: { refuseSymlink?: boolean } = {}): string {
  if (!path || !isAbsolute(path) || path.includes('\0'))
    throw new Error('The private native path requires an absolute path.')
  let existing = path
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

/**
 * Apply one private native fixture file and restore its exact bytes after success or failure.
 * Tests replace the initial writer to reproduce a partial setup failure.
 */
export async function withNativeConfigurationFile(
  options: { path: string, content: string, runDir: string },
  use: () => Promise<void>,
  writeFixture: (path: string, content: string) => void = (path, content) => writeFileSync(path, content, { mode: 0o600 }),
): Promise<void> {
  // Validate before creating directories. A parent link can point outside the private run.
  // A write through a broken link can create its target there.
  assertPrivateNativeAncestor(options.path, options.runDir)
  mkdirSync(dirname(options.path), { recursive: true })
  assertPrivateNativePath(dirname(options.path), options.runDir)
  const existing = existsSync(options.path)
  if (existing)
    assertPrivateNativePath(options.path, options.runDir)
  const original = existing ? readFileSync(options.path) : undefined
  await withCleanup(async () => {
    writeFixture(options.path, options.content)
    await use()
  }, async () => {
    // The native operation can replace the file or its parent with a link.
    assertPrivateNativeAncestor(options.path, options.runDir)
    if (original === undefined)
      rmSync(options.path, { force: true })
    else
      writeFileSync(options.path, original)
  })
}
