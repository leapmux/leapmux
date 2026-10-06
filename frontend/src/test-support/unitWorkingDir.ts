import type { ProviderWorkingDir } from '../../tests/e2e/helpers/providerWorkingDir'

// A `ProviderWorkingDir` for a unit test of an E2E helper.
//
// A unit test checks which directory a helper hands to an open that it mocks, so it needs the brand for a fixed path.
// It opens no agent, and no rule of a provider applies to a path that no test creates. Only a `.test.ts` file imports
// this module. A spec, a fixture, and a helper open real agents, so each makes its directory by the rule of the
// provider.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own.

/** Give the brand of a working directory to `path`, for a unit test that opens no agent. */
export function unitWorkingDir(path: string): ProviderWorkingDir {
  return path as ProviderWorkingDir
}
