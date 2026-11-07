/** Pi fixtures use the shared native agent workspace. */
import { nativeContext, PI_AGENT } from './pi/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const piTest = createProviderTest({
  agent: PI_AGENT,
  nativeContext,
  workspaceName: 'authenticatedPiWorkspace',
})
