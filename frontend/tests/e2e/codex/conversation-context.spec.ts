import { codexTest } from '../codex-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

codexTest('preserves the previous prompt and answer in the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
