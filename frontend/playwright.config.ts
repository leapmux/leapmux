import type { PlaywrightTestConfig } from '@playwright/test'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { defineConfig, devices } from '@playwright/test'

const outputFileDirectory = process.env.LEAPMUX_E2E_OUTPUT_FILE_DIR
if (outputFileDirectory !== undefined && (!isAbsolute(outputFileDirectory) || outputFileDirectory.includes('\0')))
  throw new Error('The E2E full tool output directory must be an absolute path without NUL characters.')

// The launcher supplies a separate directory for each Playwright process.
// Keep reports outside test-results because Playwright clears test-results before each run.
const artifactConfig: Pick<PlaywrightTestConfig, 'outputDir' | 'reporter'> = outputFileDirectory === undefined
  ? {}
  : {
      outputDir: join(outputFileDirectory, 'test-results'),
      reporter: [
        ['list'],
        ['blob', { outputDir: join(outputFileDirectory, 'blob-report') }],
        ['json', { outputFile: join(outputFileDirectory, 'report.json') }],
      ],
    }

export default defineConfig({
  ...artifactConfig,
  testDir: './tests/e2e',
  // Playwright runs browser specifications. Vitest runs co-located `.test.ts` helper tests.
  testMatch: '**/*.spec.ts',
  globalSetup: './tests/e2e/global-setup.ts',
  globalTeardown: './tests/e2e/global-teardown.ts',
  // Each shard uses one worker with a shared LeapMux process and browser context.
  // Tests in that worker share one tab.
  // A failed test restarts this worker. The new worker receives a new browser context.
  fullyParallel: false,
  workers: 1,
  // Every endpoint in this suite is deterministic. A retry can hide a product defect.
  retries: 0,
  use: {
    trace: 'retain-on-failure',
    permissions: ['clipboard-read', 'clipboard-write'],
  },
  projects: [
    {
      // Every test in this project reaches the mock model endpoint.
      // Cursor reaches the mock through helpers/cursorSurface.ts also.
      // No specification carries @real-provider, so a separate live-model project selects no test.
      // A project that selects no test contains deadlines that no test measures.
      // A new test would receive those deadlines without verification.
      //
      // The mock answers in milliseconds. Native process startup requires more time.
      // Some tests force a Worker restart also.
      // The deadlines below allow those operations without a live-model delay for each failed assertion.
      //
      // Add a live-model project together with its tests if live-model tests become necessary.
      // Measure that project's deadlines against those tests.
      // These mock tests do not detect differences from a real provider's wire format.
      // Cover those differences in testdata/*_conformance.json.
      // See https://github.com/leapmux/leapmux/issues/491.
      name: 'mock-chromium',
      timeout: 120_000,
      expect: { timeout: 30_000 },
      use: { ...devices['Desktop Chrome'], actionTimeout: 15_000 },
    },
  ],
})
