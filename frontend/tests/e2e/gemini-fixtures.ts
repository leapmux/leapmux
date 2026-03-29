/**
 * Gemini-specific e2e test fixtures.
 */
import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

// AgentProvider.GEMINI_CLI = 3
const geminiConfig: ACPFixtureConfig = {
  agentProvider: 3,
  cliBinary: 'gemini',
  skipMessage: 'Gemini E2E requires a gemini CLI on PATH',
  workspacePrefix: 'gemini-e2e',
}

export const GEMINI_E2E_SKIP_REASON = detectACPSkipReason(geminiConfig)

export const geminiTest = base.extend<{
  geminiWorkspace: WorkspaceFixture
  authenticatedGeminiWorkspace: WorkspaceFixture
}>({
  geminiWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, geminiConfig, use)
  },

  authenticatedGeminiWorkspace: async ({ page, geminiWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, geminiWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
