import { createRequire } from 'node:module'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'
import solid from 'vite-plugin-solid'
import { defineConfig } from 'vitest/config'

const require = createRequire(import.meta.url)

// vite-plugin-solid aliases `solid-refresh` -> the virtual id `/@solid-refresh`
// unconditionally -- even under `hot: false`, which only stops the babel HMR
// injection into components. So a direct import of `solid-refresh` (here,
// createStableContext's HMR reproduction) resolves to `/@solid-refresh`, a
// non-file path. On Windows vitest's `convertIdToImportUrl` turns that into
// `file:///@solid-refresh`, which Node's `fileURLToPath` rejects with
// "The argument 'filename' must be a file URL object, file URL string, or
// absolute path string" -- the Windows-only failure introduced by #347.
// POSIX round-trips the virtual id harmlessly, which is why the suite stays
// green on macOS/Linux.
//
// Re-resolve the virtual id to the real runtime file so the module id is a
// proper OS path on every platform. A user alias can't win here (Vite's
// mergeAlias puts the plugin's regex alias first, and the alias plugin runs
// before user pre-plugins), but vite-plugin-solid's own resolveId is itself
// `enforce: 'pre'`, and a pre-plugin registered ahead of it runs first in the
// same group -- first non-null resolveId result wins. Harmless to the dev
// server: this config is test-only, and the dev server resolves `/@solid-refresh`
// through the plugin's load hook before this resolution would be consulted.
const resolveSolidRefreshVirtual = {
  name: 'resolve-solid-refresh-to-file',
  enforce: 'pre' as const,
  resolveId(id: string) {
    if (id === '/@solid-refresh')
      return require.resolve('solid-refresh/dist/solid-refresh.mjs')
  },
}

export default defineConfig({
  resolve: {
    // Supplies the `~` mapping from tsconfig.json's `paths`, so it is declared
    // once for tsc, Vite and vitest rather than three times.
    tsconfigPaths: true,
  },
  // hot: false — HMR-runtime injection (/@solid-refresh) breaks fileURLToPath
  // on Windows and tests don't need it.
  plugins: [vanillaExtractPlugin(), resolveSolidRefreshVirtual, solid({ hot: false })],
  test: {
    // Worker threads rather than the default forked processes: this suite is
    // ~450 files of pure CPU (transform, module evaluation, environment
    // construction), and threads skip the per-file process spawn and IPC
    // serialization that forks pay for. Measured at ~10% of total wall time.
    //
    // Isolation stays ON. Turning it off is another ~60% on top, but the
    // suite's per-file `vi.mock` registrations do not survive a shared module
    // registry, so it trades the whole point of the tests for the time.
    pool: 'threads',
    // jsdom, not happy-dom. happy-dom constructs an environment ~2.7x faster
    // (worth ~45% of this suite), but its computed-style defaults are empty
    // strings where a browser and jsdom both report initial values --
    // `getComputedStyle(el).overflowX` is `''`, not `'visible'`. Tooltip's
    // clip detection reads exactly that, so under happy-dom every element
    // looks like it clips and the "not clipped" branch stops being reachable
    // from a test. Speed is not worth a DOM that answers differently from the
    // one the code ships against.
    environment: 'jsdom',
    globals: true,
    exclude: ['tests/e2e/**', 'node_modules/**'],
    setupFiles: ['./vitest.setup.ts'],
  },
})
