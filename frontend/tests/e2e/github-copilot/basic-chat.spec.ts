import { copilotTest } from '../copilot-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

copilotTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
