import type { ProviderWorkingDir } from '../../tests/e2e/helpers/providerWorkingDir'

// A `ProviderWorkingDir` for a unit test of an E2E helper.
//
// A unit test checks which directory a helper hands to an open that it mocks, so it needs the brand for a fixed path.
// It opens no agent, and no rule of a provider applies to a path that no test creates. Only a `.test.ts` file imports
// this module: the `no-restricted-syntax` block for `tests/e2e` in `eslint.config.ts` refuses the import in a spec, a
// fixture, or a helper, which open real agents.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own.

/** Give the brand of a working directory to `path`, for a unit test that opens no agent. */
export function unitWorkingDir(path: string): ProviderWorkingDir {
  return path as ProviderWorkingDir
}
