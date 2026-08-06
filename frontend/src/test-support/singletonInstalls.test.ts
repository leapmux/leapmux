import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { installedCopies } from '~/test-support/installedCopies'

// Every entry in package.json's `overrides` is there to collapse a package to
// one installed copy, and each one breaks differently when it doesn't. Nothing
// else fails when an entry is dropped -- not until two versions happen to
// diverge, at which point the error names only the symptom.
//
// Listed explicitly rather than derived from `Object.keys(overrides)`: an
// override declares a version spec, so a future floor bump or CVE pin would
// silently acquire a copy-count assertion nobody meant to make, and would get a
// generic failure message where these can each prescribe the actual remedy.
const SINGLETONS = [
  {
    name: '@types/hast',
    // The markdown pipeline threads one hast tree through packages that each
    // depend on `@types/hast` under their own range: remark-rehype and
    // mdast-util-to-hast build it, @shikijs/rehype annotates it,
    // rehype-stringify and hast-util-to-html serialize it. Two copies declare
    // structurally DIFFERENT `Root` types (3.0.5 replaced the loose
    // `Properties` record with per-attribute types), and `tsc` rejects passing
    // one pipeline's tree to another with a wall of "Root is not assignable to
    // Root". shiki 4.4 asking for `^3.0.5` while everyone else asked for
    // `^3.0.0` was enough to make bun nest a second copy.
    why: 'the markdown pipeline\'s Root types will not be assignable to each other',
    remedy: 'Restore the `overrides["@types/hast"]` entry in package.json and re-run `bun install --force`',
  },
  {
    name: 'vite',
    // vinxi 0.5.11 declares `vite: ^6.4.1` while the root is on 8.x, so without
    // the override bun nests a second Vite. Two Vites means two plugin
    // registries and two module graphs: the vanilla-extract and solid plugins
    // load into one while the dev server runs the other, and styles or HMR go
    // missing with no error naming the cause.
    why: 'plugins and the dev server would run against different Vite instances',
    remedy: 'Restore the `overrides["vite"]` entry in package.json and re-run `bun install --force`',
  },
]

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

describe('singleton installs', () => {
  for (const { name, why, remedy } of SINGLETONS) {
    it(`resolves ${name} to exactly one copy`, () => {
      const copies = installedCopies(name)

      // `toHaveLength(1)` fails in both directions, and the two mean opposite
      // things. At zero the "restore the override" remedy is actively wrong --
      // the override is still there; the package is gone, or the walker cannot
      // see the install layout -- so say that instead of sending the reader to
      // re-add something that is already present.
      expect(
        copies,
        copies.length === 0
          ? `No ${name} install found at all. Either it left the dependency tree (drop the overrides entry and this guard), or node_modules is not installed / not in a hoisted layout the walker understands.`
          : `Multiple ${name} installs -- ${why}. ${remedy}:\n  ${copies.join('\n  ')}`,
      ).toHaveLength(1)
    })
  }

  it('keeps an override entry for every package guarded here', () => {
    // The guards above only hold while the overrides that produce them exist.
    // Without this, deleting an override and its guard together would look
    // like a clean removal rather than the regression it is.
    const overrides = JSON.parse(readFileSync(join(frontendRoot, 'package.json'), 'utf-8')).overrides ?? {}

    for (const { name } of SINGLETONS) {
      expect(overrides, `package.json overrides no longer pins ${name}`).toHaveProperty([name])
    }
  })

  it('applies every patch in patchedDependencies to the version installed', () => {
    // `patchedDependencies` keys on an exact `name@version`. When the resolved
    // version moves past the key, bun applies nothing and says nothing --
    // verified against bun 1.3.14: a patch keyed at 3.0.0 against a resolved
    // 3.0.1 installs clean, exit 0, no warning, patch absent.
    //
    // That is silent for `vinxi@0.5.11`, whose patch teaches viteManifestPath
    // to recognise Vite 8. Losing it does not fail a build: it moves the
    // manifest lookup back to the pre-Vite-5 path, so asset preloading stops
    // matching `window.manifest` keys in the PRODUCTION bundle only. The
    // dependency is pinned exactly for the same reason, and this catches the
    // other direction -- a deliberate bump that forgets to re-key the patch.
    const pkg = JSON.parse(readFileSync(join(frontendRoot, 'package.json'), 'utf-8'))
    const patched = pkg.patchedDependencies ?? {}

    expect(Object.keys(patched).length, 'no patchedDependencies left to guard').toBeGreaterThan(0)

    for (const key of Object.keys(patched)) {
      const at = key.lastIndexOf('@')
      const name = key.slice(0, at)
      const wanted = key.slice(at + 1)
      const [dir] = installedCopies(name)

      expect(dir, `patchedDependencies names ${name}, which is not installed`).toBeTruthy()

      const installed = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf-8')).version
      expect(
        installed,
        `patchedDependencies is keyed at ${key} but ${name} resolved to ${installed}, `
        + `so ${patched[key]} is silently not applied. Re-key the patch and re-pin the dependency.`,
      ).toBe(wanted)
    }
  })
})
