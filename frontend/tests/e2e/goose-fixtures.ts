/**
 * Goose-specific e2e test fixtures.
 */
import { GOOSE_AGENT, nativeContext } from './goose/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const GOOSE_E2E_SKIP_REASON: string | null = missingBinaryReason('goose', 'Goose E2E requires a goose CLI on PATH')

export const gooseTest = createProviderTest({
  agent: GOOSE_AGENT,
  nativeContext,
  skipReason: GOOSE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedGooseWorkspace',
})
