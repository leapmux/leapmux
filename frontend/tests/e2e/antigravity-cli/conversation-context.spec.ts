import { antigravityTest } from '../antigravity-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

antigravityTest('sends prior native conversation context into the next model request', async ({ native }) => {
  await exerciseConversationContext(native)
})
