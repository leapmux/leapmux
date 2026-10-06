import { exerciseConversationContext } from '../helpers/nativeConversation'
import { qwenTest } from '../qwen-fixtures'

qwenTest('proves both prior markers reach the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
