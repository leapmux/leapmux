import { gooseTest } from '../goose-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

gooseTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
