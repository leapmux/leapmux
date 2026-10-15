/**
 * Reasonix (DeepSeek) e2e test fixtures.
 */
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, REASONIX_AGENT } from './reasonix/scenarios'

export const REASONIX_E2E_SKIP_REASON: string | null = missingBinaryReason('reasonix', 'Reasonix E2E requires a reasonix CLI on PATH')

export const reasonixTest = createProviderTest({
  agent: REASONIX_AGENT,
  nativeContext,
  skipReason: REASONIX_E2E_SKIP_REASON,
  workspaceName: 'authenticatedReasonixWorkspace',
})
