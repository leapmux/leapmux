import { cursorTest } from '../cursor-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

cursorTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
