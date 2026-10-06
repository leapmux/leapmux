import { commandCodeTest } from '../command-code-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

commandCodeTest('ends the native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
