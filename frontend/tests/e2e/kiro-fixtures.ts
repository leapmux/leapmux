/**
 * Kiro E2E fixtures, on the shared agent workspace lifetime.
 *
 * Kiro talks to its own service, which `helpers/kiroSurface.ts` answers on the mock
 * endpoint, and `helpers/mockAgentEnvironment.ts` points every Kiro setting at that
 * endpoint under an isolated HOME. No test reaches a real Kiro account.
 *
 * The skip check looks for the `kiro-cli-chat` binary without running it: the check
 * runs with the developer's own HOME, and Kiro reads its configuration directory on
 * every start. See `helpers/binaryOnPath.ts`.
 */
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { KIRO_AGENT, nativeContext } from './kiro/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const KIRO_E2E_SKIP_REASON: string | null = missingBinaryReason('kiro-cli-chat', 'Kiro E2E requires the kiro-cli-chat CLI on PATH')

export const kiroTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedKiroWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(KIRO_E2E_SKIP_REASON),
  authenticatedKiroWorkspace: authenticatedAgentWorkspace(KIRO_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId }))
  },
})
