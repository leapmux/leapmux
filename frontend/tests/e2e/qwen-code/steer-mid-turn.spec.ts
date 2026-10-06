import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { permissionMode: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.QWEN_CODE)
  })
})
