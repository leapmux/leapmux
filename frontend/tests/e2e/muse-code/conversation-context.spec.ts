import { exerciseConversationContext } from '../helpers/nativeConversation'
import { museTest } from '../muse-fixtures'

museTest('uses the earlier prompt and answer in the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
