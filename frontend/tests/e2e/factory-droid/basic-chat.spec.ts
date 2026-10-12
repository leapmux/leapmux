import { droidTest } from '../droid-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

droidTest('ends the actual native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})
