import type { AgentWorkspace, ProviderAgent } from './helpers/workspace'
import type { CliSkipFixture } from './provider-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { test as base, expect } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { processTest } from './process-control-fixtures'
import { cliSkipFixture } from './provider-fixture-factory'

export const CLAUDE_E2E_SKIP_REASON: string | null = missingBinaryReason('claude', 'Claude Code E2E requires the claude CLI on PATH')

/** How a Claude Code agent opens. */
export const CLAUDE_AGENT: ProviderAgent = { provider: AgentProvider.CLAUDE_CODE, prefix: 'claude-e2e' }

export const claudeTest = base.extend<CliSkipFixture & {
  authenticatedClaudeWorkspace: AgentWorkspace
}>({
  cliSkip: cliSkipFixture(CLAUDE_E2E_SKIP_REASON),
  authenticatedClaudeWorkspace: authenticatedAgentWorkspace(CLAUDE_AGENT),
})

/**
 * The test base of a Claude Code spec that restarts its own Hub or Worker.
 * It skips a test when the claude CLI is missing, as {@link claudeTest} does.
 */
export const claudeProcessTest = processTest.extend<CliSkipFixture>({
  cliSkip: cliSkipFixture(CLAUDE_E2E_SKIP_REASON),
})

export { expect }
