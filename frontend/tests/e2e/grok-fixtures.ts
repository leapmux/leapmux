/**
 * Grok Build e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { createGitRepo } from './helpers/worktree'

/**
 * A working directory that is the root of a git repository of its own.
 *
 * Grok keys folder trust by the git repository around the working directory, and
 * it asks the client whether to trust a repository that holds configuration of
 * its own -- an `AGENTS.md`, an `.mcp.json`, hooks. The run directory sits inside
 * the LeapMux checkout, whose root holds such files, so every agent opened there
 * raised the trust question before its first turn. A repository of its own holds
 * none, so nothing in it calls for trust: Grok asks nothing, the workspace stays
 * untrusted, and none of the checkout's configuration loads.
 * `149-grok-settings-trust` asks the question on purpose.
 */
export function createGrokWorkingDir(): string {
  return createGitRepo(createTestDirectory('grok-e2e-wd-'), 'repo')
}

const grokConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.GROK_BUILD,
  cliBinary: 'grok',
  skipMessage: 'Grok Build E2E requires a grok CLI on PATH',
  workspacePrefix: 'grok-e2e',
  workingDir: createGrokWorkingDir,
}

export const GROK_E2E_SKIP_REASON = detectACPSkipReason(grokConfig)

export const grokTest = base.extend<{
  grokWorkspace: WorkspaceFixture
  authenticatedGrokWorkspace: WorkspaceFixture
}>({
  grokWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, grokConfig, use)
  },

  authenticatedGrokWorkspace: async ({ page, grokWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, grokWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface GrokAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Grok agent in a directory the test knows, with the pinned model and
 * effort and the option values the test states over them.
 *
 * A spec that gives file paths in its tool calls needs the directory, which the
 * shared fixture keeps to itself.
 */
export async function openGrokAgent(
  server: GrokAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createGrokWorkingDir()
  const settings = agentOpenOptions(agentSettings(AgentProvider.GROK_BUILD))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.GROK_BUILD,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
