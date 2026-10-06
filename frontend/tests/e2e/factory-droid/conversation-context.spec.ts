import { droidTest } from '../droid-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

droidTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
