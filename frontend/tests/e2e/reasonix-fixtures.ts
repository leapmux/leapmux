/**
 * Reasonix (DeepSeek) e2e test fixtures.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture } from './provider-fixture-factory'
import { nativeContext } from './reasonix/scenarios'

export const REASONIX_E2E_SKIP_REASON: string | null = missingBinaryReason('reasonix', 'Reasonix E2E requires a reasonix CLI on PATH')

/** How a Reasonix agent opens. */
export const REASONIX_AGENT: ProviderAgent = { provider: AgentProvider.REASONIX, prefix: 'reasonix-e2e' }

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
