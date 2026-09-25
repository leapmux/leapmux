/**
 * Qwen Code e2e test fixtures.
 */
import type { ACPFixtureConfig } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'

const qwenConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.QWEN_CODE,
  cliBinary: 'qwen',
  skipMessage: 'Qwen Code E2E requires a qwen CLI on PATH',
  workspacePrefix: 'qwen-e2e',
}

export const QWEN_E2E_SKIP_REASON = detectACPSkipReason(qwenConfig)

export const qwenTest = base.extend<{
  qwenWorkspace: WorkspaceFixture
  authenticatedQwenWorkspace: WorkspaceFixture
}>({
  qwenWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, qwenConfig, use)
  },

  authenticatedQwenWorkspace: async ({ page, qwenWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, qwenWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }

interface QwenAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/**
 * Open a Qwen agent in a directory the test knows, with the pinned model and
 * effort and the option values the test states over them.
 *
 * A spec that gives file paths in its tool calls needs the directory, which the
 * shared fixture keeps to itself.
 */
export async function openQwenAgent(
  server: QwenAgentServer,
  workspaceId: string,
  optionValues: Record<string, string> = {},
): Promise<{ agentId: string, workingDir: string }> {
  const workingDir = createTestDirectory('qwen-e2e-wd-')
  const settings = agentOpenOptions(agentSettings(AgentProvider.QWEN_CODE))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.QWEN_CODE,
    ...settings,
    optionValues: { ...settings.optionValues, ...optionValues },
  })
  return { agentId, workingDir }
}
