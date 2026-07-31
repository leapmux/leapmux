import { resolve } from 'node:path'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'
import solid from 'vite-plugin-solid'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  // hot: false — HMR-runtime injection (/@solid-refresh) breaks fileURLToPath
  // on Windows and tests don't need it.
  plugins: [vanillaExtractPlugin(), solid({ hot: false })],
  resolve: {
    alias: {
      '~': resolve(__dirname, 'src'),
    },
  },
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
