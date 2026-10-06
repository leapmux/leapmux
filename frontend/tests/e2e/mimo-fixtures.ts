/**
 * MiMo Code E2E fixtures, on the shared agent workspace lifetime.
 *
 * The skip check looks for the `mimo` binary without running it: MiMo writes a
 * skeleton configuration file on each start, and this check runs with the
 * developer's own HOME. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { nativeContext } from './mimo-code/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const MIMO_E2E_SKIP_REASON: string | null = missingBinaryReason('mimo', 'MiMo Code E2E requires the mimo CLI on PATH')

/** How a MiMo Code agent opens. */
export const MIMO_AGENT: ProviderAgent = { provider: AgentProvider.MIMO_CODE, prefix: 'mimo-e2e' }

export const mimoTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedMiMoWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(MIMO_E2E_SKIP_REASON),
  authenticatedMiMoWorkspace: authenticatedAgentWorkspace(MIMO_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId }))
  },
})

export { expect }
