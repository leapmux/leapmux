import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

deepseekHarnessTest('ends the native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
