import { CLAUDE_AGENT, nativeContext } from './claude-code/scenarios'
import { createProviderProcessTest, createProviderTest } from './provider-fixture-factory'

export const claudeTest = createProviderTest({
  agent: CLAUDE_AGENT,
  nativeContext,
  workspaceName: 'authenticatedClaudeWorkspace',
})

/**
 * The test base of a Claude Code spec that restarts its own Hub or Worker.
 * Its `native` context belongs to the Claude Code agent in `authenticatedWorkspace`.
 * The separate Hub and Worker run that agent.
 */
export const claudeProcessTest = createProviderProcessTest({
  agent: CLAUDE_AGENT,
  nativeContext,
  openOptions: {
    optionValues: { permissionMode: 'default' },
  },
})
