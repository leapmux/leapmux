/**
 * Reasonix (DeepSeek) e2e test fixtures.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { nativeContext, REASONIX_AGENT } from './reasonix/scenarios'

export const REASONIX_E2E_SKIP_REASON: string | null = missingBinaryReason('reasonix', 'Reasonix E2E requires a reasonix CLI on PATH')

export const reasonixTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedReasonixWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(REASONIX_E2E_SKIP_REASON),
  authenticatedReasonixWorkspace: authenticatedAgentWorkspace(REASONIX_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedReasonixWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId }))
  },
})

export { expect }
