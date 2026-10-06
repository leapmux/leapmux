import { exerciseConversationContext } from '../helpers/nativeConversation'
import { qoderTest } from '../qoder-fixtures'

qoderTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
