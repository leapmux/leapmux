/**
 * Goose-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const gooseConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.GOOSE,
  cliBinary: 'goose',
  skipMessage: 'Goose E2E requires a goose CLI on PATH',
  workspacePrefix: 'goose-e2e',
}

export const GOOSE_E2E_SKIP_REASON = detectACPSkipReason(gooseConfig)

export const gooseTest = base.extend<{
  gooseWorkspace: WorkspaceFixture
  authenticatedGooseWorkspace: WorkspaceFixture
}>({
  gooseWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, gooseConfig, use)
  },

  authenticatedGooseWorkspace: async ({ page, gooseWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, gooseWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
