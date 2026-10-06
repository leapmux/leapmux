import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext } from './scenarios'

/**
 * The child tab interrupts only its actual native child. The parent must remain operational.
 *
 * The Worker drives Qwen Code through the Agent Client Protocol.
 *
 * Qwen tags foreground updates with the spawning tool call. Background children publish no stream, so the Worker reads their transcript files. The native task cancel stops a selected child.
 */
qwenTest.describe('Qwen Code subagent registry', () => {
  qwenTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openNativeAgent(context, { overrides: { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } } })
    // Qwen asks no session title for a child, so the task alone selects the
    // child's own turn.
    await exerciseChildInterrupt(context, {
      childTurn: { user: HELD_CHILD_TASK },
    })
  })
})
