/**
 * Fast Agent e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'

const fastAgentConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.FAST_AGENT,
  cliBinary: 'fast-agent',
  skipMessage: 'Fast Agent E2E requires a fast-agent CLI on PATH',
  workspacePrefix: 'fastagent-e2e',
}

export const FAST_AGENT_E2E_SKIP_REASON = detectACPSkipReason(fastAgentConfig)

export const fastAgentTest = base.extend<{
  fastAgentWorkspace: WorkspaceFixture
  authenticatedFastAgentWorkspace: WorkspaceFixture
}>({
  fastAgentWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, fastAgentConfig, use)
  },

  authenticatedFastAgentWorkspace: async ({ page, fastAgentWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, fastAgentWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface FastAgentAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Fast Agent agent in a directory the test knows, with the pinned model
 * and the option values the test states over them.
 *
 * fast-agent fixes its model at session creation: `set_config_option` raises
 * `method_not_found`, so there is no per-session model switch.
 */
export async function openFastAgentAgent(
  server: FastAgentAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('fastagent-e2e-wd-')
  const settings = agentOpenOptions(agentSettings(AgentProvider.FAST_AGENT))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.FAST_AGENT,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
