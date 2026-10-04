import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { withCleanup } from './cleanup'
import { assertPrivateNativePath } from './nativeCredentialIsolation'

/** Apply one private native fixture file and restore its exact bytes after success or failure. */
export async function withNativeConfigurationFile(
  options: { path: string, content: string, runDir: string },
  use: () => Promise<void>,
): Promise<void> {
  let ancestor = resolve(dirname(options.path))
  while (!existsSync(ancestor)) {
    const parent = dirname(ancestor)
    if (parent === ancestor)
      throw new Error('The native configuration path has no existing parent.')
    ancestor = parent
  }
  // Validate before creating directories. A parent symlink can point outside the private run.
  assertPrivateNativePath(ancestor, options.runDir)
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
