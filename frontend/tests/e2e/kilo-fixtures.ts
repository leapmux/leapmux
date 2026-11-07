import { KILO_AGENT, nativeContext } from './kilo/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const kiloTest = createProviderTest({
  agent: KILO_AGENT,
  nativeContext,
  workspaceName: 'authenticatedKiloWorkspace',
})
