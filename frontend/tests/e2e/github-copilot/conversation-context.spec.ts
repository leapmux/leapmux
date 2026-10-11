import { copilotTest } from '../copilot-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

copilotTest('carries earlier user and assistant text into the next native context', async ({ native }) => {
  await exerciseConversationContext(native)
})
