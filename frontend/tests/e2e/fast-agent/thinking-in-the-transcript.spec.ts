import { expect, FAST_AGENT_AGENT, fastAgentTest } from '../fastagent-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest('draws the reasoning in a thought band', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT, { model: MOCK_MODELS.zai })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { request } = await exerciseThinkingRows(context)
    expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
  })
})
