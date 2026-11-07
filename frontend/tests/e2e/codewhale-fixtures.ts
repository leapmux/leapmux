/** Codewhale fixtures exercise the current native REST and event protocol. */
import { CODEWHALE_AGENT, nativeContext } from './codewhale/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const codewhaleTest = createProviderTest({
  agent: CODEWHALE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedCodewhaleWorkspace',
})
