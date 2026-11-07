import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, REASONIX_AGENT } from './reasonix/scenarios'

export const reasonixTest = createProviderTest({
  agent: REASONIX_AGENT,
  nativeContext,
  workspaceName: 'authenticatedReasonixWorkspace',
})
