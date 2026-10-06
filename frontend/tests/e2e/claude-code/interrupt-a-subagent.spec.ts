/** Test native child interruption without stopping the parent. */
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseChildInterrupt, expectNoRegistryRows, HELD_CHILD_TASK } from '../helpers/subagentRegistry'

claudeTest.describe('Claude subagent background tasks', () => {
  // Claude's native stop_task control stops only the selected child.
  // The registry reports the user stop as interrupted.
  // Activity ends the child thinking indicator without a synthetic transcript message.
  claudeTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    authenticatedWorkspace,
    page,
    modelScript,
    leapmuxServer,
  }) => {
    void authenticatedWorkspace
    await expectNoRegistryRows(page, leapmuxServer)
    // Claude can start more root turns when it reports a stopped background
    // task. Their count depends on when the stop reaches its pending turn.
    await modelScript.fallback({ text: 'Notification noted.' })
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.CLAUDE_CODE,
      childTurn: { user: HELD_CHILD_TASK },
    })
  })
})
