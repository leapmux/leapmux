import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseChildInterrupt, expectNoRegistryRows, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { openWorkspace } from '../helpers/ui'
import { openQwenAgent, qwenTest } from '../qwen-fixtures'

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
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page, leapmuxServer)
    // Qwen asks no session title for a child, so the task alone selects the
    // child's own turn.
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.QWEN_CODE,
      childTurn: { user: HELD_CHILD_TASK },
    })
  })
})
