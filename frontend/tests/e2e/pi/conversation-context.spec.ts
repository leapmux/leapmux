import { exerciseConversationContext } from '../helpers/nativeConversation'
import { piTest } from '../pi-fixtures'

piTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
