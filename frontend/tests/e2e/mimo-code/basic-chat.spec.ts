import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('proves native prompt delivery, turn completion, and saved rows', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseBasicChat(context)
})
