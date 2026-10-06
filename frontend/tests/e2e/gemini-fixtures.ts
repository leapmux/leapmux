import type { ACPFixtureConfig, CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, cliSkipFixture, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'

const geminiConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.GEMINI_CLI,
  cliBinary: 'gemini',
  skipMessage: 'Gemini CLI E2E requires a gemini CLI on PATH',
  workspacePrefix: 'gemini-e2e',
}

export const GEMINI_E2E_SKIP_REASON = detectACPSkipReason(geminiConfig)

export const geminiTest = base.extend<CliSkipFixture & {
  geminiWorkspace: WorkspaceFixture
  authenticatedGeminiWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(GEMINI_E2E_SKIP_REASON),
  geminiWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, geminiConfig, use)
  },
  authenticatedGeminiWorkspace: async ({ page, geminiWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, geminiWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

/** Open Gemini in a private directory that native file scenarios can inspect. */
export async function openGeminiAgent(
  server: { hubUrl: string, adminToken: string, workerId: string },
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('gemini-e2e-wd-')
  const settings = agentOpenOptions(agentSettings(AgentProvider.GEMINI_CLI))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.GEMINI_CLI,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
