import { claudeTest } from '../claude-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

claudeTest('preserves the previous prompt and answer in the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
