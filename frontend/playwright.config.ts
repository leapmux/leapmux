import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  globalSetup: './tests/e2e/global-setup.ts',
  globalTeardown: './tests/e2e/global-teardown.ts',
  fullyParallel: false,
  workers: 2,
  // 300s per test, 120s per assertion.
  //
  // Both were raised when the per-call `{ timeout: ... }` overrides were removed
  // from the specs (the repo forbids them). The slowest legitimate waits are
  // real ones -- agent startup and a full LLM turn -- and they had been carrying
  // 60s/120s overrides, so the global has to cover the slowest of those or those
  // specs simply flake. The test timeout keeps its ~2.5x headroom over a single
  // assertion so one slow expect cannot eat the whole test budget.
  //
  // The cost is honest and deliberate: a genuinely broken assertion now takes
  // 120s to report instead of 30s. That is the price of having one number
  // instead of 63 scattered ones.
  timeout: 300_000,
  expect: {
    timeout: 120_000,
  },
  use: {
    actionTimeout: 30_000,
    trace: 'retain-on-failure',
    permissions: ['clipboard-read', 'clipboard-write'],
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
