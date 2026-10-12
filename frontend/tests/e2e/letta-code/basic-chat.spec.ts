import { exerciseBasicChat } from '../helpers/nativeConversation'
import { lettaTest } from '../letta-fixtures'

lettaTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
