import { existsSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { toPosixSeparators } from '~/lib/paths'

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

/**
 * Absolute path of the frontend package root.
 *
 * Repo guards compare walk results against this directory. A file under
 * `src/test-support/` used to recompute it with `../..` from
 * `import.meta.url`. A file under `src/components/` used `../../..`.
 * One hop that is wrong silently drops a file from a guard. This module
 * sits in `src/test-support/`, so the hop lives here and nowhere else.
 */
export const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** The empty default for `skipPaths`: no location is exempt. */
const NO_PATHS: ReadonlySet<string> = new Set()

export interface CollectFilesOptions {
  /** Reports whether a file belongs in the result, from its basename. */
  matches: (basename: string) => boolean
  /** Directory basenames to skip in ADDITION to `SKIP_DIRS`, at ANY depth. */
  alsoSkip?: ReadonlySet<string>
  /**
   * Directory paths to skip, each relative to `dir` and matched EXACTLY.
   *
   * Use this, not `alsoSkip`, for an exemption that names one sanctioned
   * location. A basename skip exempts every directory with that name at
   * every depth: `alsoSkip: new Set(['e2e'])` exempted `tests/e2e/` as
   * intended and also `tests/unit/e2e/`, so the guard that keeps the
   * retired unit mirror out could be defeated by one directory name.
   */
  skipPaths?: ReadonlySet<string>
}

/**
 * `path.relative(from, to)` with `/` separators on every platform.
 *
 * Repo guards compare against POSIX literals (`src/app.tsx`). On Windows,
 * `path.relative` returns backslashes, so those comparisons fail even when
 * the walk found the right file. An allow-list keyed by slash paths never
 * matches. `skipPaths` already uses `/`. This is that convention for a
 * path the walk did not produce.
 */
export function posixRelative(from: string, to: string): string {
  return toPosixSeparators(relative(from, to))
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
  return walk(dir, options.matches, skip, options.skipPaths ?? NO_PATHS, '')
}

/**
 * The recursive half, with the skip set merged one time by the caller.
 *
 * `rel` is the current directory's path relative to the walk root, so a
 * `skipPaths` entry names one location instead of every directory that
 * shares its basename.
 */
function walk(
  dir: string,
  matches: (basename: string) => boolean,
  skip: ReadonlySet<string>,
  skipPaths: ReadonlySet<string>,
  rel: string,
): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (skip.has(entry.name))
      continue
    const entryRel = rel === '' ? entry.name : `${rel}/${entry.name}`
    if (skipPaths.has(entryRel))
      continue
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...walk(full, matches, skip, skipPaths, entryRel))
    else if (matches(entry.name))
      found.push(full)
  }
  return found
}
