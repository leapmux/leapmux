// Locate every installed copy of a package under frontend/node_modules,
// including nested ones a package manager creates when two dependents ask for
// ranges it declines to dedupe.
//
// A second nested copy is the quiet failure mode behind a whole class of bugs:
// two instances of a runtime that is only correct as a singleton, or -- for a
// `@types/*` package -- two structurally distinct declarations of the same type,
// which `tsc` reports as an unassignable-but-identical-looking mismatch. Tests
// that care assert `toHaveLength(1)`.

import { existsSync, readdirSync, realpathSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Two levels up from src/test-support/. Moving this file without updating the
// hops would silently retarget every guard at a tree with no packages in it,
// which reads as "zero copies" rather than as an error -- so the suite keeps a
// case asserting a known-installed package is still found.
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

/**
 * The packages installed directly inside one `node_modules`, as
 * `[packageName, directory]` pairs. Scope directories (`@types`) are expanded
 * to the packages they hold rather than reported as packages themselves, so
 * every name is the one a caller would write in package.json.
 */
function packagesIn(nodeModules: string): Array<[string, string]> {
  const packages: Array<[string, string]> = []
  for (const entry of readdirSync(nodeModules, { withFileTypes: true })) {
    // A symlinked package is a real install: `bun install --linker=isolated`,
    // a pnpm-style store, and `bun link` all produce them. `withFileTypes`
    // classifies without following, so isDirectory() alone reports false for
    // one and the copy would be invisible to a `toHaveLength(1)` guard --
    // which is the guard passing while the bug it exists to catch is live.
    if (!entry.isDirectory() && !entry.isSymbolicLink())
      continue
    // Skip every dot-entry, not just `.bin`: `.cache`, `.vite`, `.vinxi` and
    // friends are tool state, and `.pnpm` genuinely holds package directories
    // that would otherwise be counted as installs.
    if (entry.name.startsWith('.'))
      continue
    const full = join(nodeModules, entry.name)
    if (entry.name.startsWith('@')) {
      for (const scoped of readdirSync(full, { withFileTypes: true })) {
        if (scoped.isDirectory() || scoped.isSymbolicLink())
          packages.push([`${entry.name}/${scoped.name}`, join(full, scoped.name)])
      }
    }
    else {
      packages.push([entry.name, full])
    }
  }
  return packages
}

/**
 * Walk `nodeModules` and every package's own nested `node_modules`, calling
 * `visit` with each `[packageName, directory]` pair found.
 *
 * `seen` holds resolved real paths. Without it a symlink that points back up
 * the tree is walked once per level until the OS refuses the path with ELOOP,
 * reporting the same install ~30 times -- so a `toHaveLength(1)` guard fails
 * on a duplicate that does not exist.
 */
function walk(nodeModules: string, visit: (name: string, dir: string) => void, seen: Set<string>): void {
  let real: string
  try {
    real = realpathSync(nodeModules)
  }
  catch {
    return // A broken link, or a path the OS gave up on.
  }
  if (seen.has(real))
    return
  seen.add(real)

  for (const [name, dir] of packagesIn(nodeModules)) {
    visit(name, dir)
    // Any package may carry its own node_modules, not just scope and
    // node_modules directories -- that is exactly where an undeduped copy
    // hides, so recurse through all of them.
    const nested = join(dir, 'node_modules')
    if (existsSync(nested))
      walk(nested, visit, seen)
  }
}

/**
 * `packageName -> every directory holding an install of it`, built by one
 * traversal.
 *
 * Memoized per module instance. Vitest isolates each test file's module
 * registry, so this is shared across the calls within one file -- which is
 * where the repetition is: the helper's own suite queries five names, and a
 * full walk is ~1400 filesystem syscalls.
 */
let index: Map<string, string[]> | null = null

function indexOf(nodeModules: string): Map<string, string[]> {
  const found = new Map<string, string[]>()
  walk(nodeModules, (name, dir) => {
    const dirs = found.get(name)
    if (dirs)
      dirs.push(dir)
    else
      found.set(name, [dir])
  }, new Set())
  return found
}

/**
 * Every directory under `frontend/node_modules` holding an install of
 * `packageName`, nested copies included.
 *
 * Accepts scoped names (`@types/hast`) as well as bare ones (`solid-refresh`),
 * and compares the whole name so a bare `hast` never counts as a copy of
 * `@types/hast`.
 *
 * `nodeModules` overrides the tree to scan; the helper's own tests point it at
 * a synthetic fixture so they pin this function's behavior rather than
 * whatever the current lockfile happened to hoist.
 */
export function installedCopies(packageName: string, nodeModules?: string): string[] {
  if (nodeModules !== undefined)
    return indexOf(nodeModules).get(packageName) ?? []

  index ??= indexOf(join(frontendRoot, 'node_modules'))
  return index.get(packageName) ?? []
}
