import { geminiTest } from '../gemini-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

geminiTest('sends prior native conversation context into the next model request', async ({ native }) => {
  await exerciseConversationContext(native)
})
