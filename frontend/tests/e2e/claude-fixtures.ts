import type { CliSkipFixture } from './acp-fixture-factory'
import type { WorkspaceFixture } from './helpers/workspace'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { cliSkipFixture } from './acp-fixture-factory'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { processTest } from './process-control-fixtures'

export const CLAUDE_E2E_SKIP_REASON: string | null = missingBinaryReason('claude', 'Claude Code E2E requires the claude CLI on PATH')

export const claudeTest = base.extend<CliSkipFixture & {
  claudeWorkspace: WorkspaceFixture
  authenticatedClaudeWorkspace: WorkspaceFixture
}>({
  cliSkip: cliSkipFixture(CLAUDE_E2E_SKIP_REASON),
  claudeWorkspace: async ({ leapmuxServer }, use) => {
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.CLAUDE_CODE, prefix: 'claude-e2e' }, use)
  },

  authenticatedClaudeWorkspace: async ({ page, claudeWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, claudeWorkspace.workspaceId)
    await use(claudeWorkspace)
  },
})

/**
 * The test base of a Claude Code spec that restarts its own Hub or Worker.
 * It skips a test when the claude CLI is missing, as {@link claudeTest} does.
 */
export const claudeProcessTest = processTest.extend<CliSkipFixture>({
  cliSkip: cliSkipFixture(CLAUDE_E2E_SKIP_REASON),
})

export { expect }
