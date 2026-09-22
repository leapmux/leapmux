import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  // Playwright runs browser specifications. Vitest runs co-located `.test.ts` helper tests.
  testMatch: '**/*.spec.ts',
  globalSetup: './tests/e2e/global-setup.ts',
  globalTeardown: './tests/e2e/global-teardown.ts',
  // One worker preserves one shared LeapMux process, browser context, and tab.
  // A failed test restarts this worker. The new worker receives a new browser context.
  fullyParallel: false,
  workers: 1,
  // A retry can hide a product defect, and every endpoint this suite reaches
  // is deterministic, so a second attempt would only mask the first.
  retries: 0,
  use: {
    trace: 'retain-on-failure',
    permissions: ['clipboard-read', 'clipboard-write'],
  },
  projects: [
    {
      // ONE project, and every test in it reaches the mock model endpoint.
      //
      // There was a second, `real-provider-chromium`, selected by a
      // `@real-provider` tag and given deadlines sized for a live model. No
      // specification carries that tag any more -- Cursor was the last holdout,
      // and it reaches the mock through `helpers/cursorSurface.ts` now -- so the
      // project matched nothing and never ran. A project that selects no test
      // is untested configuration: it states timeouts nobody measured, and the
      // first test to adopt the tag would inherit them unexamined.
      //
      // The deadlines below are sized for that one endpoint. It answers in
      // milliseconds, so the slowest wait here is an agent process starting,
      // plus a worker restart where a test forces one. A deadline sized for a
      // real model would only make a FAILURE slow: every unanswered assertion
      // would then cost two minutes.
      //
      // Bringing a real-model test back means adding the project back WITH it,
      // in the same change, so its deadlines are chosen against something that
      // runs. See `.tmp/e2e-infra-plan.md` for what this costs -- nothing now
      // detects the mock drifting from a real provider's wire format.
      name: 'mock-chromium',
      timeout: 120_000,
      expect: { timeout: 30_000 },
      use: { ...devices['Desktop Chrome'], actionTimeout: 15_000 },
    },
  ],
})
