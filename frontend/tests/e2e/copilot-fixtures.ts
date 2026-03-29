/**
 * Copilot-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

// AgentProvider.COPILOT_CLI = 5
const copilotConfig: ACPFixtureConfig = {
  agentProvider: 5,
  cliBinary: 'copilot',
  skipMessage: 'Copilot E2E requires a copilot CLI on PATH',
  workspacePrefix: 'copilot-e2e',
}

export const COPILOT_E2E_SKIP_REASON = detectACPSkipReason(copilotConfig)

export const copilotTest = base.extend<{
  copilotWorkspace: WorkspaceFixture
  authenticatedCopilotWorkspace: WorkspaceFixture
}>({
  copilotWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, copilotConfig, use)
  },

  authenticatedCopilotWorkspace: async ({ page, copilotWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, copilotWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
