import { exerciseConversationContext } from '../helpers/nativeConversation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
