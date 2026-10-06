import { geminiTest } from '../gemini-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

geminiTest('completes a native conversation and preserves its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
