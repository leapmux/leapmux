import { diracTest } from '../dirac-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

diracTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})
