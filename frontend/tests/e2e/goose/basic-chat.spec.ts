import { gooseTest } from '../goose-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

gooseTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
