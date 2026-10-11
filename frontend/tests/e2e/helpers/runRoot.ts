import { lstatSync, realpathSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, relative, sep } from 'node:path'

/**
 * The run root of an E2E run: the private directory that the launcher (`scripts/run-e2e.ts`) creates for each run,
 * above every working directory of the run. It holds the sentinel instruction files of `./ancestorInstructions.ts`.
 */

/** The start of the name of each run root. The artifact directory of the run takes the rest of the name. */
export const RUN_ROOT_PREFIX = 'leapmux-e2e-'

/** The environment variable through which the launcher states the run root to the Playwright process of the run. */
export const RUN_ROOT_ENV = 'LEAPMUX_E2E_RUN_ROOT'

/** Whether `path` is `directory` or lies below it, by the text of both paths. */
function isWithin(path: string, directory: string): boolean {
  const rest = relative(directory, path)
  return rest === '' || (rest !== '..' && !rest.startsWith(`..${sep}`) && !isAbsolute(rest))
}

/** Resolve an existing path, or resolve the nearest existing ancestor of a missing path. */
function canonicalPath(path: string): string {
  let existing = path
  const missing: string[] = []
  for (;;) {
    if (lstatSync(existing, { throwIfNoEntry: false }))
      return join(realpathSync.native(existing), ...missing.reverse())
    const parent = dirname(existing)
    if (parent === existing)
      throw new Error('The path has no existing ancestor.')
    missing.push(basename(existing))
    existing = parent
  }
}

/**
 * Whether the absolute `path` is `directory` or lies below it.
 *
 * Resolve symbolic links before comparing the paths. A missing path uses its nearest existing ancestor.
 * A broken link or an unreadable path matches nothing. Both paths must be absolute and must contain no NUL.
 */
export function isInsideDirectory(path: string, directory: string): boolean {
  if (!isAbsolute(path) || !isAbsolute(directory) || path.includes('\0') || directory.includes('\0'))
    return false
  try {
    return isWithin(canonicalPath(path), canonicalPath(directory))
  }
  catch {
    return false
  }
}
