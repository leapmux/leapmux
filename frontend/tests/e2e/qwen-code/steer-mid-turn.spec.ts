import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { openWorkspace } from '../helpers/ui'
import { openQwenAgent, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.QWEN_CODE)
  })
})
