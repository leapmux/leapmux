import type { ACPFixtureConfig, WorkspaceFixture } from './acp-fixture-factory'
import { AgentProvider, authenticateACPWorkspace, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'

const cursorConfig: ACPFixtureConfig = {
  agentProvider: AgentProvider.CURSOR_CLI,
  cliBinary: 'cursor-agent',
  skipMessage: 'Cursor E2E requires a cursor-agent CLI on PATH',
  workspacePrefix: 'cursor-e2e',
  model: 'auto',
}

export const CURSOR_E2E_SKIP_REASON = detectACPSkipReason(cursorConfig)

export const cursorTest = base.extend<{
  cursorWorkspace: WorkspaceFixture
  authenticatedCursorWorkspace: WorkspaceFixture
}>({
  cursorWorkspace: async ({ leapmuxServer }, use) => {
    await createACPWorkspace(leapmuxServer, cursorConfig, use)
  },

  authenticatedCursorWorkspace: async ({ page, cursorWorkspace, leapmuxServer }, use) => {
    await authenticateACPWorkspace(page, cursorWorkspace, leapmuxServer.adminToken, use)
  },
})

export { expect }
