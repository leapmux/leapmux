/**
 * The test base of a spec whose every test runs against a dev server of its own, such as a hub in setup mode or a
 * hub with captcha settings that the suite hub must not take.
 */
import type { ModelScriptFixtures } from './helpers/modelScriptFixture'
import { test as base } from '@playwright/test'
import { modelScriptFixtures } from './helpers/modelScriptFixture'

/** What the base reads from the dev server of a test. */
export interface DedicatedServer {
  hubUrl: string
}

/** The fixtures of {@link devServerTest}. */
export interface DevServerFixtures<S extends DedicatedServer> extends ModelScriptFixtures {
  /** The dev server of the test. */
  server: S
}

/**
 * Build the test base of a spec whose every test starts a dev server through `start`.
 *
 * - `server`: `start` runs a new server for one test, hands it to `use`, and stops it after `use` returns.
 * - `baseURL`: the hub of that server, so `page.goto('/login')` reaches it.
 * - The test deadline and the model script of `modelScriptFixtures`, as the two other test bases have them. A wait
 *   such as `retryUntilPass` then ends before the test's own timeout, with its own message.
 */
export function devServerTest<S extends DedicatedServer>(start: (use: (server: S) => Promise<void>) => Promise<void>) {
  return base.extend<DevServerFixtures<S>>({
    ...modelScriptFixtures,
    // Playwright reads the dependencies of a fixture from its destructured first parameter, and this one has none.
    // eslint-disable-next-line no-empty-pattern
    server: async ({}, use) => start(use),
    baseURL: async ({ server }, use) => {
      await use(server.hubUrl)
    },
  })
}
