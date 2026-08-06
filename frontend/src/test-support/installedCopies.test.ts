import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { installedCopies } from '~/test-support/installedCopies'

// The guards built on this helper assert `toHaveLength(1)`, so a helper that
// under- or over-reports makes them pass (or fail) for the wrong reason and
// quietly retires itself.
//
// These run against a synthetic tree rather than frontend/node_modules: the
// recursion depth and match rules are properties of the HELPER, and pinning
// them to whatever the current lockfile happened to hoist means an unrelated
// dependency bump fails this file with a message accusing correct code. Only
// the last case touches the real tree, and only to confirm the helper is
// pointed at it at all.
describe('installedCopies', () => {
  let root: string
  let nodeModules: string

  beforeAll(() => {
    root = mkdtempSync(join(tmpdir(), 'installed-copies-'))
    nodeModules = join(root, 'node_modules')

    const pkg = (...segments: string[]) => {
      const dir = join(nodeModules, ...segments)
      mkdirSync(dir, { recursive: true })
      writeFileSync(join(dir, 'package.json'), '{}')
      return dir
    }

    pkg('target')
    pkg('@scope', 'target')
    pkg('not-target')
    // A copy nested under a PLAIN-named package -- the case a scan that only
    // descends into `node_modules`/`@scope` directories can never reach.
    pkg('plain-pkg', 'node_modules', 'target')
    pkg('plain-pkg', 'node_modules', '@scope', 'target')
    // Tool state that is not an install. `.store` mirrors the shape that makes
    // this matter: a dot-directory with its own node_modules inside it (pnpm's
    // `.pnpm`, bun's `.bun`), which a scan that skips only `.bin` walks into
    // and counts as real installs.
    pkg('.store', 'node_modules', 'target')
    mkdirSync(join(nodeModules, '.bin'), { recursive: true })
  })

  afterAll(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('finds a package installed at the top level', () => {
    expect(installedCopies('not-target', nodeModules)).toHaveLength(1)
  })

  // Traversal order is depth-first in readdir order -- an incidental property
  // no caller depends on, so compare the set rather than pinning it.
  const copiesOf = (name: string) => installedCopies(name, nodeModules).sort()

  it('descends into a package\'s own node_modules, not just scopes', () => {
    expect(copiesOf('target')).toEqual([
      join(nodeModules, 'plain-pkg', 'node_modules', 'target'),
      join(nodeModules, 'target'),
    ].sort())
  })

  it('finds a scoped package by its full name, nested copies included', () => {
    expect(copiesOf('@scope/target')).toEqual([
      join(nodeModules, '@scope', 'target'),
      join(nodeModules, 'plain-pkg', 'node_modules', '@scope', 'target'),
    ].sort())
  })

  it('does not match a package whose name merely ends with the query', () => {
    // `target` alone must not collect `@scope/target` or `not-target`.
    expect(installedCopies('target', nodeModules).every(d => d.endsWith(`${'node_modules'}/target`))).toBe(true)
    expect(installedCopies('scope/target', nodeModules)).toHaveLength(0)
  })

  it('ignores dot-directories, which hold tool state rather than installs', () => {
    // `.store/node_modules/target` exists on disk, so a scan that walks into
    // dot-directories reports three copies of `target` instead of two and
    // fails every singleton guard on installs that are not installs.
    const copies = copiesOf('target')
    expect(copies.some(d => d.includes('.store')), `walked into a dot-directory: ${copies.join(', ')}`).toBe(false)
    expect(copies).toHaveLength(2)
  })

  it('returns nothing for a package that is not installed', () => {
    expect(installedCopies('definitely-not-installed', nodeModules)).toHaveLength(0)
  })

  it('counts a symlinked package as an installed copy', () => {
    // bun's isolated linker, pnpm-style stores and `bun link` all install by
    // symlink. `readdirSync(…, {withFileTypes:true})` reports isDirectory()
    // false for one, so a scan that checks only that misses it -- and a
    // `toHaveLength(1)` guard then passes with two copies on disk.
    const linkRoot = mkdtempSync(join(tmpdir(), 'installed-copies-link-'))
    try {
      const links = join(linkRoot, 'node_modules')
      mkdirSync(links, { recursive: true })
      symlinkSync(join(nodeModules, 'target'), join(links, 'target'), 'dir')

      expect(installedCopies('target', links)).toHaveLength(1)
    }
    finally {
      rmSync(linkRoot, { recursive: true, force: true })
    }
  })

  it('reports a self-referential symlink once instead of once per level', () => {
    // Without a visited-set the walk re-enters through the link at every level
    // until the OS refuses the path with ELOOP, reporting ~30 copies of a
    // single install and failing a singleton guard on a duplicate that does
    // not exist.
    const cycleRoot = mkdtempSync(join(tmpdir(), 'installed-copies-cycle-'))
    try {
      const cycle = join(cycleRoot, 'node_modules')
      mkdirSync(join(cycle, 'looper'), { recursive: true })
      symlinkSync(cycle, join(cycle, 'looper', 'node_modules'), 'dir')

      expect(installedCopies('looper', cycle)).toHaveLength(1)
    }
    finally {
      rmSync(cycleRoot, { recursive: true, force: true })
    }
  })

  it('is pointed at frontend/node_modules by default', () => {
    // The only case that touches the real tree: it proves `frontendRoot` still
    // resolves somewhere with packages in it, without asserting how many
    // copies of anything the current lockfile produced.
    expect(installedCopies('solid-js')).not.toHaveLength(0)
  })
})
