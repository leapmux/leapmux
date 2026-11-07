/** ZCode fixtures supply private provider configuration and a mock credential. */
import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, ZCODE_AGENT } from './zcode/scenarios'

export const zcodeTest = createProviderTest({
  agent: ZCODE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedZCodeWorkspace',
})
