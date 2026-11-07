/** Kimi Code fixtures use the shared native agent workspace. */
import { KIMI_AGENT, nativeContext } from './kimi-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const kimiTest = createProviderTest({
  agent: KIMI_AGENT,
  nativeContext,
  workspaceName: 'authenticatedKimiWorkspace',
})
