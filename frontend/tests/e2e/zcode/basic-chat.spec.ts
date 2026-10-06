import { exerciseBasicChat } from '../helpers/nativeConversation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
