import { exerciseConversationContext } from '../helpers/nativeConversation'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
