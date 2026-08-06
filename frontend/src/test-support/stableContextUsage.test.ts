import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { installedCopies } from '~/test-support/installedCopies'

// Guards the two rules `createStableContext` runs on, both of which were
// documented in a comment and enforced by nothing.
//
// 1. Every context goes through the helper. Solid's `createContext` mints a
//    fresh id per call, so a module that HMR re-evaluates hands out a context
//    its already-mounted Provider never wrote to -- and the consumer's guard
//    throws "useX must be used within XProvider" with nobody having touched the
//    app. The helper pins identity under a stable key; a raw call opts straight
//    back out of that, and the next context added is the one that does it.
//
// 2. Keys are unique app-wide. They are hand-typed path-shaped strings, so a
//    copy-pasted module that forgets to change its key silently SHARES the
//    other module's context: the second Provider writes its own value shape
//    under the first module's id, both sides type-check, and the failure
//    surfaces as a TypeError on an unrelated field deep in a component.
//
// Same shape as `noMirroredUnitTests.test.ts`: a source scan, because the thing
// being guarded is a property of the source tree rather than of any runtime.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')
const helperPath = join(srcRoot, 'lib', 'createStableContext.ts')

function collectSourceFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...collectSourceFiles(full))
    else if (/\.tsx?$/.test(entry.name) && !/\.(?:test|spec)\.tsx?$/.test(entry.name))
      found.push(full)
  }
  return found
}

const sourceFiles = collectSourceFiles(srcRoot)

describe('createStableContext usage', () => {
  it('is the only way a context is created', () => {
    const offenders = sourceFiles
      // The helper is where the one legitimate `createContext` call lives.
      .filter(file => file !== helperPath)
      .filter(file => /\bcreateContext\b/.test(readFileSync(file, 'utf8')))
      .map(file => relative(frontendRoot, file))

    expect(
      offenders,
      `Use \`createStableContext\` from \`~/lib/createStableContext\` instead of Solid's `
      + `\`createContext\`, so the context survives an HMR module re-evaluation:\n  `
      + `${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('gives every context a unique key', () => {
    const byKey = new Map<string, string[]>()
    for (const file of sourceFiles) {
      const source = readFileSync(file, 'utf8')
      for (const [, key] of source.matchAll(/createStableContext(?:\s*<[^>]*>)?\s*\(\s*'([^']+)'/g)) {
        const owners = byKey.get(key) ?? []
        owners.push(relative(frontendRoot, file))
        byKey.set(key, owners)
      }
    }

    // Sanity: a regex that matched nothing would make the assertion below pass
    // for the wrong reason, quietly retiring the guard.
    expect(byKey.size, 'the scan found no createStableContext keys at all').toBeGreaterThan(0)

    const duplicated = [...byKey.entries()]
      .filter(([, owners]) => owners.length > 1)
      .map(([key, owners]) => `${key} (${owners.join(', ')})`)

    expect(
      duplicated,
      `Two modules share a createStableContext key and therefore share ONE context. `
      + `Name the key after the owning module's path:\n  ${duplicated.join('\n  ')}`,
    ).toEqual([])
  })

  // `solid-refresh` is a direct devDependency purely so `createStableContext`'s
  // HMR reproduction can drive the real runtime. The copy the DEV SERVER runs is
  // whatever `vite-plugin-solid` resolves, so one behaviour is modelled by two
  // independently-floating ranges. They dedupe to a single hoisted install
  // today; the day a plugin bump moves to a major the direct range no longer
  // covers, the package manager installs a NESTED second copy and that test
  // starts exercising a patch ordering the dev server has stopped running --
  // silently, and still green.
  it('resolves one shared solid-refresh, not a second nested copy', () => {
    const copies = installedCopies('solid-refresh').map(p => relative(frontendRoot, p))

    // Zero is a different problem from two, and the bump advice does not apply
    // to it: nothing to align ranges with if the package is not there.
    expect(
      copies,
      copies.length === 0
        ? 'No `solid-refresh` install found at all -- either it left the dependency tree, or node_modules is not installed.'
        : `Bump \`solid-refresh\` in package.json to match vite-plugin-solid's range, `
          + `so the HMR test and the dev server run the same runtime:\n  ${copies.join('\n  ')}`,
    ).toHaveLength(1)
  })
})
