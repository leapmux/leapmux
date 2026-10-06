import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

fastAgentTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
