/**
 * The parts that every provider test object shares.
 * Each `<provider>-fixtures.ts` builds its test object from these parts and from `authenticatedAgentWorkspace` in
 * `helpers/workspace.ts`, and keeps only the facts of its provider.
 */
import type { TestFixture } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './helpers/nativeScenario'

/** The fixture of {@link cliSkipFixture}, for the type parameter of a provider's `extend` call. */
export interface CliSkipFixture {
  cliSkip: void
}

/**
 * The scenario context of the provider, with every field of the provider built in.
 * Each provider test object yields it from the `nativeContext` of its provider directory, for its main
 * authenticated workspace.
 */
export interface NativeFixture {
  native: ManagedNativeScenarioContext
}

/**
 * An automatic fixture that skips each test of one provider when the E2E run cannot start the provider's CLI.
 *
 * A missing CLI is a property of the machine, not of model behavior, so every spec of the provider skips with the
 * same reason. Each provider test object registers it under the name `cliSkip`. An automatic fixture runs before
 * the fixtures that a test asks for, so the test skips before a workspace fixture starts an agent.
 */
export function cliSkipFixture(reason: string | null): [TestFixture<void, object>, { auto: true }] {
  if (reason !== null && reason.trim() === '')
    throw new Error('A provider skip needs the reason that its CLI is missing.')
  // Playwright reads the fixture names from the first parameter, which must be an object pattern.
  // eslint-disable-next-line no-empty-pattern
  return [async ({}, use, testInfo) => {
    testInfo.skip(reason !== null, reason ?? '')
    await use()
  }, { auto: true }]
}
