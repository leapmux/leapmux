import { ANTIGRAVITY_AGENT, nativeContext } from './antigravity-cli/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const antigravityTest = createProviderTest({
  agent: ANTIGRAVITY_AGENT,
  nativeContext,
  workspaceName: 'authenticatedAntigravityWorkspace',
})
