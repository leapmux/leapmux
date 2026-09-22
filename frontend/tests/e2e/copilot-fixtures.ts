/**
 * Copilot-specific e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../src/generated/contracts/copilot-protocol'
import { AgentProvider, authenticateACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import { withAgentWorkspace } from './helpers/workspace'

const copilotConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.GITHUB_COPILOT,
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
    await withAgentWorkspace(leapmuxServer, {
      provider: copilotConfig.agentProvider,
      prefix: copilotConfig.workspacePrefix,
      openOptions: { optionValues: { [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual } },
    }, use)
  },

  authenticatedCopilotWorkspace: async ({ page, copilotWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, copilotWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
