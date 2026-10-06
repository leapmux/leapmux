/**
 * Junie e2e test fixtures.
 */
import type { ACPFixtureConfig, CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, cliSkipFixture, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_RESPONSES_MODEL } from './helpers/mockAgentEnvironment'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

const junieConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.JUNIE,
  cliBinary: 'junie',
  skipMessage: 'Junie E2E requires a junie CLI on PATH',
  workspacePrefix: 'junie-e2e',
}

export const JUNIE_E2E_SKIP_REASON = detectACPSkipReason(junieConfig)

export const junieTest = base.extend<CliSkipFixture & {
  junieWorkspace: WorkspaceFixture
  authenticatedJunieWorkspace: WorkspaceFixture
  authenticatedResponsesJunieWorkspace: WorkspaceFixture
  authenticatedNativeEffortJunieWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(JUNIE_E2E_SKIP_REASON),
  junieWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, junieConfig, use)
  },

  authenticatedJunieWorkspace: async ({ page, junieWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, junieWorkspace, leapmuxServer.adminToken, use)
  },

  authenticatedResponsesJunieWorkspace: async ({ page, leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, {
      provider: AgentProvider.JUNIE,
      prefix: 'junie-e2e-responses',
      openOptions: { model: JUNIE_RESPONSES_MODEL },
    }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use(workspace)
    })
  },

  authenticatedNativeEffortJunieWorkspace: async ({ page, leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, {
      provider: AgentProvider.JUNIE,
      prefix: 'junie-e2e-native-effort',
      openOptions: agentOpenOptions({ model: JUNIE_NATIVE_EFFORT_MODEL, effort: 'high' }),
    }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use(workspace)
    })
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
  prepareWorkingDir?: (workingDir: string) => void,
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('junie-e2e-wd-')
  prepareWorkingDir?.(workingDir)
  const settings = agentOpenOptions(agentSettings(AgentProvider.JUNIE))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.JUNIE,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
