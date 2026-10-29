import { CLAUDE_AGENT, nativeContext } from './claude-code/scenarios'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderProcessTest, createProviderTest } from './provider-fixture-factory'

export const CLAUDE_E2E_SKIP_REASON: string | null = missingBinaryReason('claude', 'Claude Code E2E requires the claude CLI on PATH')

export const claudeTest = createProviderTest({
  agent: CLAUDE_AGENT,
  nativeContext,
  skipReason: CLAUDE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedClaudeWorkspace',
})

/**
 * The test base of a Claude Code spec that restarts its own Hub or Worker.
 * It skips a test when the claude CLI is missing, as {@link claudeTest} does.
 * Its `native` context belongs to the Claude Code agent in `authenticatedWorkspace`.
 * The separate Hub and Worker run that agent.
 */
export const claudeProcessTest = createProviderProcessTest({
  agent: CLAUDE_AGENT,
  nativeContext,
  skipReason: CLAUDE_E2E_SKIP_REASON,
  openOptions: {
    optionValues: { permissionMode: 'default' },
  },
})
