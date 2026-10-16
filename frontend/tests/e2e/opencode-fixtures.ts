/**
 * OpenCode-specific e2e test fixtures.
 */
import { missingBinaryReason } from './helpers/binaryOnPath'
import { nativeContext, OPENCODE_AGENT } from './opencode/scenarios'
import { createProviderTest } from './provider-fixture-factory'

export const OPENCODE_E2E_SKIP_REASON: string | null = missingBinaryReason('opencode', 'OpenCode E2E requires opencode CLI on PATH')

export const opencodeTest = createProviderTest({
  agent: OPENCODE_AGENT,
  nativeContext,
  skipReason: OPENCODE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedOpencodeWorkspace',
})
