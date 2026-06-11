/**
 * Reasonix (DeepSeek) e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const reasonixConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.REASONIX,
  cliBinary: 'reasonix',
  skipMessage: 'Reasonix E2E requires a reasonix CLI on PATH (and DEEPSEEK_API_KEY)',
  workspacePrefix: 'reasonix-e2e',
}

export const REASONIX_E2E_SKIP_REASON = detectACPSkipReason(reasonixConfig)

export const reasonixTest = base.extend<{
  reasonixWorkspace: WorkspaceFixture
  authenticatedReasonixWorkspace: WorkspaceFixture
}>({
  reasonixWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, reasonixConfig, use)
  },

  authenticatedReasonixWorkspace: async ({ page, reasonixWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, reasonixWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
