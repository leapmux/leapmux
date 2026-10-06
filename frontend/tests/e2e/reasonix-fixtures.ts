/**
 * Reasonix (DeepSeek) e2e test fixtures.
 */
import type { ACPFixtureConfig, CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, cliSkipFixture, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const reasonixConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.REASONIX,
  cliBinary: 'reasonix',
  skipMessage: 'Reasonix E2E requires a reasonix CLI on PATH',
  workspacePrefix: 'reasonix-e2e',
}

export const REASONIX_E2E_SKIP_REASON = detectACPSkipReason(reasonixConfig)

export const reasonixTest = base.extend<CliSkipFixture & {
  reasonixWorkspace: WorkspaceFixture
  authenticatedReasonixWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(REASONIX_E2E_SKIP_REASON),
  reasonixWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, reasonixConfig, use)
  },

  authenticatedReasonixWorkspace: async ({ page, reasonixWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, reasonixWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
