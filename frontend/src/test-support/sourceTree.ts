import { existsSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

// The file walk that every repo guard scanning the source tree runs.
//
// Eight guards carried a near-identical copy of it, and they had already
// drifted: two skipped the package install and the build outputs, and six
// skipped nothing at all. That is harmless only while each one happens to
// start below a directory the outputs cannot reach, and nothing holds them
// there -- a guard repointed at `frontendRoot` would have walked 1117 packages
// and reported whatever it found in them as repo code.
//
// `styleFiles.ts` made the same move for the `.css.ts` walk that three style
// guards shared. This is that walk generalized, and `collectStyleFiles` now
// delegates to it.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its
// own. Its cases live in `sourceTree.test.ts` beside it.

/**
 * The directories that no source scan may walk: the package install and the
 * build outputs. Their contents are not repo code, and a guard that reports
 * one of them reports something nobody can fix.
 *
 * Every `collectFiles` walk skips these. A caller adds to the set through
 * `alsoSkip`; it cannot replace them.
 */
export const SKIP_DIRS: ReadonlySet<string> = new Set([
  'node_modules',
  '.output',
  '.vinxi',
  'dist',
  'test-results',
])

export interface CollectFilesOptions {
  /** Reports whether a file belongs in the result, from its basename. */
  matches: (basename: string) => boolean
  /** Directory basenames to skip in ADDITION to `SKIP_DIRS`. */
  alsoSkip?: ReadonlySet<string>
}

/**
 * Every file under `dir`, recursively, whose basename `matches` accepts, as an
 * absolute path.
 *
 * Returns an empty array when `dir` is absent, so a guard aimed at a directory
 * a later layout removes fails on its own emptiness check rather than on an
 * ENOENT from the walk. Pair the call with a case that asserts the walk found
 * something, or the guard passes vacuously the day its root moves.
 */
export function collectFiles(dir: string, options: CollectFilesOptions): string[] {
  if (!existsSync(dir))
    return []
  const skip = options.alsoSkip === undefined
    ? SKIP_DIRS
    : new Set([...SKIP_DIRS, ...options.alsoSkip])
  return walk(dir, options.matches, skip)
}

/** The recursive half, with the skip set merged one time by the caller. */
function walk(dir: string, matches: (basename: string) => boolean, skip: ReadonlySet<string>): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (skip.has(entry.name))
      continue
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...walk(full, matches, skip))
    else if (matches(entry.name))
      found.push(full)
  }
  return found
}
