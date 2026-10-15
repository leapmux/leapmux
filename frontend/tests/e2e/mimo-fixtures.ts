/**
 * MiMo Code E2E fixtures, on the shared agent workspace lifetime.
 *
 * The skip check finds the `mimo` binary without running it.
 * MiMo writes an initial configuration file on each start. This check uses the developer's HOME.
 * See `helpers/binaryOnPath.ts`.
 */
import { missingBinaryReason } from './helpers/binaryOnPath'
import { MIMO_AGENT, nativeContext } from './mimo-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const MIMO_E2E_SKIP_REASON: string | null = missingBinaryReason('mimo', 'MiMo Code E2E requires the mimo CLI on PATH')

export const mimoTest = createProviderTest({
  agent: MIMO_AGENT,
  nativeContext,
  skipReason: MIMO_E2E_SKIP_REASON,
  workspaceName: 'authenticatedMiMoWorkspace',
})
