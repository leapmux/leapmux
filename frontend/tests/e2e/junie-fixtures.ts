/**
 * Junie e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'

const junieConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.JUNIE,
  cliBinary: 'junie',
  skipMessage: 'Junie E2E requires a junie CLI on PATH',
  workspacePrefix: 'junie-e2e',
}

export const JUNIE_E2E_SKIP_REASON = detectACPSkipReason(junieConfig)

export const junieTest = base.extend<{
  junieWorkspace: WorkspaceFixture
  authenticatedJunieWorkspace: WorkspaceFixture
}>({
  junieWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, junieConfig, use)
  },

  authenticatedJunieWorkspace: async ({ page, junieWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, junieWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface JunieAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Junie agent in a directory the test knows, with the pinned model and
 * the option values the test states over them.
 */
export async function openJunieAgent(
  server: JunieAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('junie-e2e-wd-')
  const settings = agentOpenOptions(agentSettings(AgentProvider.JUNIE))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.JUNIE,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
