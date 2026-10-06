import { exerciseConversationContext } from '../helpers/nativeConversation'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
