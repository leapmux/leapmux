import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

// Build-configuration guard: the `~` mapping has THREE consumers, and each one
// learns it from a different place.
//
//   - tsc reads `paths` in tsconfig.json.
//   - vitest reads the same `paths`, through Vite's `resolve.tsconfigPaths`
//     option in vitest.config.ts (this replaced vite-tsconfig-paths, which Vite
//     8 deprecates -- it warns once per router, so three lines per dev start).
//   - the vinxi build gets `~` -> `<cwd>/src` from @solidjs/start, which
//     HARDCODES that alias from its `appRoot` option and never reads tsconfig.
//     An alias wins over `resolve.tsconfigPaths`, so under vinxi the option in
//     app.config.ts is what a SECOND `paths` entry would resolve through, and
//     `~` itself resolves through SolidStart.
//
// Nothing connects the three. A `paths` edit that moves `~` off `src` keeps tsc
// and vitest agreeing with each other and silently disagreeing with the build,
// where `~` still points at `src`. The cases below pin the mapping that all
// three currently share, and the behavior that proves it resolves.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

function readTsconfig(): { paths?: Record<string, string[]> } {
  const path = join(frontendRoot, 'tsconfig.json')
  // tsconfig.json carries `//` comments, so JSON.parse rejects it. The
  // TypeScript API is the parser that defines the format.
  const { config, error } = ts.readConfigFile(path, p => readFileSync(p, 'utf-8'))
  expect(error, `tsconfig.json did not parse: ${error?.messageText}`).toBeUndefined()
  return config.compilerOptions ?? {}
}

describe('tsconfig path mapping', () => {
  it('resolves ~/ and a relative specifier to one module instance', async () => {
    const viaMapping = await import('~/lib/clamp')
    const viaRelative = await import('../lib/clamp')

    // Function identity, not namespace identity: the module runner can hand out
    // a fresh namespace object per import call, but one module instance has one
    // `clamp`. A `~` that pointed at a COPY of src (a stale build output, a
    // second checkout) would load a second instance and give two functions --
    // which is how module-level singletons silently double in this codebase.
    expect(
      viaMapping.clamp,
      '`~/lib/clamp` and `../lib/clamp` loaded different module instances, so `~` does not point at frontend/src.',
    ).toBe(viaRelative.clamp)
  })

  it('maps ~/* onto ./src/*, which is where SolidStart also points it', () => {
    const { paths } = readTsconfig()

    expect(
      paths,
      'tsconfig.json `paths` must map `~/*` to `./src/*`. @solidjs/start hardcodes its own '
      + '`~` -> `<cwd>/src` alias for the vinxi build and never reads this file, so any other '
      + 'target here type-checks and tests green while the build still resolves `~` to src.\n'
      + 'Adding a SECOND mapping is fine -- it resolves through `resolve.tsconfigPaths` in every '
      + 'consumer -- but extend this guard so the new entry is covered.',
    ).toEqual({ '~/*': ['./src/*'] })
  })

  it('keeps resolve.tsconfigPaths and drops the deprecated plugin', () => {
    const appConfig = readFileSync(join(frontendRoot, 'app.config.ts'), 'utf-8')
    const vitestConfig = readFileSync(join(frontendRoot, 'vitest.config.ts'), 'utf-8')
    const pkg = JSON.parse(readFileSync(join(frontendRoot, 'package.json'), 'utf-8'))

    // app.config.ts is the one that looks deletable: SolidStart's alias already
    // covers `~`, so removing the option changes nothing until someone adds a
    // second `paths` entry, which then resolves for tsc and vitest and fails
    // the build only.
    expect(
      appConfig,
      'app.config.ts must keep `resolve.tsconfigPaths: true`. It looks redundant because '
      + 'SolidStart\'s alias already resolves `~`, but it is what any other `paths` entry resolves through.',
    ).toContain('tsconfigPaths: true')
    expect(vitestConfig, 'vitest.config.ts must keep `resolve.tsconfigPaths: true`').toContain('tsconfigPaths: true')

    // The plugin and the native option do the same job, and Vite 8 warns once
    // per router when it finds the plugin. Re-adding it puts those lines back.
    const declared = { ...pkg.dependencies, ...pkg.devDependencies }
    // Without this, a package.json that lost both blocks (or a shape change)
    // would leave `declared` empty and the absence check below would pass while
    // testing nothing.
    expect(declared, 'package.json declares no dependencies at all').toHaveProperty(['vite'])
    expect(
      declared,
      'vite-tsconfig-paths is redundant on Vite 8 and makes it print a deprecation warning '
      + 'once per vinxi router. Use `resolve.tsconfigPaths: true` instead.',
    ).not.toHaveProperty(['vite-tsconfig-paths'])
    for (const [name, source] of [['app.config.ts', appConfig], ['vitest.config.ts', vitestConfig]] as const)
      expect(source, `${name} must not import vite-tsconfig-paths`).not.toContain('vite-tsconfig-paths')
  })
})
