import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { CLAUDE_AGENT, nativeContext } from './claude-code/scenarios'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { cliSkipFixture, createProviderProcessTest } from './provider-fixture-factory'

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
 * Its `native` fixture is the scenario context of the agent of `authenticatedWorkspace`, the Claude Code agent that
 * the separate Hub and Worker run.
 */
export const claudeProcessTest = createProviderProcessTest({
  agent: CLAUDE_AGENT,
  nativeContext,
  skipReason: CLAUDE_E2E_SKIP_REASON,
  openOptions: { optionValues: { permissionMode: 'default' } },
})
