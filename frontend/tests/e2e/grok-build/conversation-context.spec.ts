import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('proves both prior markers reach the next native request', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseConversationContext(context)
})
