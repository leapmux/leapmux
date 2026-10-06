import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

codebuddyTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
