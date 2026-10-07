import { exerciseBasicChat } from '../helpers/nativeConversation'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
