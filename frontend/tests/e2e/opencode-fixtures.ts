/**
 * OpenCode-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const opencodeConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.OPENCODE,
  cliBinary: 'opencode',
  skipMessage: 'OpenCode E2E requires opencode CLI on PATH',
  workspacePrefix: 'opencode-e2e',
}

export const OPENCODE_E2E_SKIP_REASON = detectACPSkipReason(opencodeConfig)

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
