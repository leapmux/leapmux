import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext, QWEN_AGENT } from './scenarios'

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { permissionMode: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerBeforeTool(context)
  })
})
