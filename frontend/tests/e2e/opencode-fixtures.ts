import { nativeContext, OPENCODE_AGENT } from './opencode/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const opencodeTest = createProviderTest({
  agent: OPENCODE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedOpencodeWorkspace',
})
