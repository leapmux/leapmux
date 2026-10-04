/**
 * Kiro E2E fixtures, on the shared fixtures of the Agent Client Protocol (ACP)
 * providers.
 *
 * Kiro talks to its own service, which `helpers/kiroSurface.ts` answers on the mock
 * endpoint, and `helpers/mockAgentEnvironment.ts` points every Kiro setting at that
 * endpoint under an isolated HOME. No test reaches a real Kiro account.
 *
 * The skip check looks for the `kiro-cli-chat` binary without running it: the check
 * runs with the developer's own HOME, and Kiro reads its configuration directory on
 * every start. See `helpers/binaryOnPath.ts`.
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
 * Kiro reads steering documents, agents and hooks from the workspace it runs in. The
 * run directory sits inside the LeapMux checkout, whose root holds such files, and a
 * repository of its own holds none of them.
 */
export function createKiroWorkingDir(): string {
  return createGitRepo(createTestDirectory('kiro-e2e-wd-'), 'repo')
}

const kiroConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.KIRO,
  cliBinary: 'kiro-cli-chat',
  skipMessage: 'Kiro E2E requires the kiro-cli-chat CLI on PATH',
  workspacePrefix: 'kiro-e2e',
  workingDir: createKiroWorkingDir,
}

export const KIRO_E2E_SKIP_REASON = detectACPSkipReason(kiroConfig)

export const kiroTest = base.extend<{
  kiroWorkspace: WorkspaceFixture
  authenticatedKiroWorkspace: WorkspaceFixture
}>({
  kiroWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, kiroConfig, use)
  },

  authenticatedKiroWorkspace: async ({ page, kiroWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, kiroWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface KiroAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Kiro agent in a directory the test knows, with the pinned model and effort
 * and the option values the test states over them.
 *
 * A spec that gives file paths in its tool calls needs the directory, which the
 * shared fixture keeps to itself. `prepare` writes into the directory before the
 * agent starts, for a configuration that Kiro reads only at its start.
 */
export async function openKiroAgent(
  server: KiroAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
  prepare?: (workingDir: string) => void,
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createKiroWorkingDir()
  prepare?.(workingDir)
  const settings = agentOpenOptions(agentSettings(AgentProvider.KIRO))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.KIRO,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
