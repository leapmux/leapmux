import { cursorTest } from '../cursor-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

cursorTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
