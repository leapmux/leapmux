import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Qwen Code through the Agent Client Protocol.
 *
 * Qwen tags foreground updates with the spawning tool call. Background children publish no stream, so the Worker reads their transcript files. The native task cancel stops a selected child.
 */
qwenTest.describe('Qwen Code subagent registry', () => {
  qwenTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.QWEN_CODE,
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { workingDir },
    })
  })
})
