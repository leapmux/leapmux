import { grokTest } from '../grok-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

grokTest('proves both prior markers reach the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
