import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'
import solid from 'vite-plugin-solid'
import { defineConfig } from 'vitest/config'
import { VITE_SOURCE_DIRECTORIES } from './viteAssetAccess.ts'
import { NODE_TEST_FILES } from './vitest.node.ts'

const require = createRequire(import.meta.url)

// vite-plugin-solid aliases solid-refresh to /@solid-refresh even with hot:false.
// That option stops Babel hot-module-replacement (HMR) injection only.
// A direct runtime import still reaches this virtual ID. On Windows, conversion to file:///@solid-refresh fails.
// POSIX conversion accepts the same ID. Only Windows exposed the failure in #347.
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
    // The next plugin resolves every other ID.
    return undefined
  },
}

export default defineConfig({
  // Native conformance fixtures require raw reads when JSON transforms reject valid UTF-16 units.
  server: { fs: { allow: [...VITE_SOURCE_DIRECTORIES, fileURLToPath(new URL('../testdata', import.meta.url))] } },
  resolve: {
    // tsconfig.json supplies one `~` mapping for the compiler and both runners.
    tsconfigPaths: true,
  },
  // Tests require no HMR injection. Its /@solid-refresh ID breaks fileURLToPath on Windows.
  plugins: [vanillaExtractPlugin(), resolveSolidRefreshVirtual, solid({ hot: false })],
  test: {
    // Threads avoid the cost of one process per file. Keep isolation for per-file mocks.
    pool: 'threads',
    globals: true,
    // One budget for every test, so no test states its own: subprocess and
    // server-driving tests need far more than the runner's 5s default, above
    // all on the two-core Windows CI runner.
    testTimeout: 30_000,
    // Show a test's captured stdout and stderr only when it fails. Passing
    // tests' console noise flooded the CI logs and buried the failures.
    silent: 'passed-only',
    // Solid's dev mode warns whenever a computation or a cleanup is created
    // outside a root. Many tests construct stores outside a root on purpose
    // (vitest.setup.ts owns the roots it needs), and the two warnings then
    // repeat for nearly every dom test -- thousands of lines per CI log that
    // no assertion reads. Drop exactly these two lines and keep every other
    // console line visible.
    onConsoleLog(log) {
      const solidDevNoise = [
        'computations created outside a `createRoot` or `render` will never be disposed',
        'cleanups created outside a `createRoot` or `render` will never be run',
      ]
      return solidDevNoise.some(message => log.includes(message)) ? false : undefined
    },
    // Playwright owns .spec.ts. Keep co-located .test.ts files in Vitest.
    exclude: ['tests/e2e/**/*.spec.ts', 'node_modules/**'],
    projects: [
      {
        // Solid adds browser conditions and DOM matchers. Node tests require neither.
        resolve: { tsconfigPaths: true },
        test: {
          name: 'node',
          environment: 'node',
          globals: true,
          pool: 'threads',
          include: NODE_TEST_FILES,
          // This project does not extend the root, so the root's one 30s
          // budget does not reach it. State it here for the same reason.
          testTimeout: 30_000,
        },
      },
      {
        extends: true,
        test: {
          name: 'dom',
          // happy-dom builds a DOM about 2.7 times faster: the full suite takes 33.6s instead of 60.6s.
          // It returns '' from getComputedStyle(el).overflowX, although browsers and jsdom return 'visible'.
          // Tooltip.tsx reads that value to detect clipping.
          // The incorrect value makes every element appear clipped and prevents tests from reaching the unclipped branch.
          // Correct that difference before proposing a switch from jsdom.
          environment: 'jsdom',
          exclude: NODE_TEST_FILES,
          // Dexie reads IDBKeyRange during import. Install it before the storage setup.
          setupFiles: ['./vitest.idbKeyRange.ts', './vitest.setup.ts'],
        },
      },
    ],
  },
})
