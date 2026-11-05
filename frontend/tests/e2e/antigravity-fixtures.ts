import { ANTIGRAVITY_AGENT, nativeContext } from './antigravity-cli/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'

export const ANTIGRAVITY_E2E_SKIP_REASON: string | null = missingBinaryReason('agy', 'Antigravity CLI E2E requires agy on PATH')

export const antigravityTest = createProviderTest({
  agent: ANTIGRAVITY_AGENT,
  nativeContext,
  skipReason: ANTIGRAVITY_E2E_SKIP_REASON,
  workspaceName: 'authenticatedAntigravityWorkspace',
})
