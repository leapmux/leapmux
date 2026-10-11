import { exerciseBasicChat } from '../helpers/nativeConversation'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
