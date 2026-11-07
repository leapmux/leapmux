import { MIMO_AGENT, nativeContext } from './mimo-code/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const mimoTest = createProviderTest({
  agent: MIMO_AGENT,
  nativeContext,
  workspaceName: 'authenticatedMiMoWorkspace',
})
