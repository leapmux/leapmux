import { exerciseBasicChat } from '../helpers/nativeConversation'
import { junieTest } from '../junie-fixtures'

junieTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
