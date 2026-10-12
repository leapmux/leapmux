import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

fastAgentTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
