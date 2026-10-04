import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'

export const claudeTest = base.extend<{
  claudeWorkspace: WorkspaceFixture
  authenticatedClaudeWorkspace: WorkspaceFixture
}>({
  claudeWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CLAUDE_CODE, prefix: 'claude-e2e' }, use)
  },

  authenticatedClaudeWorkspace: async ({ page, claudeWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, claudeWorkspace.workspaceId)
    await use(claudeWorkspace)
  },
})

export { expect }
