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
    environment: 'jsdom',
    globals: true,
    exclude: ['tests/e2e/**', 'node_modules/**'],
    setupFiles: ['./vitest.setup.ts'],
  },
})
