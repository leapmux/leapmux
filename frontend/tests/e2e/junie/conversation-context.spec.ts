import { exerciseConversationContext } from '../helpers/nativeConversation'
import { junieTest } from '../junie-fixtures'

junieTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
