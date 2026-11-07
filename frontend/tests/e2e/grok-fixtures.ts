/**
 * Grok Build e2e test fixtures.
 */
import { GROK_AGENT, nativeContext } from './grok-build/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const grokTest = createProviderTest({
  agent: GROK_AGENT,
  nativeContext,
  workspaceName: 'authenticatedGrokWorkspace',
})
