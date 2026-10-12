import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

deepseekHarnessTest('uses prior user and assistant context in the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
