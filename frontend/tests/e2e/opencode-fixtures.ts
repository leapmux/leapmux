/**
 * OpenCode-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { authenticateACPWorkspace, createACPWorkspace } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

// AgentProvider.OPENCODE = 4
const opencodeConfig: ACPFixtureConfig = {
  agentProvider: 4,
  workspacePrefix: 'opencode-e2e',
}

export const opencodeTest = base.extend<{
  opencodeWorkspace: WorkspaceFixture
  authenticatedOpencodeWorkspace: WorkspaceFixture
}>({
  opencodeWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, opencodeConfig, use)
  },

  authenticatedOpencodeWorkspace: async ({ page, opencodeWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, opencodeWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
