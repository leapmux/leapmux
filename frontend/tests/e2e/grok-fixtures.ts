/**
 * Grok Build e2e test fixtures.
 */
import { GROK_AGENT, nativeContext } from './grok-build/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const GROK_E2E_SKIP_REASON: string | null = missingBinaryReason('grok', 'Grok Build E2E requires a grok CLI on PATH')

export const grokTest = createProviderTest({
  agent: GROK_AGENT,
  nativeContext,
  skipReason: GROK_E2E_SKIP_REASON,
  workspaceName: 'authenticatedGrokWorkspace',
})
