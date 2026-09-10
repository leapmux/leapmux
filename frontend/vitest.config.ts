import { createRequire } from 'node:module'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'
import solid from 'vite-plugin-solid'
import { defineConfig } from 'vitest/config'
import { NODE_TEST_FILES } from './vitest.node.ts'

const require = createRequire(import.meta.url)

// vite-plugin-solid aliases solid-refresh to /@solid-refresh even with hot:false.
// That option stops Babel hot-module-replacement (HMR) injection only.
// A direct runtime import still reaches this virtual ID. On Windows, conversion to file:///@solid-refresh fails.
// POSIX conversion accepts the same ID, so only Windows exposed the failure in #347.
//
// Resolve the virtual ID to the installed runtime file on every platform.
// A user alias cannot override the plugin alias, which Vite puts first.
// Register this pre-plugin before Solid's pre-plugin. The first non-null resolveId result wins.
// This configuration applies only to tests. The development server keeps Solid's virtual load handler.
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
    // Threads avoid the cost of one process per file. Keep isolation for per-file mocks.
    pool: 'threads',
    globals: true,
    // Playwright owns .spec.ts. Keep co-located .test.ts files in Vitest.
    exclude: ['tests/e2e/**/*.spec.ts', 'node_modules/**'],
    projects: [
      {
        // Solid's plugin adds browser conditions and DOM matchers. Node tests need neither.
        resolve: { tsconfigPaths: true },
        test: {
          name: 'node',
          environment: 'node',
          globals: true,
          pool: 'threads',
          include: NODE_TEST_FILES,
        },
      },
      {
        extends: true,
        test: {
          name: 'dom',
          // jsdom supplies browser computed-style defaults that happy-dom omits.
          // Tooltip clipping tests depend on those defaults.
          environment: 'jsdom',
          exclude: NODE_TEST_FILES,
          // Dexie reads IDBKeyRange during import. Install it before the storage setup.
          setupFiles: ['./vitest.idbKeyRange.ts', './vitest.setup.ts'],
        },
      },
    ],
  },
})
