/**
 * Kilo-specific e2e test fixtures.
 */
import { missingBinaryReason } from './helpers/binaryOnPath'
import { KILO_AGENT, nativeContext } from './kilo/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const KILO_E2E_SKIP_REASON: string | null = missingBinaryReason('kilo', 'Kilo E2E requires a kilo CLI on PATH')

export const kiloTest = createProviderTest({
  agent: KILO_AGENT,
  nativeContext,
  skipReason: KILO_E2E_SKIP_REASON,
  workspaceName: 'authenticatedKiloWorkspace',
})
