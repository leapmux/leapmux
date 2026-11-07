import { CODEX_AGENT, nativeContext } from './codex/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const codexTest = createProviderTest({
  agent: CODEX_AGENT,
  nativeContext,
  workspaceName: 'authenticatedCodexWorkspace',
})
