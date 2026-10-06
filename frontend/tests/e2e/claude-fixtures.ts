import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { expect } from '@playwright/test'
import { CLAUDE_AGENT, nativeContext } from './claude-code/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { processTest } from './process-control-fixtures'
import { cliSkipFixture } from './provider-fixture-factory'

export const CLAUDE_E2E_SKIP_REASON: string | null = missingBinaryReason('claude', 'Claude Code E2E requires the claude CLI on PATH')

export const claudeTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedClaudeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CLAUDE_E2E_SKIP_REASON),
  authenticatedClaudeWorkspace: authenticatedAgentWorkspace(CLAUDE_AGENT),
  native: async ({ page, modelScript, leapmuxServer, authenticatedClaudeWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedClaudeWorkspace.workspaceId }))
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
