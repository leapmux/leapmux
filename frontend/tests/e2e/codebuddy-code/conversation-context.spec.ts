import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

codebuddyTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
