/** Test native child interruption without stopping the parent. */
import { claudeTest } from '../claude-fixtures'
import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { nativeContext } from './scenarios'

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
    // Claude can start more root turns when it reports a stopped background
    // task. Their count depends on when the stop reaches its pending turn.
    await modelScript.fallback({ text: 'Notification noted.' })
    await exerciseChildInterrupt(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedWorkspace.workspaceId }), {
      childTurn: { user: HELD_CHILD_TASK },
    })
  })
})
