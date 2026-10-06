import { commandCodeTest } from '../command-code-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

commandCodeTest('uses the earlier prompt and answer in the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
