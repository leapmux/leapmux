import type { GitFileStatusEntry } from '~/generated/proto/leapmux/v1/common_pb'
import type { PathFlavor } from '~/lib/paths'
import { join, lastSepIndex, sep, trimLastSegment } from '~/lib/paths'
import { isUntrackedDirEntry, untrackedDirBasePath } from '~/stores/repoGit.store'

/**
 * The two sets a filtered tree needs: `paths` holds every entry the filter
 * selected plus the ancestor directories needed to reach it, and `subtrees`
 * holds the entries whose WHOLE contents are selected.
 *
 * The split matters. Everything must match EXACTLY, or a file merely sitting
 * next to a changed one matches through their shared parent -- which it did,
 * all the way up to the repo root, so "Changed" listed every unchanged file at
 * the top level. Only an untracked directory may match through an ancestor.
 */
export interface GitVisibility {
  paths: Set<string>
  subtrees: Set<string>
}

/**
 * Builds the visibility sets for a filter's selection.
 *
 * A pure function of (files, root, flavor) so it can be tested directly. It is
 * the PRODUCER half of the filter; `isPathVisible` is the consumer, and it was
 * their disagreement about what belongs in `subtrees` that made "Changed" list
 * unchanged files.
 */
export function computeGitVisibility(
  files: readonly GitFileStatusEntry[],
  root: string,
  flavor: PathFlavor,
): GitVisibility {
  const paths = new Set<string>()
  const subtrees = new Set<string>()
  // The root itself is always shown, so the tree has something to render.
  // It is deliberately NOT a subtree root: making it one would show every
  // file in the repo, which is the leak this split exists to close.
  paths.add(root)

  const s = sep(flavor)
  for (const f of files) {
    const isSubtree = isUntrackedDirEntry(f.path)
    const relPath = untrackedDirBasePath(f.path)
    if (!relPath)
      continue
    let dir = join([root, relPath], flavor)
    paths.add(dir)
    if (isSubtree)
      subtrees.add(dir)
    while (true) {
      dir = trimLastSegment(dir, s)
      if (!dir || dir === root)
        break
      // Ancestor already seen via another file — skip the rest of the walk.
      if (paths.has(dir))
        break
      paths.add(dir)
    }
  }

  return { paths, subtrees }
}

/**
 * Whether a filtered tree shows `path`.
 *
 * `visible` holds the exact paths the filter selected -- each changed file plus
 * every ancestor directory needed to reach it. `subtrees` holds the ones whose
 * WHOLE contents are selected: git reports an untracked directory as a single
 * "build/" entry, so `build/bin/app` has to match through its ancestor even
 * though git never named it.
 *
 * Two sets, not one, because the ancestor walk used to run against `visible`
 * alone -- and `visible` contains the repo root. Every file directly under the
 * root therefore matched through it, so "Changed" showed the unchanged files
 * sitting next to a changed one, and the same leak applied inside any directory
 * that contained a change. Only a subtree root may match through an ancestor.
 */
export function isPathVisible(
  path: string,
  visible: ReadonlySet<string>,
  subtrees: ReadonlySet<string>,
  flavor: PathFlavor,
): boolean {
  if (visible.has(path))
    return true
  let dir = path
  while (true) {
    const i = lastSepIndex(dir, flavor)
    if (i <= 0)
      return false
    dir = dir.substring(0, i)
    if (subtrees.has(dir))
      return true
  }
}

/**
 * Builds the single predicate a filtered `DirectoryTree` consumes.
 *
 * The tree takes ONE optional `isVisible` rather than the two sets plus a
 * flavor: the halves are only ever meaningful together, and passing them
 * separately let a caller supply one without the other (and made "is this tree
 * filtered at all?" a question about which of two props was set). It also keeps
 * git's untracked-subtree semantics here, in the git-aware section, instead of
 * inside the generic directory tree.
 */
export function makeGitVisibilityPredicate(
  visibility: GitVisibility,
  flavor: PathFlavor,
): (path: string) => boolean {
  return path => isPathVisible(path, visibility.paths, visibility.subtrees, flavor)
}

/**
 * The absolute path a flat-list git entry opens as a file tab, or undefined
 * when it is not a file at all.
 *
 * Git lists a whole untracked DIRECTORY as one "build/" entry, right beside the
 * files. Opening that as a FILE tab produced a permanently broken editor: the
 * worker answers ReadFile with "path is a directory", so the tab renders an
 * error and never recovers. There is nothing to open -- the tree view is where
 * a directory is browsable.
 *
 * `isDir` covers the directory that carries NO trailing slash: a submodule.
 * Git names it like any other changed path, so the slash alone never caught it
 * and its row opened the same broken tab.
 */
export function flatEntryOpenTarget(
  entry: { path: string, isDir: boolean },
  root: string,
  flavor: PathFlavor,
): string | undefined {
  if (entry.isDir || isUntrackedDirEntry(entry.path))
    return undefined
  return join([root, entry.path], flavor)
}
