import { exerciseConversationContext } from '../helpers/nativeConversation'
import { lettaTest } from '../letta-fixtures'

lettaTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
