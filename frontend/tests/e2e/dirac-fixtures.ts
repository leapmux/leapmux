/**
 * Dirac e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'

const diracConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.DIRAC,
  cliBinary: 'dirac',
  skipMessage: 'Dirac E2E requires a dirac CLI on PATH',
  workspacePrefix: 'dirac-e2e',
}

export const DIRAC_E2E_SKIP_REASON = detectACPSkipReason(diracConfig)

export const diracTest = base.extend<{
  diracWorkspace: WorkspaceFixture
  authenticatedDiracWorkspace: WorkspaceFixture
}>({
  diracWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, diracConfig, use)
  },

  authenticatedDiracWorkspace: async ({ page, diracWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, diracWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface DiracAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Dirac agent in a directory the test knows, with the pinned model and
 * the option values the test states over them.
 *
 * A turn ends only when the model calls `respond` with `operation: "complete"`,
 * so every scripted turn must queue a tool call, not text.
 */
export async function openDiracAgent(
  server: DiracAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('dirac-e2e-wd-')
  const settings = agentOpenOptions(agentSettings(AgentProvider.DIRAC))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.DIRAC,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
