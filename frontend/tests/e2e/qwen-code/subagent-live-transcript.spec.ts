import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { qwenTest } from '../qwen-fixtures'
import { qwenChildTurn } from './childScenario'
import { nativeContext } from './scenarios'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Qwen Code through the Agent Client Protocol.
 *
 * Qwen tags foreground updates with the spawning tool call. Background children publish no stream, so the Worker reads their transcript files. The native task cancel stops a selected child.
 */
qwenTest.describe('Qwen Code subagent registry', () => {
  qwenTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openNativeAgent(context, { overrides: { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } } })
    const childTask = 'Reply with CHILD_LIVE_DONE.'
    await exerciseLiveChildTranscript(context, {
      childWhen: qwenChildTurn(childTask),
      childTask,
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir } },
    })
  })
})
