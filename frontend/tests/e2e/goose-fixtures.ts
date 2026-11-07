/**
 * Goose-specific e2e test fixtures.
 */
import { GOOSE_AGENT, nativeContext } from './goose/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const gooseTest = createProviderTest({
  agent: GOOSE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedGooseWorkspace',
})
