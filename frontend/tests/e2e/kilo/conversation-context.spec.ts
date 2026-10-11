import { exerciseConversationContext } from '../helpers/nativeConversation'
import { kiloTest } from '../kilo-fixtures'

kiloTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
