/**
 * MiMo Code E2E fixtures, on the shared agent workspace lifetime.
 *
 * The skip check looks for the `mimo` binary without running it: MiMo writes a
 * skeleton configuration file on each start, and this check runs with the
 * developer's own HOME. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { MIMO_AGENT, nativeContext } from './mimo-code/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const MIMO_E2E_SKIP_REASON: string | null = missingBinaryReason('mimo', 'MiMo Code E2E requires the mimo CLI on PATH')

export const mimoTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedMiMoWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(MIMO_E2E_SKIP_REASON),
  authenticatedMiMoWorkspace: authenticatedAgentWorkspace(MIMO_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId }))
  },
})
